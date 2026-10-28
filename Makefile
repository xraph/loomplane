.PHONY: help up down stack dev dev-api dev-dashboard build build-api build-dashboard run vuln fuzz build-cli build-agent build-qualify \
	image helm-lint k8s-manifest test-kubernetes test test-go test-dashboard test-integration acceptance acceptance-infrastructure acceptance-catalog lint lint-go lint-dashboard \
	lint-fix fmt fmt-go fmt-dashboard vet gen-client check-client setup clean \
	h b r t ti c f l lf v d

.DEFAULT_GOAL := help

API_PORT      ?= 8780
DASHBOARD_DIR := apps/dashboard
VERSION       ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS       := -ldflags "-s -w -X github.com/xraph/loomplane/internal/buildinfo.Version=$(VERSION)"
COMPOSE       := docker compose -f deployments/docker-compose/docker-compose.yml
# The compose Postgres. The integration tests create a database of their
# own on it for each run.
TEST_DATABASE_URL ?= postgres://loomplane:loomplane@localhost:55532/loomplane?sslmode=disable

help: ## List the targets
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
	@echo
	@echo "  Shortcuts: h help, b build, r run, t test, ti test-integration, c clean,"
	@echo "  f fmt, l lint, lf lint-fix, v vet, d dev"

setup: ## Install Go modules and the dashboard's packages
	go mod download
	pnpm --dir $(DASHBOARD_DIR) install

up: ## Start Postgres in Docker
	$(COMPOSE) up -d --wait

stack: ## Build the image and run all of Loomplane in Docker on LOOMPLANE_PORT (8780)
	$(COMPOSE) --profile app up -d --build --wait

down: ## Stop everything compose started (the data volume stays)
	$(COMPOSE) --profile app down

dev: ## Run the API (forge dev, hot reload), the analysis worker and the dashboard (Vite)
	@API_PORT=$(API_PORT) DASHBOARD_DIR=$(DASHBOARD_DIR) bash scripts/dev.sh

dev-api: ## Run only the API with hot reload
	forge dev -a loomplane -p $(API_PORT)

dev-dashboard: ## Run only the dashboard's dev server, proxying the API
	LOOMPLANE_API_URL=http://localhost:$(API_PORT) pnpm --dir $(DASHBOARD_DIR) dev

build: build-dashboard build-api build-cli build-agent ## Build the dashboard, API, worker, agent and loomctl

build-api: ## Build bin/loomplane and bin/loomplane-worker
	go build $(LDFLAGS) -o bin/loomplane ./cmd/loomplane
	go build $(LDFLAGS) -o bin/loomplane-worker ./cmd/loomplane-worker

build-agent: ## Build the destination observer and signed-policy receiver
	go build $(LDFLAGS) -o bin/loomplane-agent ./cmd/loomplane-agent

build-cli: ## Build bin/loomctl, the command-line client
	go build $(LDFLAGS) -o bin/loomctl ./cmd/loomctl

build-qualify: ## Build bin/loomplane-qualify, the model qualification job (its run command costs money)
	go build $(LDFLAGS) -o bin/loomplane-qualify ./cmd/loomplane-qualify

build-dashboard: ## Build the dashboard into apps/dashboard/dist
	pnpm --dir $(DASHBOARD_DIR) build

run: build ## Build everything and run the API on API_PORT, with the analysis worker
	@API_PORT=$(API_PORT) bash scripts/run.sh

image: ## Build the container image, tagged with the git version
	docker build -f deployments/docker/Dockerfile --build-arg VERSION=$(VERSION) -t loomplane:$(VERSION) .

helm-lint: ## Lint the Helm chart and validate what it renders (needs helm and kubeconform)
	bash scripts/check-helm.sh

k8s-manifest: ## Render deployments/kubernetes/loomplane.yaml from the Helm chart
	bash scripts/render-k8s-manifest.sh > deployments/kubernetes/loomplane.yaml

test-kubernetes: ## Install the chart in a kind cluster and check it stays up through pod and node loss
	docker build -f deployments/docker/Dockerfile --build-arg VERSION=$(VERSION) -t loomplane:k8s-test .
	bash scripts/test-kubernetes.sh loomplane:k8s-test

test: test-go test-dashboard ## Run the Go and dashboard unit tests

test-go: ## Run the Go unit tests
	go test ./...

test-dashboard: ## Run the dashboard's unit tests
	pnpm --dir $(DASHBOARD_DIR) test

test-integration: ## Boot the whole app against the compose Postgres (make up first)
	LOOMPLANE_TEST_DATABASE_URL='$(TEST_DATABASE_URL)' go test -tags=integration -count=1 -timeout=60m ./tests/integration/

acceptance: ## Deploy to real clouds and take it down again; costs money (docs/acceptance.md)
	LOOMPLANE_TEST_DATABASE_URL='$(TEST_DATABASE_URL)' go test -tags='integration acceptance' -count=1 -timeout=180m -v \
		-run TestAcceptance ./tests/integration/

acceptance-infrastructure: ## Verify SSH/Docker and kind with isolated targets, then remove them
	python3 -m unittest discover -s tests/acceptance/infrastructure -p 'test_*.py'
	LOOMPLANE_TEST_DATABASE_URL='$(TEST_DATABASE_URL)' python3 tests/acceptance/infrastructure/run.py

acceptance-catalog: ## Deploy a test-only composed catalog kind to isolated SSH/Docker and kind targets, then remove them
	python3 -m unittest discover -s tests/acceptance/infrastructure -p 'test_*.py'
	LOOMPLANE_TEST_DATABASE_URL='$(TEST_DATABASE_URL)' python3 tests/acceptance/infrastructure/run.py --suite catalog

lint: lint-go lint-dashboard ## Lint Go and the dashboard

lint-go: ## golangci-lint, integration tests included
	golangci-lint run --build-tags=integration ./...

lint-fix: ## Lint Go and the dashboard, applying the fixes each linter can make
	golangci-lint run --fix --build-tags=integration ./...
	pnpm --dir $(DASHBOARD_DIR) lint --fix

fmt: fmt-go fmt-dashboard ## Format Go and the dashboard

fmt-go: ## gofmt and goimports, with the settings in .golangci.yml
	golangci-lint fmt ./...

fmt-dashboard: ## Prettier over the dashboard's TypeScript
	pnpm --dir $(DASHBOARD_DIR) format

vet: ## go vet, integration tests included
	go vet -tags=integration ./...

vuln: ## Check Loomplane and its dependencies for known vulnerabilities it reaches
	go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...

FUZZTIME ?= 30s

fuzz: ## Fuzz the parsers that read what users and repositories send, FUZZTIME each (30s)
	@grep -rl --include='*_test.go' '^func Fuzz' internal pkg examples | while read -r f; do \
		d=$$(dirname "$$f"); \
		for t in $$(grep -o '^func Fuzz[A-Za-z]*' "$$f" | sed 's/func //'); do \
			echo "fuzz $$t in ./$$d"; \
			go test -run '^$$' -fuzz "^$$t$$" -fuzztime $(FUZZTIME) "./$$d" || exit 1; \
		done; \
	done

lint-dashboard: ## Type-check and lint the dashboard
	pnpm --dir $(DASHBOARD_DIR) typecheck
	pnpm --dir $(DASHBOARD_DIR) lint

gen-client: ## Refresh the API specs from the running API, then regenerate the dashboard's clients
	pnpm --dir $(DASHBOARD_DIR) gen:specs
	pnpm --dir $(DASHBOARD_DIR) gen:client

check-client: ## Fail if the generated clients drifted from the specs
	pnpm --dir $(DASHBOARD_DIR) check:client

clean: ## Remove build output
	rm -rf bin $(DASHBOARD_DIR)/dist .forge

# Short aliases. Each forwards to the long target, which carries the recipe.
h: help
b: build
r: run
t: test
ti: test-integration
c: clean
f: fmt
l: lint
lf: lint-fix
v: vet
d: dev
