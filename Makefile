.PHONY: generate build test run

generate:
	go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.4.1 --config api/oapi-codegen.yaml api/openapi.yaml

build: generate
	go build -o bin/marketplace ./cmd/server

test: generate
	go test ./...

run: generate
	go run ./cmd/server
