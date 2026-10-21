package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"marketplace/internal/generated"
	"marketplace/internal/repository"
)

var ErrInvalidInput = errors.New("invalid product")

type Products struct {
	repository *repository.Products
}

func New(repository *repository.Products) *Products {
	return &Products{repository: repository}
}

func (p *Products) Create(ctx context.Context, input generated.ProductCreate) (generated.ProductResponse, error) {
	if err := validateProduct(input); err != nil {
		return generated.ProductResponse{}, err
	}
	return p.repository.Create(ctx, input)
}

func (p *Products) Get(ctx context.Context, id uuid.UUID) (generated.ProductResponse, error) {
	return p.repository.Get(ctx, id)
}

func (p *Products) Update(ctx context.Context, id uuid.UUID, input generated.ProductUpdate) (generated.ProductResponse, error) {
	if err := validateProduct(generated.ProductCreate(input)); err != nil {
		return generated.ProductResponse{}, err
	}
	return p.repository.Update(ctx, id, input)
}

func (p *Products) Archive(ctx context.Context, id uuid.UUID) error {
	return p.repository.Archive(ctx, id)
}

func (p *Products) List(ctx context.Context, params generated.ListProductsParams) (generated.ProductPage, error) {
	page, size := int32(0), int32(20)
	if params.Page != nil {
		page = *params.Page
	}
	if params.Size != nil {
		size = *params.Size
	}
	if page < 0 || size < 1 || size > 100 {
		return generated.ProductPage{}, fmt.Errorf("%w: invalid pagination", ErrInvalidInput)
	}
	return p.repository.List(ctx, page, size, params.Status, params.Category)
}

func validateProduct(input generated.ProductCreate) error {
	if length := utf8.RuneCountInString(input.Name); length < 1 || length > 255 {
		return fmt.Errorf("%w: name length must be 1 to 255", ErrInvalidInput)
	}
	if input.Description != nil && utf8.RuneCountInString(*input.Description) > 4000 {
		return fmt.Errorf("%w: description is too long", ErrInvalidInput)
	}
	if length := utf8.RuneCountInString(input.Category); length < 1 || length > 100 {
		return fmt.Errorf("%w: category length must be 1 to 100", ErrInvalidInput)
	}
	if input.Stock < 0 {
		return fmt.Errorf("%w: stock must be nonnegative", ErrInvalidInput)
	}
	if input.Status != generated.ACTIVE && input.Status != generated.INACTIVE && input.Status != generated.ARCHIVED {
		return fmt.Errorf("%w: unsupported status", ErrInvalidInput)
	}
	return ValidatePrice(input.Price)
}

type ValidationError struct {
	Details map[string]any
}

func (e *ValidationError) Error() string {
	return "Validation failed"
}

func (e *ValidationError) Unwrap() error {
	return ErrInvalidInput
}

func ValidatePrice(price json.Number) error {
	invalid := func(message string) error {
		return &ValidationError{Details: map[string]any{"price": message}}
	}
	text := price.String()
	if index := strings.IndexAny(text, "eE"); index >= 0 {
		exponent, err := strconv.Atoi(text[index+1:])
		if err != nil || exponent < -(1<<20) || exponent > 1<<20 {
			return invalid("Price exponent is out of range")
		}
	}
	value, ok := new(big.Rat).SetString(text)
	if !ok {
		return invalid("Price must be a number")
	}
	if value.Cmp(big.NewRat(1, 100)) < 0 {
		return invalid("Price must be greater than or equal to 0.01")
	}
	if value.Cmp(big.NewRat(999999999999, 100)) > 0 {
		return invalid("Price must be less than or equal to 9999999999.99")
	}
	if cents := new(big.Rat).Mul(value, big.NewRat(100, 1)); !cents.IsInt() {
		return invalid("Price must have at most two decimal places")
	}
	return nil
}
