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

.PHONY: all help build install check verify ci fmt fmt-check tidy tidy-check vet test test-race coverage run clean docker compose-up compose-postgres

all: build

help:
	@echo "Build:"
	@echo "  make build           — $(BINARY) + $(ALIAS) symlink"
	@echo "  make install         — copy hapudding and hap into PREFIX (default /usr/local/bin)"
	@echo "Run:"
	@echo "  make run ARGS='...'  — build and run (default --config configs/hap.example.yaml)"
	@echo "  make compose-up      — docker compose up --build (SQLite)"
	@echo "  make compose-postgres — docker compose --profile postgres up --build"
	@echo "Verify:"
	@echo "  make check / verify  — fmt-check tidy-check vet test build"
	@echo "  make ci              — same as verify plus test-race"
	@echo "  make fmt / fmt-check — apply / assert gofmt"
	@echo "  make tidy/tidy-check — apply / assert go mod tidy"
	@echo "  make test test-race coverage"

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

run: build
	$(BINARY) --config $${CONFIG:-configs/hap.example.yaml} $(ARGS)

clean:
	rm -rf $(BIN_DIR) $(COVERAGE) dist

docker:
	docker build -t hapudding:local .

compose-up:
	docker compose up --build

compose-postgres:
	docker compose --profile postgres up --build
