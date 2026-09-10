.PHONY: build smoke

build:
	go build -o bin/huyang ./cmd/huyang

# Huyang service, adapter, provider, Lua kernel, and transactional workspace gates.
# SUITES narrows the run while iterating (unit mcp headless multi debug).
# Run it with no SUITES before committing.
smoke: build
	bash tests/smoke.sh $(SUITES)
