package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"
	"marketplace/internal/generated"
	"marketplace/internal/service"
)

func validateRequests(next http.Handler) (http.Handler, error) {
	spec, err := generated.GetSwagger()
	if err != nil {
		return nil, err
	}
	spec.Servers = nil
	for _, name := range []string{"ProductCreate", "ProductUpdate"} {
		price := spec.Components.Schemas[name].Value.Properties["price"].Value
		price.Min, price.Max, price.MultipleOf = nil, nil, nil
	}
	for _, name := range []string{"Money", "PositiveMoney"} {
		price := spec.Components.Schemas[name].Value
		price.Min, price.Max, price.MultipleOf = nil, nil, nil
	}
	router, err := gorillamux.NewRouter(spec)
	if err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route, params, err := router.FindRoute(r)
		if err != nil {
			if errors.Is(err, routers.ErrMethodNotAllowed) {
				allowed := []string{}
				for _, method := range []string{"GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"} {
					probe := r.Clone(r.Context())
					probe.Method = method
					if _, _, err := router.FindRoute(probe); err == nil {
						allowed = append(allowed, method)
					}
				}
				w.Header().Set("Allow", strings.Join(allowed, ", "))
				writeAPIError(w, 405, "METHOD_NOT_ALLOWED", "Method not allowed", nil)
			} else {
				writeAPIError(w, 404, "ROUTE_NOT_FOUND", "Route not found", nil)
			}
			return
		}
		fields := map[string]any{}
		for _, field := range []string{"page", "size"} {
			if values, present := r.URL.Query()[field]; present && (len(values) != 1 || values[0] == "") {
				fields[field] = "Must contain one integer value"
			}
		}
		if route.Operation.RequestBody != nil {
			if !jsonContentType(r.Header.Get("Content-Type")) {
				writeValidation(w, map[string]any{"body": "Content-Type must be application/json"})
				return
			}
			data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
			if err != nil {
				writeValidation(w, map[string]any{"body": "Request body is too large or cannot be read"})
				return
			}
			var object map[string]any
			if err := decodeJSON(bytes.NewReader(data), &object); err != nil || object == nil {
				writeValidation(w, map[string]any{"body": "Body must contain exactly one JSON object"})
				return
			}
			if number, ok := object["price"].(json.Number); ok {
				if err := service.ValidatePrice(number); err != nil {
					var validation *service.ValidationError
					if errors.As(err, &validation) {
						for field, reason := range validation.Details {
							fields[field] = reason
						}
					}
				}
			}
			for _, field := range []string{"discount_value", "min_order_amount"} {
				if number, ok := object[field].(json.Number); ok {
					if field == "min_order_amount" && moneyIsZero(number) {
						continue
					}
					if err := service.ValidatePrice(number); err != nil {
						var validation *service.ValidationError
						if errors.As(err, &validation) {
							fields[field] = validation.Details["price"]
						}
					}
				}
			}
			r.Body = io.NopCloser(bytes.NewReader(data))
		}
		input := &openapi3filter.RequestValidationInput{
			Request: r, PathParams: params, Route: route,
			Options: &openapi3filter.Options{MultiError: true, SkipSettingDefaults: true, AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
		}
		if err := openapi3filter.ValidateRequest(r.Context(), input); err != nil {
			collectFields(err, "body", fields)
		}
		if len(fields) > 0 {
			writeValidation(w, fields)
			return
		}
		next.ServeHTTP(w, r)
	}), nil
}

func decodeJSON(reader io.Reader, target any) error {
	decoder := json.NewDecoder(reader)
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("body must contain exactly one JSON object")
	}
	return nil
}

func collectFields(err error, field string, fields map[string]any) {
	switch problem := err.(type) {
	case openapi3.MultiError:
		for _, item := range problem {
			collectFields(item, field, fields)
		}
	case *openapi3filter.RequestError:
		if problem.Parameter != nil {
			field = problem.Parameter.Name
		}
		if problem.Err != nil {
			collectFields(problem.Err, field, fields)
		} else {
			fields[field] = "Required value is missing or has an unsupported format"
		}
	case *openapi3.SchemaError:
		if pointer := problem.JSONPointer(); len(pointer) > 0 && field == "body" {
			field = strings.Join(pointer, ".")
		}
		if _, exists := fields[field]; !exists {
			fields[field] = problem.Reason
		}
	default:
		fields[field] = "Invalid type or format"
	}
}

func writeValidation(w http.ResponseWriter, fields map[string]any) {
	writeAPIError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Validation failed", fields)
}

func jsonContentType(header string) bool {
	contentType, _, err := mime.ParseMediaType(header)
	return err == nil && contentType == "application/json"
}

func writeDecodeError(w http.ResponseWriter, err error) {
	field, reason := "body", "Body must contain exactly one JSON object"
	var typeError *json.UnmarshalTypeError
	if errors.As(err, &typeError) {
		field, reason = typeError.Field, "Invalid type or format"
	}
	writeValidation(w, map[string]any{field: reason})
}

func moneyIsZero(number json.Number) bool {
	coefficient := strings.Split(strings.ToLower(number.String()), "e")[0]
	return strings.Trim(coefficient, "-0.") == ""
}
