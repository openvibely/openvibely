.PHONY: dev build build-desktop package-desktop-macos run migrate templ css clean install-tools

# Install development tools
install-tools:
	go install github.com/a-h/templ/cmd/templ@latest
	go install github.com/pressly/goose/v3/cmd/goose@latest
	go install github.com/air-verse/air@latest
	go install github.com/swaggo/swag/cmd/swag@latest

# Development with live reload
dev:
	air

# Generate templ files (no global binary required)
templ:
	go run github.com/a-h/templ/cmd/templ@v0.3.1001 generate

# Generate Swagger documentation (no global binary required)
swagger:
	go run github.com/swaggo/swag/cmd/swag@v1.16.6 init -g cmd/server/main.go -o docs
	@sed -i.bak '/LeftDelim:/d' docs/docs.go && sed -i.bak '/RightDelim:/d' docs/docs.go && rm docs/docs.go.bak || true

# Build production server binary
build: templ swagger
	go build -ldflags="-s -w" -o bin/openvibely ./cmd/server

# Build desktop binary (Wails integration - see cmd/desktop)
build-desktop: templ swagger
	go build -ldflags="-s -w" -o bin/openvibely-desktop ./cmd/desktop

# Package desktop app bundle for macOS Finder/Dock launch (no Terminal)
package-desktop-macos: build-desktop
	@rm -rf bin/OpenVibely.app
	@mkdir -p bin/OpenVibely.app/Contents/MacOS
	@mkdir -p bin/OpenVibely.app/Contents/Resources
	@cp bin/openvibely-desktop bin/OpenVibely.app/Contents/MacOS/OpenVibely
	@chmod +x bin/OpenVibely.app/Contents/MacOS/OpenVibely
	@printf '%s\n' '<?xml version="1.0" encoding="UTF-8"?>' '<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">' '<plist version="1.0">' '<dict>' '<key>CFBundleName</key><string>OpenVibely</string>' '<key>CFBundleDisplayName</key><string>OpenVibely</string>' '<key>CFBundleIdentifier</key><string>com.openvibely.desktop</string>' '<key>CFBundleVersion</key><string>1.0.0</string>' '<key>CFBundleShortVersionString</key><string>1.0.0</string>' '<key>CFBundlePackageType</key><string>APPL</string>' '<key>CFBundleExecutable</key><string>OpenVibely</string>' '<key>LSMinimumSystemVersion</key><string>12.0</string>' '</dict>' '</plist>' > bin/OpenVibely.app/Contents/Info.plist
	@echo 'Created bin/OpenVibely.app (launch this from Finder for no Terminal window)'

# Run the server
run: build
	./bin/openvibely

# Run database migrations (standalone)
migrate:
	go run ./cmd/server migrate

# Clean build artifacts
clean:
	rm -rf bin/
