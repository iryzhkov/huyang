.PHONY: build smoke e2e

# Compile the compatibility bridge and the standalone Huyang service/adapter.
build:
	go build -o bin/agent99-bridge ./cmd/agent99-bridge
	go build -o bin/huyang ./cmd/huyang

# Bridge + LSP tools against a headless Neovim (no API calls, free).
# SUITES narrows the run while iterating (unit mcp headless multi debug),
# and the headless suite narrows further, to one family of tools:
#   make smoke SUITES=headless
#   make smoke SUITES=headless:edit
# Run it with no SUITES before committing.
smoke: build
	bash tests/smoke.sh $(SUITES)

# One real agent edit through the configured provider (needs DEEPSEEK_API_KEY).
e2e: build
	bash tests/e2e.sh
