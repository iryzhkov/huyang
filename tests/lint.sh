#!/usr/bin/env bash
# Huyang lint gate.
#
# Usage: make lint                          strict: any finding fails
#        make lint LINT_STRICT=0            report only, always exits 0
#        make lint LINT_MAX_FUNC_LINES=120  raise the function-length threshold
#
# Stages, in order:
#   1. gofmt -l over tracked Go files; any listed file fails.
#   2. go vet ./...
#   3. function-length check: every Go function outside _test.go files longer
#      than LINT_MAX_FUNC_LINES (default 80) lines is printed as file:line and
#      length. Length counts from the `func` line to the closing `}` in column 0.
#   4. staticcheck or golangci-lint when one is already on PATH. Nothing is ever
#      downloaded; a missing tool prints one skip line.
set -uo pipefail

: "${LINT_MAX_FUNC_LINES:=80}"
: "${LINT_STRICT:=1}"

REPO="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO"
STATUS=0
fail() { echo "lint: $1"; STATUS=1; }

GO_FILES="$(git ls-files '*.go')"
GO_SOURCE_FILES="$(echo "$GO_FILES" | grep -v '_test\.go$')"

echo "lint: gofmt"
unformatted="$(echo "$GO_FILES" | xargs gofmt -l)"
if [ -n "$unformatted" ]; then echo "$unformatted"; fail "gofmt: files need formatting"; fi

echo "lint: go vet"
go vet ./... || fail "go vet reported problems"

echo "lint: functions over $LINT_MAX_FUNC_LINES lines (non-test Go files)"
offenders="$(echo "$GO_SOURCE_FILES" | xargs awk -v max="$LINT_MAX_FUNC_LINES" '
  /^func / { start = FNR; name = $0 }
  /^}/ && start {
    length_lines = FNR - start + 1
    if (length_lines > max) printf "%s:%d %d lines\n", FILENAME, start, length_lines
    start = 0
  }')"
if [ -n "$offenders" ]; then
  echo "$offenders"
  fail "$(echo "$offenders" | wc -l) function(s) over $LINT_MAX_FUNC_LINES lines"
else
  echo "lint: no functions over $LINT_MAX_FUNC_LINES lines"
fi

if command -v staticcheck >/dev/null; then
  echo "lint: staticcheck"
  staticcheck ./... || fail "staticcheck reported problems"
elif command -v golangci-lint >/dev/null; then
  echo "lint: golangci-lint"
  golangci-lint run ./... || fail "golangci-lint reported problems"
else
  echo "lint: staticcheck and golangci-lint not on PATH; extra linter skipped"
fi

if [ "$STATUS" -ne 0 ] && [ "$LINT_STRICT" != 1 ]; then
  echo "huyang lint: findings reported (LINT_STRICT=0, not failing)"
  exit 0
fi
[ "$STATUS" -eq 0 ] && echo "huyang lint: OK" || echo "huyang lint: FAILED"
exit "$STATUS"
