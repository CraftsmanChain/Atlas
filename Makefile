.PHONY: all build clean test verify release-scripts-check web-install web-build run-server run-agent run-dbmigrate

# Binary names
SERVER_BIN=bin/atlas-server
AGENT_BIN=bin/atlas-agent
DBMIGRATE_BIN=bin/atlas-db-migrate

GO ?= go
NPM ?= npm
WEB_DIR=web
TEST_TMPDIR ?= /tmp
TEST_GOCACHE ?= $(TEST_TMPDIR)/atlas-go-cache
TEST_GOTMPDIR ?= $(TEST_TMPDIR)
GO_ENV=GOCACHE=$(TEST_GOCACHE) GOTMPDIR=$(TEST_GOTMPDIR)

all: build

build:
	@echo "Building Atlas components..."
	@mkdir -p bin
	@mkdir -p $(TEST_GOCACHE)
	$(GO_ENV) $(GO) build -o $(SERVER_BIN) ./cmd/server
	$(GO_ENV) $(GO) build -o $(AGENT_BIN) ./cmd/agent
	$(GO_ENV) $(GO) build -o $(DBMIGRATE_BIN) ./cmd/dbmigrate
	@echo "Build complete."

test:
	@mkdir -p $(TEST_GOCACHE)
	$(GO_ENV) $(GO) test ./...

web-install:
	cd $(WEB_DIR) && $(NPM) ci

web-build: web-install
	cd $(WEB_DIR) && $(NPM) run build

release-scripts-check:
	bash -n scripts/check_release_status.sh scripts/deploy_remote_source.sh scripts/remote_build_release.sh scripts/postgres_backup.sh
	@if grep -Rnw scp scripts; then echo "scp is forbidden in release scripts; use rsync instead." >&2; exit 1; fi

verify: release-scripts-check test web-build

clean:
	@echo "Cleaning up..."
	@rm -rf bin
	@echo "Clean complete."

run-server:
	$(GO) run ./cmd/server

run-agent:
	$(GO) run ./cmd/agent

run-dbmigrate:
	$(GO) run ./cmd/dbmigrate
