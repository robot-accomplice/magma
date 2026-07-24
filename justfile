# magma developer tasks. Run `just` to list recipes.
# `just ci` mirrors .github/workflows/ci.yml so you can validate locally before pushing.

min_coverage := "80"

# Show available recipes
_default:
    @just --list

# Compile everything
build:
    go build ./...

# Install the magma binary into $GOBIN (~/go/bin by default)
install:
    go install .

# Run magma from source, e.g. `just run ~/code/roboticus roboticus ~/maps`
run *ARGS:
    go run . {{ ARGS }}

# Format the tree in place
fmt:
    gofmt -w .

# Fail if anything is not gofmt-clean (CI gate)
fmt-check:
    #!/usr/bin/env bash
    set -euo pipefail
    unformatted="$(gofmt -l .)"
    if [ -n "$unformatted" ]; then
        echo "not gofmt-clean:"; echo "$unformatted"; exit 1
    fi
    echo "gofmt clean"

# go vet
vet:
    go vet ./...

# golangci-lint (uses .golangci.yml)
lint:
    golangci-lint run ./...

# Run tests with the race detector
test:
    go test ./... -race

# Test with coverage and enforce the {{ min_coverage }}% gate
cover:
    #!/usr/bin/env bash
    set -euo pipefail
    go test ./... -covermode=atomic -coverprofile=coverage.out
    pct="$(go tool cover -func=coverage.out | awk '/^total:/ {print $3}' | tr -d '%')"
    echo "total coverage: ${pct}%  (minimum: {{ min_coverage }}%)"
    awk "BEGIN { exit !(${pct} >= {{ min_coverage }}) }" || {
        echo "coverage ${pct}% is below the {{ min_coverage }}% gate"; exit 1; }

# Open the coverage report in a browser
cover-html: cover
    go tool cover -html=coverage.out

# Full local CI validation — same gates as GitHub Actions
ci: fmt-check vet lint build cover
    @echo "✓ local CI validation passed"

# Tidy module dependencies
tidy:
    go mod tidy

# Cross-compile the release archives into dist/ WITHOUT publishing (dry run of release.yml)
release-dry version="v0.0.0-dev":
    #!/usr/bin/env bash
    set -euo pipefail
    rm -rf dist && mkdir -p dist
    for t in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do
        os="${t%/*}"; arch="${t#*/}"
        bin="magma"; [ "$os" = "windows" ] && bin="magma.exe"
        echo "==> ${os}/${arch}"
        CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags "-s -w" -o "dist/${bin}" .
        base="magma_{{ version }}_${os}_${arch}"
        if [ "$os" = "windows" ]; then (cd dist && zip -q "${base}.zip" "$bin" && rm "$bin")
        else (cd dist && tar -czf "${base}.tar.gz" "$bin" && rm "$bin"); fi
    done
    (cd dist && sha256sum ./* > checksums.txt)
    ls -la dist

# Remove build and coverage artifacts
clean:
    rm -rf dist coverage.out
