.PHONY: build smoke

build:
	go build -o bin/huyang ./cmd/huyang

# Huyang Lua kernel unit tests and the Go service, provider and workspace tests.
# SUITES narrows the run while iterating (lua go). Run it with no SUITES
# before committing.
smoke: build
	bash tests/smoke.sh $(SUITES)
