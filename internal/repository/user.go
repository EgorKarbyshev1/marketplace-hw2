package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"marketplace/internal/generated"
	"time"
)

type StoredUser struct {
	generated.UserResponse
	PasswordHash string
}
type Users struct{ Pool *pgxpool.Pool }

func NewUsers(pool *pgxpool.Pool) *Users { return &Users{Pool: pool} }
func scanUser(row scanner) (StoredUser, error) {
	var user StoredUser
	var id string
	err := row.Scan(&id, &user.Email, &user.PasswordHash, &user.Role, &user.CreatedAt, &user.UpdatedAt)
	if err == nil {
		user.Id = uuid.MustParse(id)
	}
	return user, err
}
func (r *Users) Create(ctx context.Context, email, hash string, role generated.Role) (StoredUser, error) {
	return scanUser(r.Pool.QueryRow(ctx, `INSERT INTO users(id,email,password_hash,role) VALUES($1::text::uuid,$2,$3,$4)
        RETURNING id::text,email,password_hash,role,created_at,updated_at`, uuid.NewString(), email, hash, string(role)))
}
func (r *Users) ByEmail(ctx context.Context, email string) (StoredUser, error) {
	return scanUser(r.Pool.QueryRow(ctx, `SELECT id::text,email,password_hash,role,created_at,updated_at FROM users WHERE email=$1`, email))
}
func (r *Users) ByID(ctx context.Context, id uuid.UUID) (StoredUser, error) {
	return scanUser(r.Pool.QueryRow(ctx, `SELECT id::text,email,password_hash,role,created_at,updated_at FROM users WHERE id=$1::text::uuid`, id.String()))
}
func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
func (r *Users) Session(ctx context.Context, id, user uuid.UUID, token string, expires time.Time) error {
	_, err := r.Pool.Exec(ctx, `INSERT INTO refresh_tokens(id,user_id,token_hash,expires_at) VALUES($1::text::uuid,$2::text::uuid,$3,$4)`, id.String(), user.String(), TokenHash(token), expires)
	return err
}
func (r *Users) Rotate(ctx context.Context, oldID, newID, user uuid.UUID, oldToken, newToken string, expires time.Time) error {
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `DELETE FROM refresh_tokens WHERE id=$1::text::uuid AND user_id=$2::text::uuid AND token_hash=$3 AND expires_at>clock_timestamp()`, oldID.String(), user.String(), TokenHash(oldToken))
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	if _, err = tx.Exec(ctx, `INSERT INTO refresh_tokens(id,user_id,token_hash,expires_at) VALUES($1::text::uuid,$2::text::uuid,$3,$4)`, newID.String(), user.String(), TokenHash(newToken), expires); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
