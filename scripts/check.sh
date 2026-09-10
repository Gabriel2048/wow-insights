#!/usr/bin/env bash
# The same gate CI runs, so a red build is something you find here first.
# Keep this and .github/workflows/ci.yml in step: if you add a check to one,
# add it to the other.
set -euo pipefail
cd "$(dirname "$0")/.."

step() { printf '\n\033[1m==> %s\033[0m\n' "$1"; }

step "gofmt"
unformatted=$(gofmt -l .)
if [ -n "$unformatted" ]; then echo "not gofmt'd:"; echo "$unformatted"; exit 1; fi

step "go vet"
go vet ./...

step "go fix (modernizers)"
pending=$(go fix -diff ./...)
if [ -n "$pending" ]; then
  echo "pending modernizations — run 'go fix ./...':"; echo "$pending"; exit 1
fi

step "golangci-lint"
# CI runs the prebuilt binary of this same version; `go run` compiles it
# with the local toolchain instead. Keep the version in step with
# .github/workflows/ci.yml -- see the note there about the built-with
# constraint, which only the prebuilt binary can trip.
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2 run ./...

step "test"
go test -race -shuffle=on -covermode=atomic -coverprofile=cover.out ./...
go tool cover -func=cover.out | tail -1

step "govulncheck"
go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...

step "build"
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /tmp/wowinsight .

step "no third-party dependencies"
mods=$(go list -m all)
if [ "$mods" != "wowinsight" ]; then
  echo "expected exactly one module, got:"; echo "$mods"; exit 1
fi

printf '\n\033[32mall checks passed\033[0m\n'
