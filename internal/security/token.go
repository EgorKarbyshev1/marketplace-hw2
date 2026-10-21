package security

import (
	"context"
	"errors"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"marketplace/internal/generated"
	"time"
)

type Principal struct {
	ID   uuid.UUID
	Role generated.Role
}
type principalKey struct{}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}
func Current(ctx context.Context) Principal { p, _ := ctx.Value(principalKey{}).(Principal); return p }
func IsAdmin(ctx context.Context) bool      { return Current(ctx).Role == "ADMIN" }

type Claims struct {
	UserID    string         `json:"user_id"`
	Role      generated.Role `json:"role"`
	TokenType string         `json:"token_type"`
	jwt.RegisteredClaims
}
type Tokens struct {
	secret                []byte
	AccessTTL, RefreshTTL time.Duration
}

func NewTokens(secret string, access, refresh time.Duration) (*Tokens, error) {
	if len(secret) < 32 || access < 15*time.Minute || access > 30*time.Minute || refresh < 7*24*time.Hour || refresh > 30*24*time.Hour {
		return nil, errors.New("JWT secret must have 32 bytes; access TTL 15-30 minutes; refresh TTL 7-30 days")
	}
	return &Tokens{secret: []byte(secret), AccessTTL: access, RefreshTTL: refresh}, nil
}
func (t *Tokens) Sign(p Principal, kind string, ttl time.Duration) (string, uuid.UUID, error) {
	id := uuid.New()
	now := time.Now()
	claims := Claims{UserID: p.ID.String(), Role: p.Role, TokenType: kind, RegisteredClaims: jwt.RegisteredClaims{
		Subject: p.ID.String(), Issuer: "marketplace", ID: id.String(), IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(ttl))}}
	value, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(t.secret)
	return value, id, err
}
func (t *Tokens) Parse(value, kind string) (Claims, error) {
	var claims Claims
	token, err := jwt.ParseWithClaims(value, &claims, func(token *jwt.Token) (any, error) { return t.secret, nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired(), jwt.WithIssuer("marketplace"))
	if err != nil {
		return claims, err
	}
	if !token.Valid || claims.TokenType != kind || claims.Subject != claims.UserID {
		return claims, errors.New("invalid token type or subject")
	}
	if _, err := uuid.Parse(claims.UserID); err != nil {
		return claims, errors.New("invalid subject")
	}
	if _, err := uuid.Parse(claims.ID); err != nil {
		return claims, errors.New("invalid token id")
	}
	if claims.Role != "USER" && claims.Role != "SELLER" && claims.Role != "ADMIN" {
		return claims, errors.New("invalid role")
	}
	return claims, nil
}
