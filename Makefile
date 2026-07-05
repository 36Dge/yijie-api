.PHONY: dev test lint generate

dev:
	go run ./cmd/api-server

test:
	go test ./...

lint:
	go test ./...

generate:
	echo "No generated assets yet"
