.PHONY: help build demo test lint check test-gate commit-check pr-check package-check install-test-gate release docker-build docker-check docker-up

# Keep TLS behavior consistent across local, CI, and release builds.
export GOTOOLCHAIN := go1.26.10

# Plain `make` lists the targets; each target documents itself with `## text`.
.DEFAULT_GOAL := help

help: ## List the available targets
	@awk 'BEGIN {FS = ":.*## "; print "Usage: make <target>\n"} /^[a-zA-Z_ -]+:.*## / {printf "  %-24s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build bin/my-code-dashboard with the pinned Go toolchain
	go build -trimpath -buildvcs=false -o bin/my-code-dashboard ./cmd/my-code-dashboard

demo: build ## Build and open the synthetic demo (no credentials or provider calls)
	./bin/my-code-dashboard --demo --open

test: ## Run all tests with the race detector, uncached
	go test -race -count=1 ./...

lint: ## Check gofmt and run golangci-lint
	@test -z "$$(gofmt -l cmd internal scripts)" || { gofmt -l cmd internal scripts; exit 1; }
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 run ./...

check: test lint ## Run tests, lint, vet, module checks, govulncheck and asset checksums
	go vet ./...
	go mod tidy -diff
	go mod verify
	go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
	shasum -a 256 -c scripts/browser-assets.sha256

test-gate commit-check: ## Run build and check on an export of the staged files
	bash scripts/test-gate.sh

pr-check: ## Check the staged files: manifest, ShellCheck, actionlint, Gitleaks
	bash scripts/pr-check.sh

package-check: release ## Build and verify the release archives
	go run ./scripts/packagecheck -version 0.2.0-dev

install-test-gate: ## Install the repository pre-push hook
	bash scripts/install-test-gate.sh

release: ## Prepare release archives in dist/ (publishes nothing)
	bash scripts/release.sh 0.2.0-dev

docker-build: ## Build the my-code-dashboard:local image
	docker build --build-arg VERSION=0.2.0-dev --tag my-code-dashboard:local .

docker-check: ## Build and smoke-test the container image (requires Docker)
	bash scripts/docker-check.sh

docker-up: ## Start the Compose service and open it in the browser
	bash scripts/docker-up.sh
