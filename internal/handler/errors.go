package handler

import (
	"errors"
	"log/slog"
	"net/http"

	"marketplace/internal/generated"
	"marketplace/internal/repository"
	"marketplace/internal/service"
)

func writeAPIError(w http.ResponseWriter, status int, code generated.ErrorCode, message string, details map[string]any) {
	response := generated.ErrorResponse{ErrorCode: code, Message: message}
	if len(details) > 0 {
		response.Details = &details
	}
	writeJSON(w, status, response)
}

func writeError(w http.ResponseWriter, err error) {
	var validation *service.ValidationError
	var failure *service.BusinessError
	switch {
	case errors.As(err, &failure):
		status := http.StatusConflict
		switch failure.Code {
		case "PRODUCT_NOT_FOUND", "ORDER_NOT_FOUND":
			status = http.StatusNotFound
		case "TOKEN_INVALID", "TOKEN_EXPIRED", "REFRESH_TOKEN_INVALID":
			status = http.StatusUnauthorized
		case "ORDER_OWNERSHIP_VIOLATION", "ACCESS_DENIED":
			status = http.StatusForbidden
		case "ORDER_LIMIT_EXCEEDED":
			status = http.StatusTooManyRequests
		case "PROMO_CODE_INVALID", "PROMO_CODE_MIN_AMOUNT":
			status = http.StatusUnprocessableEntity
		}
		writeAPIError(w, status, failure.Code, failure.Message, failure.Details)
	case errors.Is(err, repository.ErrAccessDenied):
		writeAPIError(w, 403, "ACCESS_DENIED", "Access denied", nil)
	case errors.Is(err, repository.ErrNotFound):
		writeAPIError(w, http.StatusNotFound, "PRODUCT_NOT_FOUND", "Product not found", nil)
	case errors.As(err, &validation):
		writeValidation(w, validation.Details)
	case errors.Is(err, service.ErrInvalidInput):
		writeValidation(w, map[string]any{"body": "Invalid product or pagination"})
	default:
		slog.Error("operation failed", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error", nil)
	}
}
