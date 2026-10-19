.PHONY: build demo test lint check test-gate commit-check pr-check package-check install-test-gate release

# Keep TLS behavior consistent across local, CI, and release builds.
export GOTOOLCHAIN := go1.26.9

build:
	go build -trimpath -buildvcs=false -o bin/my-code-dashboard ./cmd/my-code-dashboard

demo: build
	./bin/my-code-dashboard --demo --open

test:
	go test -race -count=1 ./...

lint:
	@test -z "$$(gofmt -l cmd internal scripts)" || { gofmt -l cmd internal scripts; exit 1; }
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 run ./...

check: test lint
	go vet ./...
	go mod tidy -diff
	go mod verify
	go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
	shasum -a 256 -c scripts/browser-assets.sha256

test-gate commit-check:
	bash scripts/test-gate.sh

pr-check:
	bash scripts/pr-check.sh

package-check: release
	go run ./scripts/packagecheck -version 0.1.0-dev

install-test-gate:
	bash scripts/install-test-gate.sh

release:
	bash scripts/release.sh 0.1.0-dev
