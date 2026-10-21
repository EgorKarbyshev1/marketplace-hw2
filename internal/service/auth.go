package service

import (
	"context"
	"errors"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"
	"marketplace/internal/generated"
	"marketplace/internal/repository"
	"marketplace/internal/security"
	"net/mail"
	"strings"
	"time"
)

type Auth struct {
	Users  *repository.Users
	Tokens *security.Tokens
}

func NewAuth(users *repository.Users, tokens *security.Tokens) *Auth {
	return &Auth{Users: users, Tokens: tokens}
}
func (a *Auth) Register(ctx context.Context, input generated.Credentials) (generated.UserResponse, error) {
	return a.CreateUser(ctx, input, "USER")
}
func (a *Auth) CreateUser(ctx context.Context, input generated.Credentials, role generated.Role) (generated.UserResponse, error) {
	email := strings.ToLower(strings.TrimSpace(input.Email))
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email || len(email) > 254 {
		return generated.UserResponse{}, &ValidationError{Details: map[string]any{"email": "Invalid email address"}}
	}
	if len(input.Password) < 8 || len(input.Password) > 72 {
		return generated.UserResponse{}, &ValidationError{Details: map[string]any{"password": "Password must contain 8 to 72 UTF-8 bytes"}}
	}
	if role != "USER" && role != "SELLER" && role != "ADMIN" {
		return generated.UserResponse{}, errors.New("invalid role")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		return generated.UserResponse{}, err
	}
	user, err := a.Users.Create(ctx, email, string(hash), role)
	var failure *pgconn.PgError
	if errors.As(err, &failure) && failure.Code == "23505" {
		return generated.UserResponse{}, &ValidationError{Details: map[string]any{"email": "Email is already registered"}}
	}
	return user.UserResponse, err
}
func (a *Auth) pair(user repository.StoredUser) (generated.TokenPair, uuid.UUID, error) {
	actor := security.Principal{ID: user.Id, Role: user.Role}
	access, _, err := a.Tokens.Sign(actor, "access", a.Tokens.AccessTTL)
	if err != nil {
		return generated.TokenPair{}, uuid.Nil, err
	}
	refresh, id, err := a.Tokens.Sign(actor, "refresh", a.Tokens.RefreshTTL)
	return generated.TokenPair{AccessToken: access, RefreshToken: refresh, TokenType: "Bearer", ExpiresIn: int64(a.Tokens.AccessTTL / time.Second)}, id, err
}
func (a *Auth) Login(ctx context.Context, input generated.Credentials) (generated.TokenPair, error) {
	user, err := a.Users.ByEmail(ctx, strings.ToLower(strings.TrimSpace(input.Email)))
	if errors.Is(err, pgx.ErrNoRows) {
		return generated.TokenPair{}, business("TOKEN_INVALID", "Invalid email or password")
	}
	if err != nil {
		return generated.TokenPair{}, err
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(input.Password)) != nil {
		return generated.TokenPair{}, business("TOKEN_INVALID", "Invalid email or password")
	}
	pair, id, err := a.pair(user)
	if err != nil {
		return pair, err
	}
	err = a.Users.Session(ctx, id, user.Id, pair.RefreshToken, time.Now().Add(a.Tokens.RefreshTTL))
	return pair, err
}
func (a *Auth) Refresh(ctx context.Context, value string) (generated.TokenPair, error) {
	claims, err := a.Tokens.Parse(value, "refresh")
	if err != nil {
		return generated.TokenPair{}, business("REFRESH_TOKEN_INVALID", "Invalid refresh token")
	}
	user, err := a.Users.ByID(ctx, uuid.MustParse(claims.UserID))
	if errors.Is(err, pgx.ErrNoRows) {
		return generated.TokenPair{}, business("REFRESH_TOKEN_INVALID", "Invalid refresh token")
	}
	if err != nil {
		return generated.TokenPair{}, err
	}
	pair, id, err := a.pair(user)
	if err != nil {
		return pair, err
	}
	err = a.Users.Rotate(ctx, uuid.MustParse(claims.ID), id, user.Id, value, pair.RefreshToken, time.Now().Add(a.Tokens.RefreshTTL))
	if errors.Is(err, pgx.ErrNoRows) {
		return generated.TokenPair{}, business("REFRESH_TOKEN_INVALID", "Invalid refresh token")
	}
	return pair, err
}
func (a *Auth) Authenticate(ctx context.Context, value string) (security.Principal, error) {
	claims, err := a.Tokens.Parse(value, "access")
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return security.Principal{}, business("TOKEN_EXPIRED", "Access token expired")
		}
		return security.Principal{}, business("TOKEN_INVALID", "Invalid token")
	}
	user, err := a.Users.ByID(ctx, uuid.MustParse(claims.UserID))
	if errors.Is(err, pgx.ErrNoRows) {
		return security.Principal{}, business("TOKEN_INVALID", "Invalid token")
	}
	if err != nil {
		return security.Principal{}, err
	}
	if user.Role != claims.Role {
		return security.Principal{}, business("TOKEN_INVALID", "Token role is no longer valid")
	}
	return security.Principal{ID: user.Id, Role: user.Role}, nil
}
