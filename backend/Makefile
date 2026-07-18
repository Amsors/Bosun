.PHONY: build test lint fmt sqlc migrate-up migrate-down

DATABASE_URL ?= postgres://bosun:bosun@localhost:5432/bosun?sslmode=disable

build:
	go build ./cmd/api ./cmd/gateway

test:
	go test ./...

lint:
	golangci-lint run ./...

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

sqlc:
	sqlc generate

migrate-up:
	migrate -path db/migrations -database "$(DATABASE_URL)" up

migrate-down:
	migrate -path db/migrations -database "$(DATABASE_URL)" down 1
