package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"marketplace/internal/generated"
	"marketplace/internal/service"
)

type Products struct {
	service *service.Products
	orders  *service.Orders
	auth    *service.Auth
}

var _ generated.ServerInterface = (*Products)(nil)

func New(products *service.Products, orders *service.Orders, accounts ...*service.Auth) (http.Handler, error) {
	var auth *service.Auth
	if len(accounts) > 0 {
		auth = accounts[0]
	}
	handler := generated.HandlerWithOptions(&Products{service: products, orders: orders, auth: auth}, generated.StdHTTPServerOptions{
		ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			field := "request"
			if problem, ok := err.(*generated.InvalidParamFormatError); ok {
				field = problem.ParamName
			}
			writeValidation(w, map[string]any{field: "Invalid type or format"})
		},
	})
	return validateRequests(handler)
}

func (p *Products) CreateProduct(w http.ResponseWriter, r *http.Request) {
	var input generated.ProductCreate
	if err := decodeJSON(r.Body, &input); err != nil {
		writeDecodeError(w, err)
		return
	}
	product, err := p.service.Create(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Location", "/products/"+product.Id.String())
	writeJSON(w, http.StatusCreated, product)
}

func (p *Products) GetProduct(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	product, err := p.service.Get(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, product)
}

func (p *Products) ListProducts(w http.ResponseWriter, r *http.Request, params generated.ListProductsParams) {
	products, err := p.service.List(r.Context(), params)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, products)
}

func (p *Products) UpdateProduct(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	var input generated.ProductUpdate
	if err := decodeJSON(r.Body, &input); err != nil {
		writeDecodeError(w, err)
		return
	}
	product, err := p.service.Update(r.Context(), id, input)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, product)
}

func (p *Products) DeleteProduct(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	if err := p.service.Archive(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		slog.Error("write response failed", "error", err)
	}
}
