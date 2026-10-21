package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"marketplace/internal/generated"
	"marketplace/internal/repository"
)

const validProduct = `{"name":"Keyboard","price":1999.99,"stock":10,"category":"electronics","status":"ACTIVE"}`

func TestInvalidRequests(t *testing.T) {
	router, err := New(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, method, path, body, field, reason string
	}{
		{"missing stock", "POST", "/products", `{"name":"A","price":1,"category":"C","status":"ACTIVE"}`, "stock", "missing"},
		{"all required missing", "POST", "/products", `{}`, "name", "missing"},
		{"null stock", "POST", "/products", strings.Replace(validProduct, `"stock":10`, `"stock":null`, 1), "stock", ""},
		{"empty name", "POST", "/products", strings.Replace(validProduct, "Keyboard", "", 1), "name", ""},
		{"long name", "POST", "/products", strings.Replace(validProduct, "Keyboard", strings.Repeat("A", 256), 1), "name", ""},
		{"long Unicode name", "POST", "/products", strings.Replace(validProduct, "Keyboard", strings.Repeat("я", 256), 1), "name", ""},
		{"wrong name type", "POST", "/products", strings.Replace(validProduct, `"Keyboard"`, `12`, 1), "name", ""},
		{"empty category", "POST", "/products", strings.Replace(validProduct, "electronics", "", 1), "category", ""},
		{"long category", "POST", "/products", strings.Replace(validProduct, "electronics", strings.Repeat("A", 101), 1), "category", ""},
		{"long description", "POST", "/products", strings.Replace(validProduct, `"name":`, `"description":"`+strings.Repeat("A", 4001)+`","name":`, 1), "description", ""},
		{"null character", "POST", "/products", strings.Replace(validProduct, "Keyboard", `Key\u0000board`, 1), "name", ""},
		{"negative price", "POST", "/products", strings.Replace(validProduct, "1999.99", "-1", 1), "price", "greater than or equal to 0.01"},
		{"zero price", "POST", "/products", strings.Replace(validProduct, "1999.99", "0", 1), "price", "greater than or equal to 0.01"},
		{"fractional kopeck", "POST", "/products", strings.Replace(validProduct, "1999.99", "1.001", 1), "price", "two decimal places"},
		{"hidden fractional kopeck", "POST", "/products", strings.Replace(validProduct, "1999.99", "9999999998.9900001", 1), "price", "two decimal places"},
		{"large price", "POST", "/products", strings.Replace(validProduct, "1999.99", "10000000000", 1), "price", "less than or equal to"},
		{"string price", "POST", "/products", strings.Replace(validProduct, "1999.99", `"1.00"`, 1), "price", ""},
		{"negative stock", "POST", "/products", strings.Replace(validProduct, `"stock":10`, `"stock":-1`, 1), "stock", ""},
		{"large stock", "POST", "/products", strings.Replace(validProduct, `"stock":10`, `"stock":2147483648`, 1), "stock", ""},
		{"fractional stock", "POST", "/products", strings.Replace(validProduct, `"stock":10`, `"stock":1.5`, 1), "stock", ""},
		{"unknown status", "POST", "/products", strings.Replace(validProduct, "ACTIVE", "OTHER", 1), "status", ""},
		{"malformed JSON", "POST", "/products", `{`, "body", "one JSON object"},
		{"two objects", "POST", "/products", validProduct + validProduct, "body", "one JSON object"},
		{"trailing text", "POST", "/products", validProduct + " garbage", "body", "one JSON object"},
		{"trailing scalar", "POST", "/products", validProduct + " 123", "body", "one JSON object"},
		{"array body", "POST", "/products", `[]`, "body", "one JSON object"},
		{"null body", "POST", "/products", `null`, "body", "one JSON object"},
		{"invalid UUID", "GET", "/products/not-a-uuid", "", "id", ""},
		{"empty page", "GET", "/products?page=", "", "page", ""},
		{"empty size", "GET", "/products?size=", "", "size", ""},
		{"negative page", "GET", "/products?page=-1", "", "page", ""},
		{"noninteger page", "GET", "/products?page=abc", "", "page", ""},
		{"large page", "GET", "/products?page=2147483648", "", "page", ""},
		{"zero size", "GET", "/products?size=0", "", "size", ""},
		{"large size", "GET", "/products?size=101", "", "size", ""},
		{"noninteger size", "GET", "/products?size=1.5", "", "size", ""},
		{"invalid status filter", "GET", "/products?status=OTHER", "", "status", ""},
		{"empty category filter", "GET", "/products?category=", "", "category", ""},
		{"invalid update", "PUT", "/products/00000000-0000-0000-0000-000000000001", `{}`, "price", "missing"},
		{"two update objects", "PUT", "/products/00000000-0000-0000-0000-000000000001", validProduct + validProduct, "body", "one JSON object"},
		{"update trailing text", "PUT", "/products/00000000-0000-0000-0000-000000000001", validProduct + " text", "body", "one JSON object"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			if test.body != "" {
				request.Header.Set("Content-Type", "application/json")
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			problem := assertAPIError(t, response, 400, "VALIDATION_ERROR", "Validation failed")
			if problem.Details == nil {
				t.Fatal("validation details missing")
			}
			reason, ok := (*problem.Details)[test.field].(string)
			if !ok || reason == "" || !strings.Contains(reason, test.reason) {
				t.Fatalf("details[%s] = %v, expected reason containing %q", test.field, (*problem.Details)[test.field], test.reason)
			}
		})
	}
}

func TestMultipleViolations(t *testing.T) {
	router, err := New(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/products", strings.NewReader(`{"name":"","price":0,"stock":-1,"category":"","status":"UNKNOWN"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	problem := assertAPIError(t, response, 400, "VALIDATION_ERROR", "Validation failed")
	for _, field := range []string{"name", "price", "stock", "category", "status"} {
		if problem.Details == nil || (*problem.Details)[field] == nil {
			t.Errorf("no details for %s", field)
		}
	}
}

func TestDecodeExactlyOneObject(t *testing.T) {
	for _, suffix := range []string{"", " \n\t", "{}", " text", " null"} {
		var product generated.ProductCreate
		err := decodeJSON(bytes.NewBufferString(validProduct+suffix), &product)
		valid := strings.TrimSpace(suffix) == ""
		if (err == nil) != valid {
			t.Errorf("suffix %q: error=%v", suffix, err)
		}
	}
}

func TestErrorResponses(t *testing.T) {
	response := httptest.NewRecorder()
	writeError(response, repository.ErrNotFound)
	assertAPIError(t, response, 404, "PRODUCT_NOT_FOUND", "Product not found")
	response = httptest.NewRecorder()
	writeError(response, errors.New("postgres connection failed at C:/private/database"))
	assertAPIError(t, response, 500, "INTERNAL_ERROR", "Internal server error")
	if strings.Contains(response.Body.String(), "private") || strings.Contains(response.Body.String(), "postgres") {
		t.Fatal("internal error leaked")
	}
}

func assertAPIError(t *testing.T, response *httptest.ResponseRecorder, status int, code, message string) generated.ErrorResponse {
	t.Helper()
	if response.Code != status || response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status=%d content-type=%s body=%s", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
	var problem generated.ErrorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if string(problem.ErrorCode) != code || problem.Message != message {
		t.Fatalf("unexpected error response: %+v", problem)
	}
	return problem
}

func TestRouteErrors(t *testing.T) {
	router, err := New(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		method, path, code, message string
		status                      int
	}{
		{"GET", "/unknown", "ROUTE_NOT_FOUND", "Route not found", 404},
		{"GET", "/products/a/b", "ROUTE_NOT_FOUND", "Route not found", 404},
		{"PATCH", "/products", "METHOD_NOT_ALLOWED", "Method not allowed", 405},
		{"DELETE", "/orders", "METHOD_NOT_ALLOWED", "Method not allowed", 405},
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
		assertAPIError(t, response, test.status, test.code, test.message)
		if test.status == 405 && response.Header().Get("Allow") == "" {
			t.Fatal("Allow header missing")
		}
	}
}

func TestOrderValidation(t *testing.T) {
	router, err := New(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	id := "00000000-0000-0000-0000-000000000001"
	for _, body := range []string{
		`{"items":[]}`, `{"items":[{"product_id":"` + id + `","quantity":0}]}`,
		`{"items":[{"product_id":"` + id + `","quantity":1000}]}`,
		`{"items":[{"product_id":"bad","quantity":1}]}`,
		`{"items":[{"product_id":"` + id + `","quantity":1}],"promo_code":"bad"}`,
		`{"items":[{"product_id":"` + id + `","quantity":1}],"user_id":"` + id + `"}`,
		`{"items":[` + strings.TrimSuffix(strings.Repeat(`{"product_id":"`+id+`","quantity":1},`, 51), ",") + `]}`,
	} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest("POST", "/orders", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(response, request)
		problem := assertAPIError(t, response, 400, "VALIDATION_ERROR", "Validation failed")
		if problem.Details == nil || len(*problem.Details) == 0 {
			t.Fatal("details missing")
		}
	}
}
