# HAP — Highly Available Pudding
MODULE   := github.com/imevul/hapudding
BIN_DIR  := bin
BINARY   := $(BIN_DIR)/hapudding
ALIAS    := $(BIN_DIR)/hap
CMD      := ./cmd/hapudding
GO       ?= go
COVERAGE ?= coverage.out
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0)
LDFLAGS  := -s -w -X main.version=$(VERSION)

STATUS ?= http://127.0.0.1:9100

.PHONY: all help build install update check verify ci fmt fmt-check tidy tidy-check vet test test-race coverage run clean docker compose-up compose-postgres backends backend-disable backend-enable

all: build

help:
	@echo "Build:"
	@echo "  make build           — $(BINARY) + $(ALIAS) symlink"
	@echo "  make install         — copy hapudding and hap into PREFIX (default /usr/local/bin)"
	@echo "Run:"
	@echo "  make run ARGS='...'  — build and run (default --config configs/hap.yaml)"
	@echo "  make update          — git pull --ff-only, tidy, build; rebuild Compose if it is up"
	@echo "  make compose-up      — docker compose up --build (HAP + Postgres)"
	@echo "  make compose-postgres — alias for compose-up"
	@echo "Verify:"
	@echo "  make check / verify  — fmt-check tidy-check vet test build"
	@echo "  make ci              — same as verify plus test-race"
	@echo "  make fmt / fmt-check — apply / assert gofmt"
	@echo "  make tidy/tidy-check — apply / assert go mod tidy"
	@echo "  make test test-race coverage"
	@echo "Ops (status bind, default STATUS=$(STATUS)):"
	@echo "  make backends                 — GET /hap/backends"
	@echo "  make backend-disable NAME=…   — POST /hap/backends/NAME/disable"
	@echo "  make backend-enable NAME=…    — POST /hap/backends/NAME/enable"

build:
	@mkdir -p $(BIN_DIR)
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) $(CMD)
	ln -sfn hapudding $(ALIAS)

PREFIX ?= /usr/local
install: build
	install -m 0755 $(BINARY) $(PREFIX)/bin/hapudding
	ln -sfn hapudding $(PREFIX)/bin/hap

fmt:
	$(GO) fmt ./...

fmt-check:
	@out="$$(gofmt -l .)"; \
	if [ -n "$$out" ]; then \
	  echo "gofmt needed on:"; echo "$$out"; \
	  echo "run: make fmt"; \
	  exit 1; \
	fi

vet:
	$(GO) vet ./...

tidy:
	$(GO) mod tidy

tidy-check:
	@$(GO) mod tidy
	@git diff --exit-code -- go.mod go.sum || { echo "go.mod/go.sum dirty; run make tidy"; exit 1; }

test:
	$(GO) test ./... -count=1

test-race:
	$(GO) test ./... -race -count=1

coverage:
	$(GO) test ./... -count=1 -coverprofile=$(COVERAGE)

check: fmt-check vet test build
verify: fmt-check tidy-check vet test build
ci: fmt-check tidy-check vet test-race build

update:
	@if git rev-parse --is-inside-work-tree >/dev/null 2>&1 \
	    && git rev-parse --abbrev-ref --symbolic-full-name @{u} >/dev/null 2>&1; then \
	  git pull --ff-only; \
	else \
	  echo "No git upstream; skip pull"; \
	fi
	$(MAKE) tidy build
	@if docker compose ps --status running -q hap 2>/dev/null | grep -q .; then \
	  echo "Rebuilding Compose..."; \
	  docker compose up --build -d; \
	else \
	  echo "No Compose stack running; rebuilt host binary only."; \
	fi

run: build
	$(BINARY) --config $${CONFIG:-configs/hap.yaml} $(ARGS)

clean:
	rm -rf $(BIN_DIR) $(COVERAGE) dist

docker:
	docker build -t hapudding:local .

compose-up:
	docker compose up --build

compose-postgres: compose-up

backends:
	curl -sS $(STATUS)/hap/backends

backend-disable:
	@test -n "$(NAME)" || { echo "NAME is required (make backend-disable NAME=server-a)"; exit 1; }
	curl -sS -X POST $(STATUS)/hap/backends/$(NAME)/disable

backend-enable:
	@test -n "$(NAME)" || { echo "NAME is required (make backend-enable NAME=server-a)"; exit 1; }
	curl -sS -X POST $(STATUS)/hap/backends/$(NAME)/enable
