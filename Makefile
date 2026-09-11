.PHONY: build smoke lint budget check

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

# Full pre-commit gate: lint, then Go tests, then the smoke suites.
check: lint
	go test ./...
	$(MAKE) smoke
