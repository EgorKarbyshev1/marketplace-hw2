package repository

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"marketplace/internal/generated"
	"marketplace/internal/security"
)

var ErrNotFound = errors.New("product not found")
var ErrAccessDenied = errors.New("access denied")

const productColumns = `id::text, name, description, price::text, stock, category, status, created_at, updated_at, seller_id::text`

type Products struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Products {
	return &Products{pool: pool}
}

func (p *Products) Create(ctx context.Context, input generated.ProductCreate) (generated.ProductResponse, error) {
	query := `INSERT INTO products (id, name, description, price, stock, category, status, seller_id)
        VALUES ($1::text::uuid, $2, $3, $4::text::numeric, $5, $6, $7, $8::text::uuid) RETURNING ` + productColumns
	return scanProduct(p.pool.QueryRow(ctx, query, uuid.NewString(), input.Name, input.Description,
		input.Price.String(), input.Stock, input.Category, string(input.Status), security.Current(ctx).ID.String()))
}

func (p *Products) Get(ctx context.Context, id uuid.UUID) (generated.ProductResponse, error) {
	return scanProduct(p.pool.QueryRow(ctx, `SELECT `+productColumns+` FROM products WHERE id = $1::text::uuid`, id.String()))
}

func (p *Products) Update(ctx context.Context, id uuid.UUID, input generated.ProductUpdate) (generated.ProductResponse, error) {
	query := `UPDATE products SET name = $2, description = $3, price = $4::text::numeric,
        stock = $5, category = $6, status = $7 WHERE id = $1::text::uuid AND (seller_id=$8::text::uuid OR $9::boolean) RETURNING ` + productColumns
	result, err := scanProduct(p.pool.QueryRow(ctx, query, id.String(), input.Name, input.Description,
		input.Price.String(), input.Stock, input.Category, string(input.Status), security.Current(ctx).ID.String(), security.IsAdmin(ctx)))
	if errors.Is(err, ErrNotFound) {
		if _, exists := p.Get(ctx, id); exists == nil {
			return result, ErrAccessDenied
		}
	}
	return result, err
}

func (p *Products) Archive(ctx context.Context, id uuid.UUID) error {
	result, err := p.pool.Exec(ctx, `UPDATE products SET status = 'ARCHIVED' WHERE id = $1::text::uuid AND (seller_id=$2::text::uuid OR $3::boolean)`, id.String(), security.Current(ctx).ID.String(), security.IsAdmin(ctx))
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		if _, exists := p.Get(ctx, id); exists == nil {
			return ErrAccessDenied
		}
		return ErrNotFound
	}
	return nil
}

func (p *Products) List(ctx context.Context, page, size int32, status *generated.ProductStatus, category *string) (generated.ProductPage, error) {
	result := generated.ProductPage{Items: []generated.ProductResponse{}, Page: page, Size: size}
	var statusValue *string
	if status != nil {
		value := string(*status)
		statusValue = &value
	}
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	filter := ` WHERE ($1::text IS NULL OR status = $1) AND ($2::text IS NULL OR category = $2)`
	err = tx.QueryRow(ctx, `SELECT COUNT(*) FROM products`+filter, statusValue, category).Scan(&result.TotalElements)
	if err != nil {
		return result, err
	}
	rows, err := tx.Query(ctx, `SELECT `+productColumns+` FROM products`+filter+` ORDER BY id LIMIT $3 OFFSET $4`,
		statusValue, category, size, int64(page)*int64(size))
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		product, err := scanProduct(rows)
		if err != nil {
			return result, err
		}
		result.Items = append(result.Items, product)
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}

type scanner interface {
	Scan(...any) error
}

func scanProduct(row scanner) (generated.ProductResponse, error) {
	var product generated.ProductResponse
	var id, price string
	var seller *string
	err := row.Scan(&id, &product.Name, &product.Description, &price, &product.Stock,
		&product.Category, &product.Status, &product.CreatedAt, &product.UpdatedAt, &seller)
	if errors.Is(err, pgx.ErrNoRows) {
		return product, ErrNotFound
	}
	if err != nil {
		return product, err
	}
	product.Id, err = uuid.Parse(id)
	product.Price = json.Number(price)
	if seller != nil {
		value := uuid.MustParse(*seller)
		product.SellerId = &value
	}
	return product, err
}
