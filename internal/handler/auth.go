package handler

import (
	"marketplace/internal/generated"
	"net/http"
)

func (p *Products) Register(w http.ResponseWriter, r *http.Request) {
	var input generated.Credentials
	if err := decodeJSON(r.Body, &input); err != nil {
		writeDecodeError(w, err)
		return
	}
	user, err := p.auth.Register(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 201, user)
}
func (p *Products) Login(w http.ResponseWriter, r *http.Request) {
	var input generated.Credentials
	if err := decodeJSON(r.Body, &input); err != nil {
		writeDecodeError(w, err)
		return
	}
	pair, err := p.auth.Login(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, pair)
}
func (p *Products) Refresh(w http.ResponseWriter, r *http.Request) {
	var input generated.RefreshRequest
	if err := decodeJSON(r.Body, &input); err != nil {
		writeDecodeError(w, err)
		return
	}
	pair, err := p.auth.Refresh(r.Context(), input.RefreshToken)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, pair)
}
