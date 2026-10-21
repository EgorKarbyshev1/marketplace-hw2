package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"marketplace/internal/generated"
)

var ErrOrderNotFound = errors.New("order not found")

type Orders struct{ pool *pgxpool.Pool }

func NewOrders(pool *pgxpool.Pool) *Orders { return &Orders{pool: pool} }

func (r *Orders) Begin(ctx context.Context) (pgx.Tx, error) { return r.pool.Begin(ctx) }

func (r *Orders) LockUser(ctx context.Context, tx pgx.Tx, user uuid.UUID) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, user.String())
	return err
}

func (r *Orders) RecentOperation(ctx context.Context, tx pgx.Tx, user uuid.UUID, operation string, interval time.Duration) (bool, error) {
	var limited bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM user_operations WHERE user_id=$1::text::uuid
        AND operation_type=$2 AND created_at > clock_timestamp() - ($3::bigint * interval '1 microsecond'))`,
		user.String(), operation, interval.Microseconds()).Scan(&limited)
	return limited, err
}

func (r *Orders) HasActive(ctx context.Context, tx pgx.Tx, user uuid.UUID) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM orders WHERE user_id=$1::text::uuid AND status IN ('CREATED','PAYMENT_PENDING'))`, user.String()).Scan(&exists)
	return exists, err
}

func (r *Orders) RecordOperation(ctx context.Context, tx pgx.Tx, user uuid.UUID, operation string) error {
	_, err := tx.Exec(ctx, `INSERT INTO user_operations(id,user_id,operation_type) VALUES($1::text::uuid,$2::text::uuid,$3)`, uuid.NewString(), user.String(), operation)
	return err
}

func (r *Orders) Load(ctx context.Context, tx pgx.Tx, id uuid.UUID, lock bool) (generated.OrderResponse, error) {
	var order generated.OrderResponse
	var idText, userText, total, discount string
	var promo *string
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	err := tx.QueryRow(ctx, `SELECT id::text,user_id::text,status,promo_code_id::text,total_amount::text,
        discount_amount::text,created_at,updated_at FROM orders WHERE id=$1::text::uuid`+suffix, id.String()).Scan(
		&idText, &userText, &order.Status, &promo, &total, &discount, &order.CreatedAt, &order.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return order, ErrOrderNotFound
	}
	if err != nil {
		return order, err
	}
	order.Id = uuid.MustParse(idText)
	order.UserId = uuid.MustParse(userText)
	order.TotalAmount = json.Number(total)
	order.DiscountAmount = json.Number(discount)
	if promo != nil {
		value := uuid.MustParse(*promo)
		order.PromoCodeId = &value
	}
	order.Items = []generated.OrderItemResponse{}
	rows, err := tx.Query(ctx, `SELECT id::text,product_id::text,quantity,price_at_order::text FROM order_items WHERE order_id=$1::text::uuid ORDER BY product_id`, id.String())
	if err != nil {
		return order, err
	}
	defer rows.Close()
	for rows.Next() {
		var item generated.OrderItemResponse
		var itemID, productID, price string
		if err := rows.Scan(&itemID, &productID, &item.Quantity, &price); err != nil {
			return order, err
		}
		item.Id = uuid.MustParse(itemID)
		item.ProductId = uuid.MustParse(productID)
		item.PriceAtOrder = json.Number(price)
		order.Items = append(order.Items, item)
	}
	return order, rows.Err()
}

func (r *Orders) LockProducts(ctx context.Context, tx pgx.Tx, ids []string) (map[uuid.UUID]generated.ProductResponse, error) {
	rows, err := tx.Query(ctx, `SELECT `+productColumns+` FROM products WHERE id::text=ANY($1::text[]) ORDER BY id FOR UPDATE`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	products := map[uuid.UUID]generated.ProductResponse{}
	for rows.Next() {
		product, err := scanProduct(rows)
		if err != nil {
			return nil, err
		}
		products[product.Id] = product
	}
	return products, rows.Err()
}

func (r *Orders) AdjustStock(ctx context.Context, tx pgx.Tx, id uuid.UUID, delta int32) error {
	_, err := tx.Exec(ctx, `UPDATE products SET stock=stock+$2 WHERE id=$1::text::uuid`, id.String(), delta)
	return err
}

func (r *Orders) Insert(ctx context.Context, tx pgx.Tx, order generated.OrderResponse) error {
	var promo any
	if order.PromoCodeId != nil {
		promo = order.PromoCodeId.String()
	}
	_, err := tx.Exec(ctx, `INSERT INTO orders(id,user_id,status,promo_code_id,total_amount,discount_amount)
        VALUES($1::text::uuid,$2::text::uuid,$3,$4::text::uuid,$5::text::numeric,$6::text::numeric)`,
		order.Id.String(), order.UserId.String(), string(order.Status), promo, order.TotalAmount.String(), order.DiscountAmount.String())
	if err != nil {
		return err
	}
	return r.ReplaceItems(ctx, tx, order)
}

func (r *Orders) Save(ctx context.Context, tx pgx.Tx, order generated.OrderResponse) error {
	var promo any
	if order.PromoCodeId != nil {
		promo = order.PromoCodeId.String()
	}
	_, err := tx.Exec(ctx, `UPDATE orders SET status=$2,promo_code_id=$3::text::uuid,total_amount=$4::text::numeric,
        discount_amount=$5::text::numeric WHERE id=$1::text::uuid`, order.Id.String(), string(order.Status), promo, order.TotalAmount.String(), order.DiscountAmount.String())
	return err
}

func (r *Orders) ReplaceItems(ctx context.Context, tx pgx.Tx, order generated.OrderResponse) error {
	if _, err := tx.Exec(ctx, `DELETE FROM order_items WHERE order_id=$1::text::uuid`, order.Id.String()); err != nil {
		return err
	}
	for _, item := range order.Items {
		if _, err := tx.Exec(ctx, `INSERT INTO order_items(id,order_id,product_id,quantity,price_at_order)
            VALUES($1::text::uuid,$2::text::uuid,$3::text::uuid,$4,$5::text::numeric)`,
			item.Id.String(), order.Id.String(), item.ProductId.String(), item.Quantity, item.PriceAtOrder.String()); err != nil {
			return err
		}
	}
	return nil
}

func (r *Orders) LockPromo(ctx context.Context, tx pgx.Tx, code string, id *uuid.UUID) (generated.PromoCodeResponse, error) {
	var promo generated.PromoCodeResponse
	var idText, value, minimum string
	var idValue any
	if id != nil {
		idValue = id.String()
	}
	err := tx.QueryRow(ctx, `SELECT id::text,code,discount_type,discount_value::text,min_order_amount::text,
        max_uses,current_uses,valid_from,valid_until,active FROM promo_codes
        WHERE ($1::text IS NOT NULL AND id=$1::text::uuid) OR ($1::text IS NULL AND code=$2) FOR UPDATE`, idValue, code).Scan(
		&idText, &promo.Code, &promo.DiscountType, &value, &minimum, &promo.MaxUses, &promo.CurrentUses, &promo.ValidFrom, &promo.ValidUntil, &promo.Active)
	if err != nil {
		return promo, err
	}
	promo.Id = uuid.MustParse(idText)
	promo.DiscountValue = json.Number(value)
	promo.MinOrderAmount = json.Number(minimum)
	return promo, nil
}

func (r *Orders) PromoUses(ctx context.Context, tx pgx.Tx, id uuid.UUID, delta int) error {
	_, err := tx.Exec(ctx, `UPDATE promo_codes SET current_uses=current_uses+$2 WHERE id=$1::text::uuid`, id.String(), delta)
	return err
}

func (r *Orders) CreatePromo(ctx context.Context, input generated.PromoCodeCreate) (generated.PromoCodeResponse, error) {
	var result generated.PromoCodeResponse
	id := uuid.New()
	_, err := r.pool.Exec(ctx, `INSERT INTO promo_codes(id,code,discount_type,discount_value,min_order_amount,max_uses,valid_from,valid_until,active)
        VALUES($1::text::uuid,$2,$3,$4::text::numeric,$5::text::numeric,$6,$7,$8,$9)`, id.String(), input.Code, string(input.DiscountType),
		input.DiscountValue.String(), input.MinOrderAmount.String(), input.MaxUses, input.ValidFrom, input.ValidUntil, input.Active)
	if err != nil {
		return result, err
	}
	result = generated.PromoCodeResponse{Id: id, Code: input.Code, DiscountType: input.DiscountType, DiscountValue: input.DiscountValue,
		MinOrderAmount: input.MinOrderAmount, MaxUses: input.MaxUses, ValidFrom: input.ValidFrom, ValidUntil: input.ValidUntil, Active: input.Active, CurrentUses: 0}
	return result, nil
}
