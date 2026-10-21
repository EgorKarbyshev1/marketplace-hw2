FROM golang:1.25-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.4.1 --config api/oapi-codegen.yaml api/openapi.yaml
RUN go test ./...
RUN CGO_ENABLED=0 go build -trimpath -o /server ./cmd/server
RUN CGO_ENABLED=0 go build -trimpath -o /create-user ./cmd/create-user

FROM flyway/flyway:11.8.1-alpine
WORKDIR /app
COPY --from=build /server ./server
COPY --from=build /create-user ./create-user
COPY migrations ./migrations
COPY api/openapi.yaml ./api/openapi.yaml
COPY web ./web
EXPOSE 8080
ENTRYPOINT ["./server"]
