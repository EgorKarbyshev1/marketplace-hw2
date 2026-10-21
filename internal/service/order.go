package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"marketplace/internal/generated"
	"marketplace/internal/repository"
	"marketplace/internal/security"
)

type BusinessError struct {
	Code    generated.ErrorCode
	Message string
	Details map[string]any
}

func (e *BusinessError) Error() string { return e.Message }
func business(code generated.ErrorCode, message string) error {
	return &BusinessError{Code: code, Message: message}
}

type Orders struct {
	repository *repository.Orders
	interval   time.Duration
}

func NewOrders(repository *repository.Orders, interval time.Duration) *Orders {
	return &Orders{repository: repository, interval: interval}
}

func (s *Orders) transaction(ctx context.Context, action func(pgx.Tx) (generated.OrderResponse, error)) (generated.OrderResponse, error) {
	tx, err := s.repository.Begin(ctx)
	if err != nil {
		return generated.OrderResponse{}, err
	}
	defer tx.Rollback(ctx)
	result, err := action(tx)
	if err != nil {
		return generated.OrderResponse{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return generated.OrderResponse{}, err
	}
	return result, nil
}

func (s *Orders) rateLimit(ctx context.Context, tx pgx.Tx, user uuid.UUID, operation string) error {
	limited, err := s.repository.RecentOperation(ctx, tx, user, operation, s.interval)
	if err != nil {
		return err
	}
	if limited {
		return business("ORDER_LIMIT_EXCEEDED", "Order operation rate limit exceeded")
	}
	return nil
}

func (s *Orders) Create(ctx context.Context, user uuid.UUID, input generated.OrderCreate) (generated.OrderResponse, error) {
	if err := validateItems(input.Items); err != nil {
		return generated.OrderResponse{}, err
	}
	return s.transaction(ctx, func(tx pgx.Tx) (generated.OrderResponse, error) {
		var order generated.OrderResponse
		if err := s.repository.LockUser(ctx, tx, user); err != nil {
			return order, err
		}
		if err := s.rateLimit(ctx, tx, user, "CREATE_ORDER"); err != nil {
			return order, err
		}
		active, err := s.repository.HasActive(ctx, tx, user)
		if err != nil {
			return order, err
		}
		if active {
			return order, business("ORDER_HAS_ACTIVE", "User already has an active order")
		}
		order = generated.OrderResponse{Id: uuid.New(), UserId: user, Status: generated.OrderStatus("CREATED")}
		products, err := s.repository.LockProducts(ctx, tx, productIDs(input.Items, nil))
		if err != nil {
			return order, err
		}
		items, total, err := s.reserve(ctx, tx, input.Items, products)
		if err != nil {
			return order, err
		}
		order.Items = items
		discount := int64(0)
		if input.PromoCode != nil {
			promo, err := s.repository.LockPromo(ctx, tx, *input.PromoCode, nil)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return order, business("PROMO_CODE_INVALID", "Promo code is unavailable")
				}
				return order, err
			}
			if err := validatePromo(promo, false); err != nil {
				return order, err
			}
			if total < moneyCents(promo.MinOrderAmount) {
				return order, business("PROMO_CODE_MIN_AMOUNT", "Order amount is below the promo code minimum")
			}
			discount = calculateDiscount(total, promo)
			order.PromoCodeId = &promo.Id
			if err := s.repository.PromoUses(ctx, tx, promo.Id, 1); err != nil {
				return order, err
			}
		}
		if err := setOrderAmounts(&order, total, discount); err != nil {
			return order, err
		}
		if err := s.repository.Insert(ctx, tx, order); err != nil {
			return order, err
		}
		if err := s.repository.RecordOperation(ctx, tx, user, "CREATE_ORDER"); err != nil {
			return order, err
		}
		return s.repository.Load(ctx, tx, order.Id, false)
	})
}

func (s *Orders) owned(ctx context.Context, tx pgx.Tx, id, user uuid.UUID) (generated.OrderResponse, error) {
	order, err := s.repository.Load(ctx, tx, id, true)
	if errors.Is(err, repository.ErrOrderNotFound) {
		return order, business("ORDER_NOT_FOUND", "Order not found")
	}
	if err != nil {
		return order, err
	}
	if order.UserId != user && !security.IsAdmin(ctx) {
		return order, business("ORDER_OWNERSHIP_VIOLATION", "Order belongs to another user")
	}
	return order, nil
}

func (s *Orders) Get(ctx context.Context, id, user uuid.UUID) (generated.OrderResponse, error) {
	return s.transaction(ctx, func(tx pgx.Tx) (generated.OrderResponse, error) { return s.owned(ctx, tx, id, user) })
}

func (s *Orders) Update(ctx context.Context, id, user uuid.UUID, input generated.OrderUpdate) (generated.OrderResponse, error) {
	if err := validateItems(input.Items); err != nil {
		return generated.OrderResponse{}, err
	}
	return s.transaction(ctx, func(tx pgx.Tx) (generated.OrderResponse, error) {
		var order generated.OrderResponse
		if err := s.repository.LockUser(ctx, tx, user); err != nil {
			return order, err
		}
		order, err := s.owned(ctx, tx, id, user)
		if err != nil {
			return order, err
		}
		if order.Status != "CREATED" {
			return order, business("INVALID_STATE_TRANSITION", "Only CREATED orders can be updated")
		}
		if err := s.rateLimit(ctx, tx, user, "UPDATE_ORDER"); err != nil {
			return order, err
		}
		products, err := s.repository.LockProducts(ctx, tx, productIDs(input.Items, order.Items))
		if err != nil {
			return order, err
		}
		for _, item := range order.Items {
			if err := s.repository.AdjustStock(ctx, tx, item.ProductId, item.Quantity); err != nil {
				return order, err
			}
			product := products[item.ProductId]
			product.Stock += item.Quantity
			products[item.ProductId] = product
		}
		items, total, err := s.reserve(ctx, tx, input.Items, products)
		if err != nil {
			return order, err
		}
		discount := int64(0)
		if order.PromoCodeId != nil {
			promo, err := s.repository.LockPromo(ctx, tx, "", order.PromoCodeId)
			if err != nil {
				return order, err
			}
			if err := validatePromo(promo, true); err != nil {
				return order, err
			}
			if total < moneyCents(promo.MinOrderAmount) {
				if err := s.repository.PromoUses(ctx, tx, promo.Id, -1); err != nil {
					return order, err
				}
				order.PromoCodeId = nil
			} else {
				discount = calculateDiscount(total, promo)
			}
		}
		order.Items = items
		if err := setOrderAmounts(&order, total, discount); err != nil {
			return order, err
		}
		if err := s.repository.ReplaceItems(ctx, tx, order); err != nil {
			return order, err
		}
		if err := s.repository.Save(ctx, tx, order); err != nil {
			return order, err
		}
		if err := s.repository.RecordOperation(ctx, tx, user, "UPDATE_ORDER"); err != nil {
			return order, err
		}
		return s.repository.Load(ctx, tx, id, false)
	})
}

func (s *Orders) Cancel(ctx context.Context, id, user uuid.UUID) (generated.OrderResponse, error) {
	return s.transaction(ctx, func(tx pgx.Tx) (generated.OrderResponse, error) {
		var order generated.OrderResponse
		if err := s.repository.LockUser(ctx, tx, user); err != nil {
			return order, err
		}
		order, err := s.owned(ctx, tx, id, user)
		if err != nil {
			return order, err
		}
		if order.Status != "CREATED" && order.Status != "PAYMENT_PENDING" {
			return order, business("INVALID_STATE_TRANSITION", "Order cannot be canceled in its current state")
		}
		if _, err := s.repository.LockProducts(ctx, tx, productIDs(nil, order.Items)); err != nil {
			return order, err
		}
		for _, item := range order.Items {
			if err := s.repository.AdjustStock(ctx, tx, item.ProductId, item.Quantity); err != nil {
				return order, err
			}
		}
		if order.PromoCodeId != nil {
			if _, err := s.repository.LockPromo(ctx, tx, "", order.PromoCodeId); err != nil {
				return order, err
			}
			if err := s.repository.PromoUses(ctx, tx, *order.PromoCodeId, -1); err != nil {
				return order, err
			}
		}
		order.Status = "CANCELED"
		if err := s.repository.Save(ctx, tx, order); err != nil {
			return order, err
		}
		return s.repository.Load(ctx, tx, id, false)
	})
}

func (s *Orders) ChangeStatus(ctx context.Context, id, user uuid.UUID, status generated.OrderStatus) (generated.OrderResponse, error) {
	return s.transaction(ctx, func(tx pgx.Tx) (generated.OrderResponse, error) {
		var order generated.OrderResponse
		if err := s.repository.LockUser(ctx, tx, user); err != nil {
			return order, err
		}
		order, err := s.owned(ctx, tx, id, user)
		if err != nil {
			return order, err
		}
		next := map[generated.OrderStatus]generated.OrderStatus{"CREATED": "PAYMENT_PENDING", "PAYMENT_PENDING": "PAID", "PAID": "SHIPPED", "SHIPPED": "COMPLETED"}
		if next[order.Status] != status || status == "" {
			return order, business("INVALID_STATE_TRANSITION", "Order status transition is not allowed")
		}
		order.Status = status
		if err := s.repository.Save(ctx, tx, order); err != nil {
			return order, err
		}
		return s.repository.Load(ctx, tx, id, false)
	})
}

func (s *Orders) reserve(ctx context.Context, tx pgx.Tx, input []generated.OrderItemRequest, products map[uuid.UUID]generated.ProductResponse) ([]generated.OrderItemResponse, int64, error) {
	for _, item := range input {
		product, ok := products[item.ProductId]
		if !ok {
			return nil, 0, business("PRODUCT_NOT_FOUND", "Product not found")
		}
		if product.Status != generated.ACTIVE {
			return nil, 0, business("PRODUCT_INACTIVE", "Product is not active")
		}
	}
	shortages := []map[string]any{}
	for _, item := range input {
		if products[item.ProductId].Stock < item.Quantity {
			shortages = append(shortages, map[string]any{"product_id": item.ProductId, "requested_quantity": item.Quantity, "available_stock": products[item.ProductId].Stock})
		}
	}
	if len(shortages) > 0 {
		return nil, 0, &BusinessError{Code: "INSUFFICIENT_STOCK", Message: "Insufficient stock", Details: map[string]any{"products": shortages}}
	}
	items := make([]generated.OrderItemResponse, 0, len(input))
	total := int64(0)
	for _, item := range input {
		product := products[item.ProductId]
		total += moneyCents(product.Price) * int64(item.Quantity)
		items = append(items, generated.OrderItemResponse{Id: uuid.New(), ProductId: item.ProductId, Quantity: item.Quantity, PriceAtOrder: product.Price})
	}
	for _, item := range input {
		if err := s.repository.AdjustStock(ctx, tx, item.ProductId, -item.Quantity); err != nil {
			return nil, 0, err
		}
	}
	return items, total, nil
}

func validateItems(items []generated.OrderItemRequest) error {
	if len(items) < 1 || len(items) > 50 {
		return &ValidationError{Details: map[string]any{"items": "Must contain 1 to 50 items"}}
	}
	seen := map[uuid.UUID]bool{}
	for _, item := range items {
		if seen[item.ProductId] || item.Quantity < 1 || item.Quantity > 999 {
			return &ValidationError{Details: map[string]any{"items": "Product IDs must be unique and quantities must be 1 to 999"}}
		}
		seen[item.ProductId] = true
	}
	return nil
}

func productIDs(items []generated.OrderItemRequest, previous []generated.OrderItemResponse) []string {
	ids := map[string]bool{}
	for _, item := range items {
		ids[item.ProductId.String()] = true
	}
	for _, item := range previous {
		ids[item.ProductId.String()] = true
	}
	result := make([]string, 0, len(ids))
	for id := range ids {
		result = append(result, id)
	}
	return result
}

func validatePromo(promo generated.PromoCodeResponse, alreadyUsed bool) error {
	used := promo.CurrentUses
	if alreadyUsed {
		used--
	}
	now := time.Now()
	if !promo.Active || used >= promo.MaxUses || now.Before(promo.ValidFrom) || now.After(promo.ValidUntil) {
		return business("PROMO_CODE_INVALID", "Promo code is unavailable or expired")
	}
	return nil
}

func moneyCents(number json.Number) int64 {
	value, ok := new(big.Rat).SetString(number.String())
	if !ok {
		return 0
	}
	return new(big.Rat).Mul(value, big.NewRat(100, 1)).Num().Int64()
}
func moneyNumber(cents int64) json.Number {
	return json.Number(fmt.Sprintf("%d.%02d", cents/100, cents%100))
}

func calculateDiscount(total int64, promo generated.PromoCodeResponse) int64 {
	value := moneyCents(promo.DiscountValue)
	if promo.DiscountType == "FIXED_AMOUNT" {
		return min(value, total)
	}
	if value > 7000 {
		return 0
	}
	numerator := new(big.Int).Mul(big.NewInt(total), big.NewInt(value))
	numerator.Add(numerator, big.NewInt(5000))
	rounded := numerator.Quo(numerator, big.NewInt(10000)).Int64()
	return min(rounded, total*70/100)
}

func (s *Orders) CreatePromo(ctx context.Context, input generated.PromoCodeCreate) (generated.PromoCodeResponse, error) {
	if input.ValidUntil.Before(input.ValidFrom) {
		return generated.PromoCodeResponse{}, &ValidationError{Details: map[string]any{"valid_until": "Must not precede valid_from"}}
	}
	result, err := s.repository.CreatePromo(ctx, input)
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) && databaseError.Code == "23505" {
		return result, &ValidationError{Details: map[string]any{"code": "Promo code already exists"}}
	}
	return result, err
}

func setOrderAmounts(order *generated.OrderResponse, total, discount int64) error {
	if total-discount > 999999999999 || discount > 999999999999 {
		return &ValidationError{Details: map[string]any{"items": "Final amount or discount exceeds 9999999999.99"}}
	}
	order.TotalAmount = moneyNumber(total - discount)
	order.DiscountAmount = moneyNumber(discount)
	return nil
}
