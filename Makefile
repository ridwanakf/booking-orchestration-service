.PHONY: build test fmt tidy swagger generate run migrate up down logs check

build:
	go build ./...

test:
	go test -race ./...

test-integration:
	go test -race -tags integration -count=1 ./internal/repository/...

fmt:
	go fmt ./...

tidy:
	go mod tidy

swagger:
	PATH="$(shell go env GOPATH)/bin:$$PATH" swag init -g main.go -o docs --parseInternal --quiet

generate: swagger mocks

run:
	go run . serve

migrate:
	go run . migrate

up:
	docker compose up -d --build

down:
	docker compose down -v

logs:
	docker compose logs -f

mocks:
	PATH="$(shell go env GOPATH)/bin:$$PATH" go generate ./...

vet:
	go vet ./...
	@test -z "$$(gofmt -l . | grep -v '^docs/')" || (echo "unformatted:"; gofmt -l . | grep -v '^docs/'; exit 1)

lint:
	golangci-lint run ./...

check: swagger vet build test
