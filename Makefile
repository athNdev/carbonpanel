# SPDX-License-Identifier: MIT AND MPL-2.0
# This file includes DiscoPanel material under the MIT License,
# Copyright (c) 2025 Nicholas W. Heyer. The MIT license text is in LICENSE
# and still applies to that inherited material.
# Maintainer changes are licensed under the Mozilla Public License, v. 2.0.
# If a copy of the MPL was not distributed with this file, You can obtain
# one at https://mozilla.org/MPL/2.0/.


.PHONY: dev prod clean build build-frontend run deps test fmt lint check help kill-dev image dev-docker dev-auth modules proto proto-clean proto-lint proto-format proto-breaking gen dev-docs knowledge knowledge-lint knowledge-index knowledge-search knowledge-mcp knowledge-mcp-git knowledge-mcp-smoke knowledge-for-path snapshot-panel-db compose-upgrade compose-upgrade-rollback review-stack-clean

DATA_DIR := ./data
FRONTEND_DIR := web/carbon-panel
CARBONPANEL_BIN := build/carbon-panel
BUF_IMAGE ?= bufbuild/buf:1.50.0
BUF_RUN := docker run --rm \
	--volume "$(shell pwd):/workspace" \
	--workdir /workspace \
	--user "$(shell id -u):$(shell id -g)" \
	--env HOME=/tmp \
	$(BUF_IMAGE)

# Development mode - runs backend (with live reload) and frontend concurrently
run:
	@bun run scripts/dev.ts

dev: run

# Build and run with OIDC provider (Keycloak)
dev-auth-%:
	@echo "Building and running with OIDC provider..."
	docker compose -f oidc/$*/docker-compose.yaml up --build

dev-docker:
	@echo "Building and running with base compose..."
	docker compose up --build

dev-docs:
	cd docs/carbon-panel && npm run dev
	
# Production build and run
prod: build-frontend
	@echo "Building for production..."
	@mkdir -p $(DATA_DIR)
	go build -o $(CARBONPANEL_BIN) ./cmd/carbon-panel

# Build frontend for production
build-frontend:
	@echo "Building frontend..."
	@bash scripts/web-build.sh

# Build backend with embedded frontend
build: build-frontend
	@echo "Building backend with embedded frontend..."
	go build -o $(CARBONPANEL_BIN) ./cmd/carbon-panel

# Build and push Docker image to :dev tag
image:
	@echo "Building Docker image..."
	@bash scripts/build.sh

# Build and push Docker image to Docker Hub
image-push:
	@echo "Building and pushing Docker image..."
	@bash scripts/build.sh dev --push

# Build and push all module Docker images
modules: gen
	@echo "Building and pushing module images..."
	@for dockerfile in docker/Dockerfile.*; do \
		name=$$(basename $$dockerfile | sed 's/Dockerfile\.//'); \
		if [ "$$name" != "carbon-panel" ]; then \
			echo "Building athndev/carbon-panel-$$name:latest..."; \
			docker build -t "athndev/carbon-panel-$$name:latest" -f "$$dockerfile" . && \
			echo "Pushing athndev/carbon-panel-$$name:latest..." && \
			docker push "athndev/carbon-panel-$$name:latest"; \
		fi \
	done
	@echo "Module builds complete!"

# Build and push a specific module (e.g., make module-status, make module-geyser)
module-%: gen
	@if [ ! -f "docker/Dockerfile.$*" ]; then \
		echo "Error: docker/Dockerfile.$* not found"; \
		echo "Available modules:"; \
		ls docker/Dockerfile.* 2>/dev/null | sed 's/docker\/Dockerfile\./  /g' | grep -v carbon-panel; \
		exit 1; \
	fi
	@echo "Building athndev/carbon-panel-$*:latest..."
	@docker build -t "athndev/carbon-panel-$*:latest" -f "docker/Dockerfile.$*" .
	@echo "Pushing athndev/carbon-panel-$*:latest..."
	@docker push "athndev/carbon-panel-$*:latest"
	@echo "Module $* build complete!"

# Remove generated build outputs only. This is not a data-reset: server worlds,
# the controller SQLite file, backups and Docker bind mounts stay in place.
# Named destructive cleanup is `make review-stack-clean` (review stack only).
# Snapshot the controller DB with `make snapshot-panel-db`.
clean:
	@echo "Removing generated build outputs only; server data and Docker volumes are preserved."
	@if [ -f "$(CARBONPANEL_BIN)" ]; then \
		echo "Removing backend binary..."; \
		rm -f $(CARBONPANEL_BIN); \
	fi
	@echo "Clean complete!"

# Operator snapshot of the controller SQLite file. Not invoked by test, build,
# web-build, or compose up. Writes a timestamped copy beside the data directory.
snapshot-panel-db:
	@bash scripts/snapshot-panel-db.sh

# Named Compose image recreate. Default is dry-run. Pass APPLY=1 to snapshot
# the controller DB and run `docker compose up -d --pull always` (no --volumes).
compose-upgrade:
	@if [ "$(APPLY)" = "1" ]; then \
		bash scripts/compose-upgrade.sh --apply; \
	else \
		bash scripts/compose-upgrade.sh; \
	fi

# Copy a snapshot-panel-db artifact back onto the controller SQLite file.
# Default is dry-run. Pass SNAPSHOT=path APPLY=1. Does not delete worlds.
compose-upgrade-rollback:
	@if [ -z "$(SNAPSHOT)" ]; then echo 'usage: make compose-upgrade-rollback SNAPSHOT=path [APPLY=1]'; exit 2; fi
	@if [ "$(APPLY)" = "1" ]; then \
		bash scripts/compose-upgrade-rollback.sh --snapshot "$(SNAPSHOT)" --apply; \
	else \
		bash scripts/compose-upgrade-rollback.sh --snapshot "$(SNAPSHOT)"; \
	fi

# Named destructive cleanup for the disposable review stack only. Never a
# side effect of make clean / test / build.
review-stack-clean:
	@bash scripts/review/devstack.sh clean

# Never infer process ownership from an executable name.
kill-dev:
	@echo "Use Ctrl+C in the owning development session to stop its process trees."
	@echo "For an orphan, inspect its exact PID and stop it explicitly; process-name killing is unsupported."

# Install dependencies
deps:
	@echo "Installing Go dependencies..."
	go mod download
	@echo "Installing frontend dependencies..."
	cd $(FRONTEND_DIR) && bun install --frozen-lockfile

# Dependency updates are deliberate and separate from the frozen install.
deps-update-buf:
	$(BUF_RUN) dep update

deps-update-frontend:
	cd $(FRONTEND_DIR) && bun update

# Run tests
test:
	@echo "Running Go tests..."
	go test ./...

# Format code
fmt:
	@echo "Formatting Go code..."
	go fmt ./...
	@echo "Formatting frontend code..."
	cd $(FRONTEND_DIR) && bun run format

# Lint code
lint: proto-lint
	@echo "Linting frontend code..."
	cd $(FRONTEND_DIR) && bun run lint

# Type check frontend
check:
	@echo "Type checking frontend..."
	cd $(FRONTEND_DIR) && bun run check

# Validate the knowledge base
knowledge: knowledge-lint

knowledge-lint:
	@echo "Linting knowledge base..."
	go run ./cmd/knowledge-lint -root .

knowledge-index:
	go run ./cmd/knowledge-lint -root . -fix

knowledge-search:
	@if [ -z "$(q)" ]; then echo 'usage: make knowledge-search q="..."'; exit 2; fi
	@if command -v rg >/dev/null 2>&1; then \
		rg -n --heading --glob '!index.md' --glob '!README.md' "$(q)" knowledge; \
	else \
		grep -Rni --exclude=index.md --exclude=README.md "$(q)" knowledge; \
	fi

knowledge-mcp:
	npx -y @modelcontextprotocol/server-filesystem@2.0.0 knowledge

knowledge-mcp-git:
	npx -y @cyanheads/git-mcp-server@2.15.3

knowledge-mcp-smoke:
	node scripts/mcp-smoke.mjs

proto:
	@echo "Generating protocol buffer code (using Docker)..."
	BUF_IMAGE="$(BUF_IMAGE)" bun scripts/proto-generate.mjs --docker
	@echo "Proto generation complete!"

proto-clean:
	@echo "Cleaning generated proto files..."
	rm -rf pkg/proto
	rm -rf web/carbon-panel/src/lib/proto
	@echo "Proto files cleaned!"

proto-lint:
	@echo "Linting proto files (using Docker)..."
	$(BUF_RUN) lint
	@echo "Proto linting complete!"

gen: proto

proto-format:
	@echo "Formatting proto files (using Docker)..."
	$(BUF_RUN) format -w
	@echo "Proto files formatted!"

proto-breaking:
	@echo "Checking for breaking changes (using Docker)..."
	$(BUF_RUN) breaking --against '.git#branch=main'
	@echo "Breaking change check complete!"

# proto-install:
# 	go install github.com/sudorandom/protoc-gen-connect-openapi@latest

# Help
help:
	@echo "Available commands:"
	@echo "  make dev            - Run in development mode (frontend + backend)"
	@echo "  make build          - Build standalone binary with embedded frontend"
	@echo "  make prod           - Build and run in production mode"
	@echo "  make image          - Build and push Docker image to :dev tag"
	@echo "  make dev-docker     - Build and run Docker container locally (no cache)"
	@echo "  make dev-auth       - Build and run with OIDC provider (Keycloak)"
	@echo "  make modules        - Build and push all module Docker images"
	@echo "  make clean          - Remove the generated backend binary; worlds and databases are preserved"
	@echo "  make snapshot-panel-db - Copy controller SQLite beside the data dir (named operator step)"
	@echo "  make compose-upgrade - Dry-run Compose image recreate (APPLY=1 to snapshot and up --pull)"
	@echo "  make compose-upgrade-rollback - Copy a DB snapshot back (SNAPSHOT=path, APPLY=1 to write)"
	@echo "  make review-stack-clean - Delete ONLY the disposable review stack data (named destructive)"
	@echo "  make kill-dev       - Explain safe session-owned development shutdown"
	@echo "  make deps           - Install all dependencies"
	@echo "  make test           - Run tests"
	@echo "  make fmt            - Format code"
	@echo "  make lint           - Lint code"
	@echo "  make check          - Type check frontend"
	@echo "  make knowledge-lint   - Validate the knowledge base"
	@echo "  make knowledge-index  - Regenerate knowledge indexes"
	@echo "  make knowledge-search - Search the knowledge base (q=...)"
	@echo "  make knowledge-mcp-smoke - Exercise the pinned MCP servers"
	@echo "  make knowledge-for-path - Show concepts governing a path (path=...)"
	@echo "  make gen            - Stage and regenerate proto code (via Docker)"
	@echo "  make deps-update-buf / deps-update-frontend - Deliberately update lockfiles"
	@echo "  make proto          - Generate Go and TypeScript code from proto files (via Docker)"
	@echo "  make proto-clean    - Remove all generated proto files"
	@echo "  make proto-lint     - Lint proto files for style and correctness (via Docker)"
	@echo "  make proto-format   - Format proto files (via Docker)"
	@echo "  make proto-breaking - Check for breaking changes against main (via Docker)"
	@echo "  make help           - Show this help message"

knowledge-for-path:
	@if [ -z "$(path)" ]; then echo 'usage: make knowledge-for-path path=...'; exit 2; fi
	go run ./cmd/knowledge-lint -root . -for-path $(path)
