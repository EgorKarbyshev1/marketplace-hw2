package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"marketplace/internal/database"
	"marketplace/internal/generated"
	"marketplace/internal/security"
)

func TestProductsPostgres(t *testing.T) {
	f := newOrderFixture(t, 0)
	ctx := context.Background()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	pool, server := f.pool, f.server
	if err := f.ensureUser(f.seller, "SELLER"); err != nil {
		t.Fatal(err)
	}
	token, _, err := f.tokens.Sign(security.Principal{ID: f.seller, Role: "SELLER"}, "access", 20*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body string, status int, output any) http.Header {
		t.Helper()
		request, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		request.Header.Set("Authorization", "Bearer "+token)
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != status {
			t.Fatalf("%s %s: status = %d, expected %d, body = %s", method, path, response.StatusCode, status, data)
		}
		if status >= 400 {
			var problem generated.ErrorResponse
			if err := json.Unmarshal(data, &problem); err != nil {
				t.Fatal(err)
			}
			expectedCode, expectedMessage := "PRODUCT_NOT_FOUND", "Product not found"
			if status == http.StatusBadRequest {
				expectedCode, expectedMessage = "VALIDATION_ERROR", "Validation failed"
				if problem.Details == nil || len(*problem.Details) == 0 {
					t.Fatal("validation details missing")
				}
			}
			if response.Header.Get("Content-Type") != "application/json" || string(problem.ErrorCode) != expectedCode || problem.Message != expectedMessage {
				t.Fatalf("unexpected error: %s", data)
			}
		}
		if output != nil {
			if err := json.Unmarshal(data, output); err != nil {
				t.Fatal(err)
			}
		}
		return response.Header
	}
	category := "test-" + uuid.NewString()
	body := fmt.Sprintf(`{"name":"Keyboard","description":"USB","price":9999999999.99,"stock":10,"category":%q,"status":"ACTIVE"}`, category)
	var created generated.ProductResponse
	header := call("POST", "/products", body+" \n\t", http.StatusCreated, &created)
	if created.Id == uuid.Nil || created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() || created.Price.String() != "9999999999.99" {
		t.Fatalf("unexpected created product: %+v", created)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(cleanup, `DELETE FROM products WHERE id = $1::text::uuid`, created.Id.String()); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	path := "/products/" + created.Id.String()
	if header.Get("Location") != path {
		t.Fatalf("Location = %s", header.Get("Location"))
	}
	var fetched generated.ProductResponse
	call("GET", path, "", http.StatusOK, &fetched)
	if fetched.Name != created.Name || fetched.Price != created.Price {
		t.Fatalf("unexpected fetched product: %+v", fetched)
	}
	var countBefore int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM products").Scan(&countBefore); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{body + `{}`, body + ` extra`, `{`, strings.Replace(body, "9999999999.99", "0", 1), strings.Replace(body, "9999999999.99", "1.001", 1)} {
		call("POST", "/products", invalid, http.StatusBadRequest, nil)
		call("PUT", path, invalid, http.StatusBadRequest, nil)
	}
	call("GET", path, "", http.StatusOK, &fetched)
	if fetched.Name != created.Name || fetched.Price != created.Price || fetched.Stock != created.Stock || !fetched.UpdatedAt.Equal(created.UpdatedAt) {
		t.Fatal("invalid request changed product")
	}
	var countAfter int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM products").Scan(&countAfter); err != nil {
		t.Fatal(err)
	}
	if countAfter != countBefore {
		t.Fatal("invalid request inserted products")
	}
	var page generated.ProductPage
	call("GET", "/products?status=ACTIVE&category="+category, "", http.StatusOK, &page)
	if len(page.Items) != 1 || page.TotalElements != 1 || page.Page != 0 || page.Size != 20 {
		t.Fatalf("unexpected page: %+v", page)
	}
	call("GET", "/products?page=1&size=1&category="+category, "", http.StatusOK, &page)
	if len(page.Items) != 0 || page.TotalElements != 1 {
		t.Fatalf("unexpected empty page: %+v", page)
	}
	call("GET", "/products?category="+category+"-other", "", http.StatusOK, &page)
	if len(page.Items) != 0 || page.TotalElements != 0 {
		t.Fatalf("category filter did not match exactly: %+v", page)
	}
	update := fmt.Sprintf(`{"name":"Mouse","price":1799.90,"stock":0,"category":%q,"status":"INACTIVE"}`, category)
	var updated generated.ProductResponse
	call("PUT", path, update, http.StatusOK, &updated)
	if updated.Description != nil || updated.Name != "Mouse" || updated.Price.String() != "1799.90" || updated.Stock != 0 ||
		!updated.CreatedAt.Equal(created.CreatedAt) || !updated.UpdatedAt.After(created.UpdatedAt) {
		t.Fatalf("unexpected updated product: %+v", updated)
	}
	call("GET", "/products?status=ACTIVE&category="+category, "", http.StatusOK, &page)
	if page.TotalElements != 0 {
		t.Fatal("status filter returned inactive product")
	}
	call("DELETE", path, "", http.StatusNoContent, nil)
	call("DELETE", path, "", http.StatusNoContent, nil)
	call("GET", path, "", http.StatusOK, &fetched)
	if fetched.Status != generated.ARCHIVED {
		t.Fatal("product was not archived")
	}
	var status, price string
	var stock int
	if err := pool.QueryRow(ctx, `SELECT status, price::text, stock FROM products WHERE id = $1::text::uuid`, created.Id.String()).Scan(&status, &price, &stock); err != nil {
		t.Fatal(err)
	}
	if status != "ARCHIVED" || price != "1799.90" || stock != 0 {
		t.Fatalf("database row: status=%s price=%s stock=%d", status, price, stock)
	}
	missing := "/products/" + uuid.NewString()
	call("GET", missing, "", http.StatusNotFound, nil)
	call("PUT", missing, update, http.StatusNotFound, nil)
	call("DELETE", missing, "", http.StatusNotFound, nil)
	reopened, err := database.Open(ctx, databaseURL, "../../migrations")
	if err != nil {
		t.Fatalf("repeat migrations: %v", err)
	}
	reopened.Close()
}
