package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"marketplace/internal/database"
	"marketplace/internal/handler"
	"marketplace/internal/repository"
	"marketplace/internal/security"
	"marketplace/internal/service"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("startup failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	pool, err := database.Open(startup, env("DATABASE_URL", "postgres://marketplace:marketplace@localhost:5432/marketplace?sslmode=disable"), "migrations")
	if err != nil {
		return err
	}
	defer pool.Close()
	slog.Info("Database connected, Flyway migrations applied")
	minutes, err := strconv.Atoi(env("ORDER_OPERATION_INTERVAL_MINUTES", "1"))
	if err != nil || minutes < 0 || minutes > 525600 {
		return errors.New("ORDER_OPERATION_INTERVAL_MINUTES must be 0 to 525600")
	}
	orders := service.NewOrders(repository.NewOrders(pool), time.Duration(minutes)*time.Minute)
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		return nil
	}
	accessMinutes, err := strconv.Atoi(env("ACCESS_TOKEN_MINUTES", "20"))
	if err != nil {
		return err
	}
	refreshDays, err := strconv.Atoi(env("REFRESH_TOKEN_DAYS", "14"))
	if err != nil {
		return err
	}
	tokens, err := security.NewTokens(os.Getenv("JWT_SECRET"), time.Duration(accessMinutes)*time.Minute, time.Duration(refreshDays)*24*time.Hour)
	if err != nil {
		return err
	}
	accounts := service.NewAuth(repository.NewUsers(pool), tokens)
	api, err := handler.New(service.New(repository.New(pool)), orders, accounts)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("/", handler.Secure(api, accounts))
	mux.Handle("GET /swagger/", http.StripPrefix("/swagger/", http.FileServer(http.Dir("web"))))
	mux.HandleFunc("GET /openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		http.ServeFile(w, r, "api/openapi.yaml")
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/swagger/", http.StatusSeeOther)
	})
	server := &http.Server{
		Addr: ":" + env("PORT", "8080"), Handler: handler.Audit(mux, slog.Default()),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second,
	}
	failures := make(chan error, 1)
	go func() {
		slog.Info("API listening", "address", server.Addr)
		failures <- server.ListenAndServe()
	}()
	select {
	case err := <-failures:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
