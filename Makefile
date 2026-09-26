.PHONY: all build build-arm64 test clean run validate install check-go deps

BINARY_NAME=sekha-working-scratchpad
VALIDATE_NAME=sekha-scratchpad-validate
NODE2_HOST=192.168.8.175
NODE2_USER=admin

ENV_FILE ?= .env

all: check-go build

check-go:
	@which go > /dev/null 2>&1 || (echo "ERROR: 'go' is not installed or not in PATH. Run 'make deps' or 'sudo apt install -y golang-go' on Debian/Raspberry Pi OS." && exit 1)

deps:
	@echo "Installing Go compiler on Debian / Raspberry Pi OS..."
	sudo apt update && sudo apt install -y golang-go git
	@echo "Go installation verified: $$(go version)"

build: check-go
	@mkdir -p bin
	go build -ldflags="-s -w" -o bin/$(BINARY_NAME) ./cmd/server
	go build -ldflags="-s -w" -o bin/$(VALIDATE_NAME) ./cmd/validate
	@echo "Build complete: bin/$(BINARY_NAME) and bin/$(VALIDATE_NAME)"

build-arm64: check-go
	@mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o bin/$(BINARY_NAME)-linux-arm64 ./cmd/server
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o bin/$(VALIDATE_NAME)-linux-arm64 ./cmd/validate
	@echo "Cross-compiled ARM64 binaries in bin/"

install: build
	sudo systemctl stop sekha-working-scratchpad.service 2>/dev/null || true
	sudo cp bin/$(BINARY_NAME) /usr/local/bin/
	@if [ -n "$(ENV_FILE)" ] && [ -f "$(ENV_FILE)" ]; then \
		echo "Installing environment configuration from $(ENV_FILE) to /etc/default/sekha..."; \
		sudo mkdir -p /etc/default; \
		sudo cp $(ENV_FILE) /etc/default/sekha; \
	elif [ -f /etc/default/sekha ]; then \
		echo "Preserving existing /etc/default/sekha configuration."; \
	else \
		echo "No environment file specified or found; proceeding without external embedding configuration."; \
	fi
	sudo cp systemd/sekha-working-scratchpad.service /etc/systemd/system/
	sudo systemctl daemon-reload
	sudo systemctl restart sekha-working-scratchpad.service
	@echo "Daemon updated and restarted: sekha-working-scratchpad.service"

test: check-go
	go test -v ./...

validate: check-go
	./bin/$(VALIDATE_NAME)

run: check-go
	go run ./cmd/server

clean:
	rm -rf bin/
