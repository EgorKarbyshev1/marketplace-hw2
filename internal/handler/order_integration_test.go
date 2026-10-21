package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"log/slog"
	"marketplace/internal/database"
	"marketplace/internal/generated"
	"marketplace/internal/repository"
	"marketplace/internal/security"
	"marketplace/internal/service"
)

type orderFixture struct {
	t        *testing.T
	pool     *pgxpool.Pool
	server   *httptest.Server
	user     uuid.UUID
	seller   uuid.UUID
	accounts *service.Auth
	tokens   *security.Tokens
	logs     *bytes.Buffer
}

func newOrderFixture(t *testing.T, interval time.Duration) *orderFixture {
	t.Helper()
	connection := os.Getenv("TEST_DATABASE_URL")
	if connection == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	parsed, err := url.Parse(connection)
	if err != nil || !strings.HasSuffix(parsed.Path, "_test") {
		t.Fatal("separate _test database required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, connection, "../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	tokens, err := security.NewTokens(strings.Repeat("test-secret-", 4), 20*time.Minute, 14*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	accounts := service.NewAuth(repository.NewUsers(pool), tokens)
	handler, err := New(service.New(repository.New(pool)), service.NewOrders(repository.NewOrders(pool), interval), accounts)
	if err != nil {
		t.Fatal(err)
	}
	logs := &bytes.Buffer{}
	server := httptest.NewServer(Audit(Secure(handler, accounts), slog.New(slog.NewJSONHandler(logs, nil))))
	t.Cleanup(server.Close)
	return &orderFixture{t: t, pool: pool, server: server, user: uuid.New(), seller: uuid.New(), accounts: accounts, tokens: tokens, logs: logs}
}

func (f *orderFixture) raw(method, path string, input any, user uuid.UUID) (int, []byte, error) {
	var body []byte
	var err error
	if input != nil {
		body, err = json.Marshal(input)
		if err != nil {
			return 0, nil, err
		}
	}
	request, err := http.NewRequest(method, f.server.URL+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	role := generated.Role("USER")
	if path == "/promo-codes" || (strings.HasPrefix(path, "/products") && method != "GET") {
		user = f.seller
		role = "SELLER"
	}
	if err := f.ensureUser(user, role); err != nil {
		return 0, nil, err
	}
	token, _, err := f.tokens.Sign(security.Principal{ID: user, Role: role}, "access", 20*time.Minute)
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := f.server.Client().Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if response.Header.Get("Content-Type") != "application/json" {
		return response.StatusCode, data, fmt.Errorf("unexpected content-type %s", response.Header.Get("Content-Type"))
	}
	return response.StatusCode, data, err
}
func (f *orderFixture) call(method, path string, input any, status int, output any) {
	f.t.Helper()
	actual, data, err := f.raw(method, path, input, f.user)
	if err != nil {
		f.t.Fatal(err)
	}
	if actual != status {
		f.t.Fatalf("%s %s: status=%d expected=%d body=%s", method, path, actual, status, data)
	}
	if output != nil {
		if err := json.Unmarshal(data, output); err != nil {
			f.t.Fatal(err)
		}
	}
}
func (f *orderFixture) failure(method, path string, input any, status int, code string) {
	f.t.Helper()
	var problem generated.ErrorResponse
	f.call(method, path, input, status, &problem)
	if string(problem.ErrorCode) != code || problem.Message == "" {
		f.t.Fatalf("unexpected error: %+v", problem)
	}
	if code == "INSUFFICIENT_STOCK" && (problem.Details == nil || (*problem.Details)["products"] == nil) {
		f.t.Fatal("stock details missing")
	}
}
func (f *orderFixture) product(price string, stock int32, status generated.ProductStatus) uuid.UUID {
	f.t.Helper()
	var result generated.ProductResponse
	f.call("POST", "/products", generated.ProductCreate{Name: "Order test", Category: "order-test", Price: json.Number(price), Stock: stock, Status: status}, 201, &result)
	return result.Id
}
func (f *orderFixture) stock(id uuid.UUID) int32 {
	f.t.Helper()
	var stock int32
	if err := f.pool.QueryRow(context.Background(), `SELECT stock FROM products WHERE id=$1::text::uuid`, id.String()).Scan(&stock); err != nil {
		f.t.Fatal(err)
	}
	return stock
}
func (f *orderFixture) create(id uuid.UUID, quantity int32, promo *string) generated.OrderResponse {
	f.t.Helper()
	var result generated.OrderResponse
	f.call("POST", "/orders", orderInput(id, quantity, promo), 201, &result)
	return result
}
func orderInput(id uuid.UUID, quantity int32, promo *string) generated.OrderCreate {
	return generated.OrderCreate{Items: []generated.OrderItemRequest{{ProductId: id, Quantity: quantity}}, PromoCode: promo}
}
func (f *orderFixture) promo(kind generated.DiscountType, value, minimum string, uses int32, expired bool) generated.PromoCodeResponse {
	f.t.Helper()
	var result generated.PromoCodeResponse
	end := time.Now().Add(time.Hour)
	if expired {
		end = time.Now().Add(-time.Hour)
	}
	f.call("POST", "/promo-codes", generated.PromoCodeCreate{Code: strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", ""))[:16],
		DiscountType: kind, DiscountValue: json.Number(value), MinOrderAmount: json.Number(minimum), MaxUses: uses,
		ValidFrom: time.Now().Add(-2 * time.Hour), ValidUntil: end, Active: true}, 201, &result)
	return result
}
func (f *orderFixture) uses(id uuid.UUID) int32 {
	f.t.Helper()
	var uses int32
	if err := f.pool.QueryRow(context.Background(), `SELECT current_uses FROM promo_codes WHERE id=$1::text::uuid`, id.String()).Scan(&uses); err != nil {
		f.t.Fatal(err)
	}
	return uses
}
func (f *orderFixture) get(id uuid.UUID) generated.OrderResponse {
	f.t.Helper()
	var order generated.OrderResponse
	f.call("GET", "/orders/"+id.String(), nil, 200, &order)
	return order
}

func TestOrderCreateAndSnapshot(t *testing.T) {
	f := newOrderFixture(t, 0)
	product := f.product("100.25", 10, "ACTIVE")
	order := f.create(product, 2, nil)
	if order.Status != "CREATED" || order.TotalAmount != "200.50" || order.Items[0].PriceAtOrder != "100.25" || f.stock(product) != 8 {
		t.Fatalf("unexpected order %+v", order)
	}
	f.call("PUT", "/products/"+product.String(), generated.ProductUpdate{Name: "New price", Category: "order-test", Price: json.Number("300"), Stock: 8, Status: "ACTIVE"}, 200, nil)
	fetched := f.get(order.Id)
	if fetched.TotalAmount != "200.50" || fetched.Items[0].PriceAtOrder != "100.25" {
		t.Fatal("snapshot changed")
	}
}

func TestOrderCatalogErrors(t *testing.T) {
	f := newOrderFixture(t, 0)
	f.failure("POST", "/orders", orderInput(uuid.New(), 1, nil), 404, "PRODUCT_NOT_FOUND")
	inactive := f.product("10", 3, "INACTIVE")
	f.failure("POST", "/orders", orderInput(inactive, 1, nil), 409, "PRODUCT_INACTIVE")
	product := f.product("10", 1, "ACTIVE")
	f.failure("POST", "/orders", orderInput(product, 2, nil), 409, "INSUFFICIENT_STOCK")
	if f.stock(product) != 1 {
		t.Fatal("failed order changed stock")
	}
	f.create(product, 1, nil)
	f.failure("POST", "/orders", orderInput(product, 1, nil), 409, "ORDER_HAS_ACTIVE")
}

func TestOrderRateLimits(t *testing.T) {
	f := newOrderFixture(t, time.Minute)
	product := f.product("10", 10, "ACTIVE")
	order := f.create(product, 1, nil)
	f.failure("POST", "/orders", orderInput(product, 1, nil), 429, "ORDER_LIMIT_EXCEEDED")
	f.call("PUT", "/orders/"+order.Id.String(), generated.OrderUpdate{Items: orderInput(product, 2, nil).Items}, 200, nil)
	f.failure("PUT", "/orders/"+order.Id.String(), generated.OrderUpdate{Items: orderInput(product, 3, nil).Items}, 429, "ORDER_LIMIT_EXCEEDED")
	if f.stock(product) != 8 {
		t.Fatal("rate limited update changed stock")
	}
}

func TestOrderPromoDiscounts(t *testing.T) {
	for _, test := range []struct {
		name                   string
		kind                   generated.DiscountType
		value, discount, total string
	}{
		{"percentage", "PERCENTAGE", "20", "40.00", "160.00"},
		{"fixed", "FIXED_AMOUNT", "30", "30.00", "170.00"},
		{"fixed larger than total", "FIXED_AMOUNT", "300", "200.00", "0.00"},
		{"over seventy pays full price", "PERCENTAGE", "80", "0.00", "200.00"},
		{"exactly seventy", "PERCENTAGE", "70", "140.00", "60.00"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newOrderFixture(t, 0)
			product := f.product("100", 10, "ACTIVE")
			promo := f.promo(test.kind, test.value, "0", 1, false)
			order := f.create(product, 2, &promo.Code)
			if order.DiscountAmount != json.Number(test.discount) || order.TotalAmount != json.Number(test.total) || f.uses(promo.Id) != 1 {
				t.Fatalf("unexpected discount %+v", order)
			}
			f.call("PUT", "/orders/"+order.Id.String(), generated.OrderUpdate{Items: orderInput(product, 2, nil).Items}, 200, nil)
			if f.uses(promo.Id) != 1 {
				t.Fatal("own use was counted twice")
			}
			f.call("POST", "/orders/"+order.Id.String()+"/cancel", nil, 200, nil)
			if f.uses(promo.Id) != 0 || f.stock(product) != 10 {
				t.Fatal("cancel did not restore promo/stock")
			}
		})
	}
}

func TestOrderPromoErrorsRollback(t *testing.T) {
	f := newOrderFixture(t, 0)
	product := f.product("100", 10, "ACTIVE")
	missing := "MISSING_PROMO"
	f.failure("POST", "/orders", orderInput(product, 1, &missing), 422, "PROMO_CODE_INVALID")
	expired := f.promo("PERCENTAGE", "10", "0", 1, true)
	f.failure("POST", "/orders", orderInput(product, 1, &expired.Code), 422, "PROMO_CODE_INVALID")
	minimum := f.promo("FIXED_AMOUNT", "10", "150", 1, false)
	f.failure("POST", "/orders", orderInput(product, 1, &minimum.Code), 422, "PROMO_CODE_MIN_AMOUNT")
	if f.stock(product) != 10 || f.uses(expired.Id) != 0 || f.uses(minimum.Id) != 0 {
		t.Fatal("promo failure was not rolled back")
	}
	var operations int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM user_operations WHERE user_id=$1::text::uuid`, f.user.String()).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if operations != 0 {
		t.Fatal("failed operation recorded")
	}
	promo := f.promo("PERCENTAGE", "10", "0", 1, false)
	f.create(product, 1, &promo.Code)
	f.user = uuid.New()
	f.failure("POST", "/orders", orderInput(product, 1, &promo.Code), 422, "PROMO_CODE_INVALID")
	if f.stock(product) != 9 || f.uses(promo.Id) != 1 {
		t.Fatal("exhausted promo changed data")
	}
}

func TestOrderUpdateAndRollback(t *testing.T) {
	f := newOrderFixture(t, 0)
	first := f.product("100", 10, "ACTIVE")
	second := f.product("200", 10, "ACTIVE")
	order := f.create(first, 2, nil)
	f.failure("PUT", "/orders/"+order.Id.String(), generated.OrderUpdate{Items: orderInput(second, 11, nil).Items}, 409, "INSUFFICIENT_STOCK")
	failed := f.get(order.Id)
	if f.stock(first) != 8 || f.stock(second) != 10 || failed.TotalAmount != order.TotalAmount || failed.Items[0].Id != order.Items[0].Id || !failed.UpdatedAt.Equal(order.UpdatedAt) {
		t.Fatal("failed update did not roll back")
	}
	var updated generated.OrderResponse
	f.call("PUT", "/orders/"+order.Id.String(), generated.OrderUpdate{Items: orderInput(second, 3, nil).Items}, 200, &updated)
	if f.stock(first) != 10 || f.stock(second) != 7 || updated.TotalAmount != "600.00" {
		t.Fatal("old reservation not restored")
	}
}

func TestOrderUpdateRemovesPromoBelowMinimum(t *testing.T) {
	f := newOrderFixture(t, 0)
	product := f.product("100", 10, "ACTIVE")
	promo := f.promo("PERCENTAGE", "20", "150", 1, false)
	order := f.create(product, 2, &promo.Code)
	var updated generated.OrderResponse
	f.call("PUT", "/orders/"+order.Id.String(), generated.OrderUpdate{Items: orderInput(product, 1, nil).Items}, 200, &updated)
	if updated.PromoCodeId != nil || updated.DiscountAmount != "0.00" || updated.TotalAmount != "100.00" || f.uses(promo.Id) != 0 || f.stock(product) != 9 {
		t.Fatal("promo was not removed")
	}
}

func TestOrderExpiredPromoUpdateRollback(t *testing.T) {
	f := newOrderFixture(t, 0)
	product := f.product("100", 10, "ACTIVE")
	promo := f.promo("PERCENTAGE", "20", "0", 1, false)
	order := f.create(product, 2, &promo.Code)
	if _, err := f.pool.Exec(context.Background(), `UPDATE promo_codes SET valid_until=clock_timestamp()-interval '1 minute' WHERE id=$1::text::uuid`, promo.Id.String()); err != nil {
		t.Fatal(err)
	}
	f.failure("PUT", "/orders/"+order.Id.String(), generated.OrderUpdate{Items: orderInput(product, 3, nil).Items}, 422, "PROMO_CODE_INVALID")
	if f.stock(product) != 8 || f.uses(promo.Id) != 1 || f.get(order.Id).TotalAmount != order.TotalAmount {
		t.Fatal("expired promo update did not roll back")
	}
}

func TestOrderCancelOwnershipAndStates(t *testing.T) {
	f := newOrderFixture(t, 0)
	product := f.product("100", 10, "ACTIVE")
	order := f.create(product, 2, nil)
	owner := f.user
	f.user = uuid.New()
	f.failure("GET", "/orders/"+order.Id.String(), nil, 403, "ORDER_OWNERSHIP_VIOLATION")
	f.failure("PUT", "/orders/"+order.Id.String(), generated.OrderUpdate{Items: orderInput(product, 1, nil).Items}, 403, "ORDER_OWNERSHIP_VIOLATION")
	f.failure("POST", "/orders/"+order.Id.String()+"/cancel", nil, 403, "ORDER_OWNERSHIP_VIOLATION")
	f.user = owner
	f.failure("POST", "/orders/"+order.Id.String()+"/status", generated.OrderStatusUpdate{Status: "SHIPPED"}, 409, "INVALID_STATE_TRANSITION")
	f.call("POST", "/orders/"+order.Id.String()+"/status", generated.OrderStatusUpdate{Status: "PAYMENT_PENDING"}, 200, nil)
	f.failure("PUT", "/orders/"+order.Id.String(), generated.OrderUpdate{Items: orderInput(product, 1, nil).Items}, 409, "INVALID_STATE_TRANSITION")
	f.call("POST", "/orders/"+order.Id.String()+"/cancel", nil, 200, nil)
	f.failure("POST", "/orders/"+order.Id.String()+"/cancel", nil, 409, "INVALID_STATE_TRANSITION")
	if f.stock(product) != 10 {
		t.Fatal("double stock return")
	}
	f.failure("GET", "/orders/"+uuid.NewString(), nil, 404, "ORDER_NOT_FOUND")
	order = f.create(product, 1, nil)
	for _, status := range []generated.OrderStatus{"PAYMENT_PENDING", "PAID", "SHIPPED", "COMPLETED"} {
		f.call("POST", "/orders/"+order.Id.String()+"/status", generated.OrderStatusUpdate{Status: status}, 200, nil)
	}
	f.failure("POST", "/orders/"+order.Id.String()+"/cancel", nil, 409, "INVALID_STATE_TRANSITION")
}

type concurrentResult struct {
	status int
	code   generated.ErrorCode
	err    error
}

func simultaneous(f *orderFixture, users []uuid.UUID, inputs []generated.OrderCreate) []concurrentResult {
	start := make(chan struct{})
	results := make(chan concurrentResult, len(users))
	for i, user := range users {
		go func(index int, user uuid.UUID) {
			<-start
			status, data, err := f.raw("POST", "/orders", inputs[index], user)
			var problem generated.ErrorResponse
			if status != 201 && err == nil {
				err = json.Unmarshal(data, &problem)
			}
			results <- concurrentResult{status: status, code: problem.ErrorCode, err: err}
		}(i, user)
	}
	close(start)
	all := make([]concurrentResult, 0, len(users))
	for range users {
		all = append(all, <-results)
	}
	return all
}
func assertRace(t *testing.T, results []concurrentResult, failureCode generated.ErrorCode) {
	t.Helper()
	successful, failed := 0, 0
	for _, result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.status == 201 {
			successful++
		} else if result.code == failureCode {
			failed++
		} else {
			t.Fatalf("unexpected race result %+v", result)
		}
	}
	if successful != 1 || failed != 1 {
		t.Fatalf("race outcomes %+v", results)
	}
}
func TestConcurrentOrdersStock(t *testing.T) {
	f := newOrderFixture(t, 0)
	product := f.product("100", 2, "ACTIVE")
	input := orderInput(product, 2, nil)
	assertRace(t, simultaneous(f, []uuid.UUID{uuid.New(), uuid.New()}, []generated.OrderCreate{input, input}), "INSUFFICIENT_STOCK")
	if f.stock(product) != 0 {
		t.Fatal("stock was double reserved")
	}
}
func TestConcurrentOrdersSameUser(t *testing.T) {
	for _, interval := range []time.Duration{0, time.Minute} {
		t.Run(interval.String(), func(t *testing.T) {
			f := newOrderFixture(t, interval)
			product := f.product("100", 10, "ACTIVE")
			input := orderInput(product, 1, nil)
			code := generated.ErrorCode("ORDER_HAS_ACTIVE")
			if interval > 0 {
				code = "ORDER_LIMIT_EXCEEDED"
			}
			assertRace(t, simultaneous(f, []uuid.UUID{f.user, f.user}, []generated.OrderCreate{input, input}), code)
			if f.stock(product) != 9 {
				t.Fatal("same user reserved twice")
			}
		})
	}
}
func TestConcurrentOrdersLastPromo(t *testing.T) {
	f := newOrderFixture(t, 0)
	first := f.product("100", 5, "ACTIVE")
	second := f.product("100", 5, "ACTIVE")
	promo := f.promo("FIXED_AMOUNT", "10", "0", 1, false)
	assertRace(t, simultaneous(f, []uuid.UUID{uuid.New(), uuid.New()}, []generated.OrderCreate{orderInput(first, 1, &promo.Code), orderInput(second, 1, &promo.Code)}), "PROMO_CODE_INVALID")
	if f.uses(promo.Id) != 1 || f.stock(first)+f.stock(second) != 9 {
		t.Fatal("promo limit exceeded or failed transaction reserved stock")
	}
}

func TestOrderInactiveAndFuturePromo(t *testing.T) {
	f := newOrderFixture(t, 0)
	product := f.product("10", 5, "ACTIVE")
	inactive := f.promo("FIXED_AMOUNT", "1", "0", 1, false)
	if _, err := f.pool.Exec(context.Background(), `UPDATE promo_codes SET active=false WHERE id=$1::text::uuid`, inactive.Id.String()); err != nil {
		t.Fatal(err)
	}
	f.failure("POST", "/orders", orderInput(product, 1, &inactive.Code), 422, "PROMO_CODE_INVALID")
	future := f.promo("FIXED_AMOUNT", "1", "0", 1, false)
	if _, err := f.pool.Exec(context.Background(), `UPDATE promo_codes SET valid_from=clock_timestamp()+interval '10 minutes' WHERE id=$1::text::uuid`, future.Id.String()); err != nil {
		t.Fatal(err)
	}
	f.failure("POST", "/orders", orderInput(product, 1, &future.Code), 422, "PROMO_CODE_INVALID")
	if f.stock(product) != 5 {
		t.Fatal("invalid promo reserved stock")
	}
}

func TestOrderDuplicateItemsAndAmountOverflow(t *testing.T) {
	f := newOrderFixture(t, 0)
	product := f.product("9999999999.99", 5, "ACTIVE")
	f.failure("POST", "/orders", orderInput(product, 2, nil), 400, "VALIDATION_ERROR")
	input := orderInput(product, 1, nil)
	input.Items = append(input.Items, input.Items[0])
	f.failure("POST", "/orders", input, 400, "VALIDATION_ERROR")
	if f.stock(product) != 5 {
		t.Fatal("invalid amounts reserved stock")
	}
}

func TestOrderLargeSubtotalWithDiscount(t *testing.T) {
	f := newOrderFixture(t, 0)
	product := f.product("6000000000", 2, "ACTIVE")
	promo := f.promo("PERCENTAGE", "50", "0", 1, false)
	order := f.create(product, 2, &promo.Code)
	if order.TotalAmount != "6000000000.00" || order.DiscountAmount != "6000000000.00" {
		t.Fatalf("large money calculation: %+v", order)
	}
}

func (f *orderFixture) ensureUser(id uuid.UUID, role generated.Role) error {
	_, err := f.pool.Exec(context.Background(), `INSERT INTO users(id,email,password_hash,role) VALUES($1::text::uuid,$2,'unused-test-login',$3) ON CONFLICT(id) DO NOTHING`, id.String(), id.String()+"@fixture.test", string(role))
	return err
}
