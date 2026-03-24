.PHONY: all build clean run-gateway run-agent run-api

# Binary names
GATEWAY_BIN=bin/gateway
AGENT_BIN=bin/agent
API_BIN=bin/api

all: build

build:
	@echo "Building Atlas components..."
	@mkdir -p bin
	go build -o $(GATEWAY_BIN) ./cmd/gateway
	go build -o $(AGENT_BIN) ./cmd/agent
	go build -o $(API_BIN) ./cmd/api
	@echo "Build complete."

clean:
	@echo "Cleaning up..."
	@rm -rf bin
	@echo "Clean complete."

run-gateway:
	go run ./cmd/gateway/main.go

run-agent:
	go run ./cmd/agent/main.go

run-api:
	go run ./cmd/api/main.go
