package handler

import (
	"marketplace/internal/generated"
	"marketplace/internal/security"
	"net/http"
)

func (p *Products) CreateOrder(w http.ResponseWriter, r *http.Request) {
	var input generated.OrderCreate
	if err := decodeJSON(r.Body, &input); err != nil {
		writeDecodeError(w, err)
		return
	}
	result, err := p.orders.Create(r.Context(), security.Current(r.Context()).ID, input)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Location", "/orders/"+result.Id.String())
	writeJSON(w, http.StatusCreated, result)
}
func (p *Products) GetOrder(w http.ResponseWriter, r *http.Request, id generated.OrderId) {
	result, err := p.orders.Get(r.Context(), id, security.Current(r.Context()).ID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
func (p *Products) UpdateOrder(w http.ResponseWriter, r *http.Request, id generated.OrderId) {
	var input generated.OrderUpdate
	if err := decodeJSON(r.Body, &input); err != nil {
		writeDecodeError(w, err)
		return
	}
	result, err := p.orders.Update(r.Context(), id, security.Current(r.Context()).ID, input)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
func (p *Products) CancelOrder(w http.ResponseWriter, r *http.Request, id generated.OrderId) {
	result, err := p.orders.Cancel(r.Context(), id, security.Current(r.Context()).ID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
func (p *Products) ChangeOrderStatus(w http.ResponseWriter, r *http.Request, id generated.OrderId) {
	var input generated.OrderStatusUpdate
	if err := decodeJSON(r.Body, &input); err != nil {
		writeDecodeError(w, err)
		return
	}
	result, err := p.orders.ChangeStatus(r.Context(), id, security.Current(r.Context()).ID, input.Status)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
func (p *Products) CreatePromoCode(w http.ResponseWriter, r *http.Request) {
	var input generated.PromoCodeCreate
	if err := decodeJSON(r.Body, &input); err != nil {
		writeDecodeError(w, err)
		return
	}
	result, err := p.orders.CreatePromo(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}
