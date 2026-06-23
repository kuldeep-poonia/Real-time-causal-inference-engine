.PHONY: build test test-unit test-integration lint docker-build docker-run clean

BINARY  := absia
PKG     := ./...
DOCKER  := docker

# ── Build ────────────────────────────────────────────────────────────────────
build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(BINARY) .

# ── Test ─────────────────────────────────────────────────────────────────────
test: test-unit test-integration

test-unit:
	go test -v -race -count=1 $(PKG) -run "^Test[^I]"

test-integration:
	go test -v -race -count=1 ./internal/integration/... ./pkg/orchestrator/... ./pkg/api/...

# ── Lint / vet ───────────────────────────────────────────────────────────────
lint:
	go vet $(PKG)

# ── Docker ───────────────────────────────────────────────────────────────────
docker-build:
	$(DOCKER) build -t absia:latest .

docker-run:
	$(DOCKER) compose up --build

docker-run-with-prometheus:
	$(DOCKER) compose --profile with-prometheus up --build

# ── Clean ────────────────────────────────────────────────────────────────────
clean:
	rm -f $(BINARY)
	$(DOCKER) compose down --volumes 2>/dev/null || true
