.PHONY: build smoke lint budget live check

build:
	go build -o bin/huyang ./cmd/huyang

# Huyang Lua kernel unit tests and the Go service, provider and workspace tests.
# SUITES narrows the run while iterating (lua go). Run it with no SUITES
# before committing.
smoke: build
	bash tests/smoke.sh $(SUITES)

# gofmt, go vet, function-length check, and staticcheck/golangci-lint if present.
# LINT_STRICT=0 reports without failing; LINT_MAX_FUNC_LINES overrides the 80-line limit.
LINT_STRICT ?= 1
LINT_MAX_FUNC_LINES ?= 80
lint:
	LINT_STRICT=$(LINT_STRICT) LINT_MAX_FUNC_LINES=$(LINT_MAX_FUNC_LINES) bash tests/lint.sh

# Service RSS, state-directory and repository size budget against docs/plans/budget/baseline.json.
budget: build
	bash tests/budget.sh

# The service boundary: the daemon as its own process, reached through the
# huyang mcp adapter over a socket, against the fixture repositories. It starts
# processes and language servers, so it is tagged rather than part of go test
# ./... - but it is the only gate that sees what a client actually receives.
live: build
	go test -tags live -count=1 ./internal/livetest/

# Full pre-commit gate: lint, then Go tests, then the smoke suites, then the
# live service boundary.
check: lint
	go test ./...
	$(MAKE) smoke
	$(MAKE) live
