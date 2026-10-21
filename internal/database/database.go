package database

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func Open(ctx context.Context, databaseURL, migrationPath string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	if err = pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	if err = applyMigrations(ctx, pool, migrationPath); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}
func applyMigrations(ctx context.Context, pool *pgxpool.Pool, path string) error {
	if os.Getenv("MIGRATIONS_MODE") == "verify" {
		if !strings.HasSuffix(pool.Config().ConnConfig.Database, "_test") {
			return errors.New("migration verification mode is restricted to test databases")
		}
		var ready bool
		err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM flyway_schema_history WHERE version='3' AND success)`).Scan(&ready)
		if err != nil {
			return err
		}
		if !ready {
			return errors.New("test database must be migrated by Flyway first")
		}
		return nil
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err = conn.Exec(ctx, `SELECT pg_advisory_lock(79241021)`); err != nil {
		return err
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock(79241021)`)
	var history, legacy bool
	if err = conn.QueryRow(ctx, `SELECT to_regclass('public.flyway_schema_history') IS NOT NULL,to_regclass('public.schema_migrations') IS NOT NULL`).Scan(&history, &legacy); err != nil {
		return err
	}
	config := pool.Config().ConnConfig
	directory, err := filepath.Abs(filepath.Join(path, "flyway"))
	if err != nil {
		return err
	}
	binary := os.Getenv("FLYWAY_BINARY")
	if binary == "" {
		binary = "/flyway/flyway"
	}
	run := func(command string) error {
		cmd := exec.CommandContext(ctx, binary, "-baselineVersion=2", "-baselineOnMigrate=false", "-cleanDisabled=true", command)
		cmd.Env = append(os.Environ(), "FLYWAY_URL=jdbc:postgresql://"+config.Host+":"+fmt.Sprint(config.Port)+"/"+config.Database,
			"FLYWAY_USER="+config.User, "FLYWAY_PASSWORD="+config.Password, "FLYWAY_LOCATIONS=filesystem:"+directory)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("Flyway %s failed: %s", command, output)
		}
		return nil
	}
	if !history && legacy {
		var version int
		var dirty bool
		if err = conn.QueryRow(ctx, `SELECT version,dirty FROM schema_migrations`).Scan(&version, &dirty); err != nil {
			return err
		}
		if version != 2 || dirty {
			return errors.New("legacy schema must be clean at version 2; automatic baseline refused")
		}
		expected := map[string]string{
			"products":        "id,name,description,price,stock,category,status,created_at,updated_at",
			"orders":          "id,user_id,status,promo_code_id,total_amount,discount_amount,created_at,updated_at",
			"order_items":     "id,order_id,product_id,quantity,price_at_order",
			"promo_codes":     "id,code,discount_type,discount_value,min_order_amount,max_uses,current_uses,valid_from,valid_until,active",
			"user_operations": "id,user_id,operation_type,created_at",
		}
		for table, columns := range expected {
			var actual string
			if err = conn.QueryRow(ctx, `SELECT string_agg(column_name,',' ORDER BY ordinal_position) FROM information_schema.columns WHERE table_schema='public' AND table_name=$1`, table).Scan(&actual); err != nil {
				return err
			}
			if actual != columns {
				return fmt.Errorf("legacy columns do not match version 2 in %s; baseline refused", table)
			}
		}
		var valid bool
		if err = conn.QueryRow(ctx, `SELECT to_regclass('idx_orders_one_active') IS NOT NULL AND to_regclass('idx_products_status') IS NOT NULL AND to_regclass('users') IS NULL`).Scan(&valid); err != nil {
			return err
		}
		if !valid {
			return errors.New("legacy indexes or tables do not match version 2")
		}
		if err = run("baseline"); err != nil {
			return err
		}
	}
	return run("migrate")
}
