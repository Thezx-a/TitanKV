# =========================================================
# TitanKV — unified Makefile
#
# Usage:
#   make help         # list targets
#   make build        # build all C++ + Go components
#   make test         # run all tests
#   make lint         # lint C++ & Go
#   make docker-up    # start local dev stack (compose)
#   make docker-down  # stop local dev stack
#   make run-gateway  # run gateway service (Phase 3)
#   make run-auth     # run auth service (Phase 3)
#   make run-data     # run data service (Phase 4)
#   make run-meta     # run meta service (Phase 4)
#   make run-observ   # run observability service (Phase 4)
#   make run-all      # run minikv + Go + skynet front proxy
#   make web-install  # install Next.js deps
#   make web-dev      # run Next.js dev server
# =========================================================

SHELL := /bin/bash

CMAKE_BUILD_DIR  ?= build
CMAKE_BUILD_TYPE ?= Release
JOBS             ?= $(shell nproc 2>/dev/null || echo 4)

GO            ?= go
GO_TEST_FLAGS ?= -race -count=1
GOLINT        ?= golangci-lint
CLANG_TIDY    ?= clang-tidy

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}'

# ---------------------------------------------------------
# C++ build / test / lint
# ---------------------------------------------------------
.PHONY: cmake-configure cmake-build cpp-test cpp-lint
cmake-configure: ## Configure CMake build
	cmake -B $(CMAKE_BUILD_DIR) -G Ninja -DCMAKE_BUILD_TYPE=$(CMAKE_BUILD_TYPE) \
		-DENABLE_TESTS=ON -DENABLE_BENCHMARKS=OFF \
		-DCMAKE_CXX_COMPILER=g++-12 \
		-DCMAKE_POLICY_VERSION_MINIMUM=3.5

cmake-build: cmake-configure ## Build all C++ targets
	cmake --build $(CMAKE_BUILD_DIR) -j $(JOBS)

cpp-test: cmake-build ## Run C++ unit tests (ctest)
	cd $(CMAKE_BUILD_DIR) && ctest --output-on-failure --parallel $(JOBS)

cpp-lint: ## Run clang-tidy on C++ sources
	@find minikv skynet -name '*.cpp' -o -name '*.h' \
		| xargs -r $(CLANG_TIDY) -p $(CMAKE_BUILD_DIR)

# ---------------------------------------------------------
# Go build / test / lint
# ---------------------------------------------------------
.PHONY: go-build go-test go-lint go-mod go-tidy
go-mod: ## Tidy and download Go modules
	$(GO) mod download

go-tidy: ## Run go mod tidy
	$(GO) mod tidy

go-build: go-mod ## Build all Go services and tools
	$(GO) build ./...

go-test: go-mod ## Run Go tests with race detector
	$(GO) test ./... $(GO_TEST_FLAGS)

go-lint: ## Run golangci-lint on all Go code
	$(GO) vet ./...
	@if command -v $(GOLINT) >/dev/null 2>&1; then $(GOLINT) run ./...; else echo "skip (golangci-lint not installed)"; fi

# ---------------------------------------------------------
# Run services (Phase 3 / Phase 4)
# ---------------------------------------------------------
MINIKV_BIN  ?= $(CMAKE_BUILD_DIR)/minikv/minikv_server
MINIKV_ADDR ?= 127.0.0.1:8888
MINIKV_DB   ?= ./data/minikv_data
MINIKV_PORT ?= 8888
# 方案一：skynet 占对外 :8080，Gin 内网 :18080
SKYNET_BIN          ?= $(CMAKE_BUILD_DIR)/skynet/skynet_gateway
SKYNET_CONFIG       ?= $(CURDIR)/skynet/gateway/gateway.yaml
GIN_INTERNAL_ADDR   ?= :18080
PUBLIC_GATEWAY_PORT ?= 8080

.PHONY: run-gateway run-auth run-data run-meta run-observ run-minikv run-skynet run-all smoke-skynet
run-gateway: ## Run Gin gateway alone (default :8080; behind skynet use GATEWAY_ADDR=:18080)
	$(GO) run ./cmd/gateway

run-auth: ## Run auth service (Phase 3, port 8082)
	$(GO) run ./cmd/auth

run-data: ## Run data service (Phase 4, port 8081); set MINIKV_ADDR to use C++ engine
	MINIKV_ADDR=$(MINIKV_ADDR) $(GO) run ./cmd/data

run-meta: ## Run meta service (Phase 4, port 8083)
	$(GO) run ./cmd/meta

run-observ: ## Run observability service (Phase 4, port 8084)
	$(GO) run ./cmd/observability

run-minikv: cmake-build ## Run C++ minikv_server (default :8888)
	@mkdir -p $(MINIKV_DB)
	$(MINIKV_BIN) --host 0.0.0.0 --port $(MINIKV_PORT) --db $(MINIKV_DB)

run-skynet: cmake-build ## Run skynet front proxy (public :8080 → Gin :18080)
	@test -x "$(SKYNET_BIN)" || $(MAKE) cmake-build
	$(SKYNET_BIN) --config $(SKYNET_CONFIG)

smoke-skynet: cmake-build ## Smoke Client→skynet(:8080)→Gin(:18080)
	bash scripts/smoke_skynet_front.sh

run-all: ## Run minikv + Go services + skynet front proxy (Ctrl+C stops all)
	@echo "Starting minikv + Go + skynet front proxy. Public entry :$(PUBLIC_GATEWAY_PORT) → Gin $(GIN_INTERNAL_ADDR)"
	@mkdir -p $(MINIKV_DB)
	@if [ ! -x "$(MINIKV_BIN)" ] || [ ! -x "$(SKYNET_BIN)" ]; then $(MAKE) cmake-build; fi
	@$(MINIKV_BIN) --host 127.0.0.1 --port $(MINIKV_PORT) --db $(MINIKV_DB) & echo $$! > /tmp/titankv-minikv.pid
	@sleep 0.5
	@MINIKV_ADDR=$(MINIKV_ADDR) $(GO) run ./cmd/auth & echo $$! > /tmp/titankv-auth.pid
	@MINIKV_ADDR=$(MINIKV_ADDR) $(GO) run ./cmd/data & echo $$! > /tmp/titankv-data.pid
	@$(GO) run ./cmd/meta & echo $$! > /tmp/titankv-meta.pid
	@$(GO) run ./cmd/observability & echo $$! > /tmp/titankv-observ.pid
	@GATEWAY_ADDR=$(GIN_INTERNAL_ADDR) $(GO) run ./cmd/gateway & echo $$! > /tmp/titankv-gin.pid
	@sleep 1
	@$(SKYNET_BIN) --config $(SKYNET_CONFIG) & echo $$! > /tmp/titankv-skynet.pid
	@echo "PIDs saved under /tmp/titankv-*.pid  —  curl http://127.0.0.1:$(PUBLIC_GATEWAY_PORT)/ping"
	@trap 'kill $$(cat /tmp/titankv-*.pid 2>/dev/null) 2>/dev/null; exit 0' INT TERM; \
		while kill -0 $$(cat /tmp/titankv-skynet.pid) 2>/dev/null; do sleep 1; done

# ---------------------------------------------------------
# Frontend (Next.js, Phase 6)
# ---------------------------------------------------------
.PHONY: web-install web-dev web-build web-lint web-test
web-install: ## Install web dependencies
	cd web && npm install

web-dev: ## Run Next.js dev server (port 3000)
	cd web && npm run dev

web-build: ## Build the Next.js admin console
	cd web && npm run build

web-lint: ## Lint frontend
	cd web && npm run lint

web-test: ## Run frontend tests (optional; no test suite yet)
	@echo "web has no unit test script yet; run make web-lint / web-build instead"

# ---------------------------------------------------------
# Top-level aggregated targets
# ---------------------------------------------------------
.PHONY: build test lint proto clean
build: cmake-build go-build ## Build C++ and Go

test: cpp-test go-test ## Run all tests

lint: cpp-lint go-lint web-lint ## Run all linters

proto: ## Generate gRPC + protobuf stubs (Phase 2)
	@echo "proto target is a no-op until Phase 2 defines proto/keyforge/*.proto"

clean: ## Remove all build artifacts
	rm -rf $(CMAKE_BUILD_DIR) web/.next web/node_modules

# ---------------------------------------------------------
# Local dev stack (Docker Compose)
# ---------------------------------------------------------
DEV_COMPOSE := deploy/dev/docker-compose.yml
.PHONY: docker-up docker-down docker-logs
docker-up: ## Start Postgres / Redis / etcd / Jaeger / Prometheus / Grafana
	docker compose -f $(DEV_COMPOSE) up -d

docker-down: ## Stop the local dev stack
	docker compose -f $(DEV_COMPOSE) down

docker-logs: ## Tail compose logs
	docker compose -f $(DEV_COMPOSE) logs -f
