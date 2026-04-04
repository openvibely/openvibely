.PHONY: dev build run migrate templ css clean install-tools

# Install development tools
install-tools:
	go install github.com/a-h/templ/cmd/templ@latest
	go install github.com/pressly/goose/v3/cmd/goose@latest
	go install github.com/air-verse/air@latest
	go install github.com/swaggo/swag/cmd/swag@latest

# Development with live reload
dev:
	air

# Generate templ files
templ:
	templ generate

# Generate Swagger documentation
swagger:
	@GOPATH=$$(go env GOPATH); $$GOPATH/bin/swag init -g cmd/server/main.go -o docs
	@sed -i.bak '/LeftDelim:/d' docs/docs.go && sed -i.bak '/RightDelim:/d' docs/docs.go && rm docs/docs.go.bak || true

# Build production binary
build: templ swagger
	go build -ldflags="-s -w" -o bin/openvibely ./cmd/server

# Run the server
run: build
	./bin/openvibely

# Run database migrations (standalone)
migrate:
	go run ./cmd/server migrate

# Clean build artifacts
clean:
	rm -rf bin/
