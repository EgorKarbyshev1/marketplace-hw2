package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"io"
	"marketplace/internal/generated"
	"marketplace/internal/security"
	"net/http"
	"strings"
	"testing"
	"time"
)

func authCall(t *testing.T, f *orderFixture, method, path string, input any, token string, status int) []byte {
	t.Helper()
	var body []byte
	var err error
	if input != nil {
		body, err = json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, f.server.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != status {
		t.Fatalf("%s %s: %d expected %d: %s", method, path, response.StatusCode, status, data)
	}
	if _, err := uuid.Parse(response.Header.Get("X-Request-Id")); err != nil {
		t.Fatal("missing request id")
	}
	return data
}
func authCode(t *testing.T, data []byte, code string) {
	t.Helper()
	var problem generated.ErrorResponse
	if err := json.Unmarshal(data, &problem); err != nil {
		t.Fatal(err)
	}
	if string(problem.ErrorCode) != code || problem.Message == "" {
		t.Fatalf("error: %+v", problem)
	}
}
func authUser(t *testing.T, f *orderFixture, role generated.Role) (generated.UserResponse, string) {
	t.Helper()
	input := generated.Credentials{Email: uuid.NewString() + "@roles.test", Password: "Local-test-pass-2026"}
	user, err := f.accounts.CreateUser(context.Background(), input, role)
	if err != nil {
		t.Fatal(err)
	}
	data := authCall(t, f, "POST", "/auth/login", input, "", 200)
	var tokens generated.TokenPair
	if err := json.Unmarshal(data, &tokens); err != nil {
		t.Fatal(err)
	}
	return user, tokens.AccessToken
}
func TestAuthRegistrationLoginRefresh(t *testing.T) {
	f := newOrderFixture(t, 0)
	credentials := generated.Credentials{Email: uuid.NewString() + "@auth.test", Password: "never-log-this-password"}
	data := authCall(t, f, "POST", "/auth/register", credentials, "", 201)
	var user generated.UserResponse
	if err := json.Unmarshal(data, &user); err != nil {
		t.Fatal(err)
	}
	if user.Role != "USER" || user.Id == uuid.Nil {
		t.Fatal("registration role or id")
	}
	stored, err := f.accounts.Users.ByID(context.Background(), user.Id)
	if err != nil {
		t.Fatal(err)
	}
	if stored.PasswordHash == credentials.Password || bcrypt.CompareHashAndPassword([]byte(stored.PasswordHash), []byte(credentials.Password)) != nil {
		t.Fatal("password not hashed")
	}
	authCode(t, authCall(t, f, "POST", "/auth/register", credentials, "", 400), "VALIDATION_ERROR")
	authCode(t, authCall(t, f, "POST", "/auth/register", map[string]any{"email": "admin@auth.test", "password": "password123", "role": "ADMIN"}, "", 400), "VALIDATION_ERROR")
	bad := credentials
	bad.Password = "wrong-password"
	authCode(t, authCall(t, f, "POST", "/auth/login", bad, "", 401), "TOKEN_INVALID")
	data = authCall(t, f, "POST", "/auth/login", credentials, "", 200)
	var pair generated.TokenPair
	if err := json.Unmarshal(data, &pair); err != nil {
		t.Fatal(err)
	}
	if pair.ExpiresIn != 1200 || pair.TokenType != "Bearer" {
		t.Fatal("wrong token lifetime")
	}
	authCall(t, f, "GET", "/products", nil, pair.AccessToken, 200)
	authCode(t, authCall(t, f, "GET", "/products", nil, "", 401), "TOKEN_INVALID")
	authCode(t, authCall(t, f, "GET", "/products", nil, pair.RefreshToken, 401), "TOKEN_INVALID")
	authCode(t, authCall(t, f, "GET", "/products", nil, pair.AccessToken+"bad", 401), "TOKEN_INVALID")
	expired, _, _ := f.tokens.Sign(security.Principal{ID: user.Id, Role: "USER"}, "access", -time.Minute)
	authCode(t, authCall(t, f, "GET", "/products", nil, expired, 401), "TOKEN_EXPIRED")
	data = authCall(t, f, "POST", "/auth/refresh", generated.RefreshRequest{RefreshToken: pair.RefreshToken}, "", 200)
	var rotated generated.TokenPair
	if err := json.Unmarshal(data, &rotated); err != nil {
		t.Fatal(err)
	}
	if rotated.RefreshToken == pair.RefreshToken {
		t.Fatal("refresh did not rotate")
	}
	authCall(t, f, "GET", "/products", nil, rotated.AccessToken, 200)
	authCode(t, authCall(t, f, "POST", "/auth/refresh", generated.RefreshRequest{RefreshToken: pair.RefreshToken}, "", 401), "REFRESH_TOKEN_INVALID")
	authCode(t, authCall(t, f, "POST", "/auth/refresh", generated.RefreshRequest{RefreshToken: "invalid"}, "", 401), "REFRESH_TOKEN_INVALID")
	authCode(t, authCall(t, f, "POST", "/auth/refresh", generated.RefreshRequest{RefreshToken: pair.AccessToken}, "", 401), "REFRESH_TOKEN_INVALID")
	logText := f.logs.String()
	for _, secret := range []string{credentials.Password, pair.AccessToken, pair.RefreshToken, rotated.AccessToken, rotated.RefreshToken} {
		if strings.Contains(logText, secret) {
			t.Fatal("secret leaked into request logs")
		}
	}
	foundUser, foundUnauthorized := false, false
	for _, line := range strings.Split(strings.TrimSpace(logText), "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event["user_id"] == user.Id.String() {
			foundUser = true
		}
		if event["status_code"] == float64(401) {
			foundUnauthorized = true
		}
	}
	if !foundUser || !foundUnauthorized {
		t.Fatal("authorized user or failed request missing from logs")
	}
}
func TestRoleMatrixAndSellerOwnership(t *testing.T) {
	f := newOrderFixture(t, 0)
	user, userToken := authUser(t, f, "USER")
	_, otherUserToken := authUser(t, f, "USER")
	seller, sellerToken := authUser(t, f, "SELLER")
	_, otherSellerToken := authUser(t, f, "SELLER")
	_, adminToken := authUser(t, f, "ADMIN")
	input := generated.ProductCreate{Name: "Role product", Category: "roles", Price: "100.00", Stock: 10, Status: "ACTIVE"}
	authCode(t, authCall(t, f, "POST", "/products", input, userToken, 403), "ACCESS_DENIED")
	var product generated.ProductResponse
	if err := json.Unmarshal(authCall(t, f, "POST", "/products", input, sellerToken, 201), &product); err != nil {
		t.Fatal(err)
	}
	if product.SellerId == nil || *product.SellerId != seller.Id {
		t.Fatal("wrong seller id")
	}
	update := generated.ProductUpdate(input)
	path := "/products/" + product.Id.String()
	authCall(t, f, "PUT", path, update, sellerToken, 200)
	authCode(t, authCall(t, f, "PUT", path, update, otherSellerToken, 403), "ACCESS_DENIED")
	authCode(t, authCall(t, f, "DELETE", path, nil, otherSellerToken, 403), "ACCESS_DENIED")
	authCall(t, f, "PUT", path, update, adminToken, 200)
	for _, token := range []string{userToken, sellerToken, adminToken} {
		authCall(t, f, "GET", path, nil, token, 200)
	}
	inputOrder := orderInput(product.Id, 2, nil)
	authCode(t, authCall(t, f, "POST", "/orders", inputOrder, sellerToken, 403), "ACCESS_DENIED")
	var order generated.OrderResponse
	if err := json.Unmarshal(authCall(t, f, "POST", "/orders", inputOrder, userToken, 201), &order); err != nil {
		t.Fatal(err)
	}
	if order.UserId != user.Id {
		t.Fatal("order owner not taken from JWT")
	}
	orderPath := "/orders/" + order.Id.String()
	authCode(t, authCall(t, f, "GET", orderPath, nil, otherUserToken, 403), "ORDER_OWNERSHIP_VIOLATION")
	authCode(t, authCall(t, f, "PUT", orderPath, generated.OrderUpdate{Items: inputOrder.Items}, otherUserToken, 403), "ORDER_OWNERSHIP_VIOLATION")
	authCode(t, authCall(t, f, "POST", orderPath+"/cancel", nil, otherUserToken, 403), "ORDER_OWNERSHIP_VIOLATION")
	authCall(t, f, "GET", orderPath, nil, adminToken, 200)
	authCall(t, f, "PUT", orderPath, generated.OrderUpdate{Items: inputOrder.Items}, adminToken, 200)
	authCall(t, f, "POST", orderPath+"/cancel", nil, adminToken, 200)
	authCall(t, f, "POST", "/orders", inputOrder, adminToken, 201)
	promo := generated.PromoCodeCreate{Code: strings.ToUpper(uuid.NewString()[:8]), DiscountType: "FIXED_AMOUNT", DiscountValue: "10", MinOrderAmount: "0", MaxUses: 5, ValidFrom: time.Now().Add(-time.Hour), ValidUntil: time.Now().Add(time.Hour), Active: true}
	authCode(t, authCall(t, f, "POST", "/promo-codes", promo, userToken, 403), "ACCESS_DENIED")
	authCall(t, f, "POST", "/promo-codes", promo, sellerToken, 201)
	promo.Code = strings.ToUpper(uuid.NewString()[:8])
	authCall(t, f, "POST", "/promo-codes", promo, adminToken, 201)
	legacyID := uuid.NewString()
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO products(id,name,price,stock,category,status) VALUES($1::text::uuid,'Legacy',1,1,'roles','ACTIVE')`, legacyID); err != nil {
		t.Fatal(err)
	}
	authCode(t, authCall(t, f, "PUT", "/products/"+legacyID, update, sellerToken, 403), "ACCESS_DENIED")
	authCall(t, f, "PUT", "/products/"+legacyID, update, adminToken, 200)
	authCall(t, f, "DELETE", "/products/"+legacyID, nil, adminToken, 204)
	authCall(t, f, "DELETE", path, nil, sellerToken, 204)
}
