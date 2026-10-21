package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"marketplace/internal/database"
	"marketplace/internal/generated"
	"marketplace/internal/repository"
	"marketplace/internal/service"
	"os"
	"time"
)

func main() {
	email := flag.String("email", "", "Email")
	role := flag.String("role", "USER", "USER, SELLER or ADMIN")
	flag.Parse()
	if os.Getenv("ALLOW_DEMO_USERS") != "true" {
		log.Fatal("Set ALLOW_DEMO_USERS=true explicitly for local demonstration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := database.Open(ctx, os.Getenv("DATABASE_URL"), "migrations")
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()
	accounts := service.NewAuth(repository.NewUsers(pool), nil)
	user, err := accounts.CreateUser(ctx, generated.Credentials{Email: *email, Password: os.Getenv("DEMO_USER_PASSWORD")}, generated.Role(*role))
	if err != nil {
		log.Fatal(err)
	}
	if err = json.NewEncoder(os.Stdout).Encode(user); err != nil {
		log.Fatal(err)
	}
}
