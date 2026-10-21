package security

import (
	"errors"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"strings"
	"testing"
	"time"
)

func TestTokenTypesAndSignatures(t *testing.T) {
	manager, err := NewTokens(strings.Repeat("test", 10), 20*time.Minute, 14*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	user := Principal{ID: uuid.New(), Role: "USER"}
	access, _, _ := manager.Sign(user, "access", 20*time.Minute)
	if claims, err := manager.Parse(access, "access"); err != nil || claims.UserID != user.ID.String() {
		t.Fatalf("access: %+v %v", claims, err)
	}
	refresh, _, _ := manager.Sign(user, "refresh", 14*24*time.Hour)
	if _, err := manager.Parse(refresh, "access"); err == nil {
		t.Fatal("refresh accepted as access")
	}
	expired, _, _ := manager.Sign(user, "access", -time.Minute)
	if _, err := manager.Parse(expired, "access"); !errors.Is(err, jwt.ErrTokenExpired) {
		t.Fatalf("expired: %v", err)
	}
	if _, err := manager.Parse(access+"broken", "access"); err == nil {
		t.Fatal("invalid signature accepted")
	}
	none, _ := jwt.NewWithClaims(jwt.SigningMethodNone, Claims{UserID: user.ID.String(), TokenType: "access"}).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if _, err := manager.Parse(none, "access"); err == nil {
		t.Fatal("unsigned token accepted")
	}
}
