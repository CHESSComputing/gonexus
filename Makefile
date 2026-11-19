# Makefile for gonexus
#
# Common usage:
#   make deps     # one-time: install libhdf5-dev (Linux) via apt, or print
#                 # instructions for other platforms
#   make tidy     # generate/update go.sum (fixes "missing go.sum entry")
#   make build    # go build ./...
#   make test     # go test ./...
#   make examples # build and run examples
#   make fmt      # gofmt -w on all Go source
#   make vet      # go vet ./...
#   make clean    # remove build artifacts

GO       ?= go
MODULE   := $(shell $(GO) list -m 2>/dev/null)
PKGS     := ./...
BIN_DIR  := bin

# HDF5 is a cgo dependency; its headers/libs often aren't on the default
# search path, so detect common macOS package managers and point CGO at
# them automatically.
UNAME_S := $(shell uname -s)
ifeq ($(UNAME_S),Darwin)
  ifneq ($(wildcard /opt/local/include/hdf5.h),)
    # MacPorts
    export CGO_CFLAGS  := -I/opt/local/include $(CGO_CFLAGS)
    export CGO_LDFLAGS := -L/opt/local/lib $(CGO_LDFLAGS)
  else
    HDF5_PREFIX := $(shell brew --prefix hdf5 2>/dev/null)
    ifneq ($(HDF5_PREFIX),)
      # Homebrew
      export CGO_CFLAGS  := -I$(HDF5_PREFIX)/include $(CGO_CFLAGS)
      export CGO_LDFLAGS := -L$(HDF5_PREFIX)/lib $(CGO_LDFLAGS)
    endif
  endif
endif

.PHONY: all
all: build changes

# ---------------------------------------------------------------------------
# System dependencies (the HDF5 C library gonum.org/v1/hdf5 links against)
# ---------------------------------------------------------------------------
.PHONY: deps
deps:
	@echo "gonexus needs the HDF5 C library (>=1.8) and its headers to build."
	@if command -v port >/dev/null 2>&1; then \
		echo "Detected MacPorts - installing hdf5..."; \
		sudo port install hdf5; \
	elif command -v brew >/dev/null 2>&1; then \
		echo "Detected Homebrew (macOS) - installing hdf5..."; \
		brew install hdf5; \
	elif command -v apt-get >/dev/null 2>&1; then \
		echo "Detected apt (Debian/Ubuntu) - installing libhdf5-dev..."; \
		sudo apt-get update && sudo apt-get install -y libhdf5-dev; \
	elif command -v yum >/dev/null 2>&1; then \
		echo "Detected yum (RHEL/CentOS/Fedora) - installing hdf5-devel..."; \
		sudo yum install -y hdf5-devel; \
	else \
		echo "Could not detect a package manager. Install HDF5 >=1.8 manually,"; \
		echo "e.g. from https://www.hdfgroup.org/downloads/hdf5, then re-run make."; \
		exit 1; \
	fi

# ---------------------------------------------------------------------------
# Go module bookkeeping
# ---------------------------------------------------------------------------
.PHONY: tidy
tidy:
	$(GO) mod tidy

.PHONY: verify
verify:
	$(GO) mod verify

# ---------------------------------------------------------------------------
# Build / test / lint
# ---------------------------------------------------------------------------
.PHONY: build
build: tidy
	$(GO) build $(PKGS)

.PHONY: test
test: tidy
	$(GO) test -v $(PKGS)

.PHONY: vet
vet: tidy
	$(GO) vet $(PKGS)

.PHONY: fmt
fmt:
	gofmt -l -w $$(find . -name '*.go' -not -path './vendor/*')

.PHONY: check
check: fmt vet test

# ---------------------------------------------------------------------------
# Example program
# ---------------------------------------------------------------------------
.PHONY: examples
examples: tidy
	mkdir -p $(BIN_DIR)
	$(GO) build -o $(BIN_DIR)/gonexus-io-example ./examples/io
	$(GO) build -o $(BIN_DIR)/gonexus-reader-example ./examples/reader

changes:
	./changes.sh
	./last_changes.sh

# ---------------------------------------------------------------------------
# Housekeeping
# ---------------------------------------------------------------------------
.PHONY: clean
clean:
	$(GO) clean $(PKGS)
	rm -rf $(BIN_DIR) example.nxs

.PHONY: help
help:
	@echo "Targets:"
	@echo "  deps     install the HDF5 C library needed to build gonexus"
	@echo "  tidy     go mod tidy (generates/updates go.sum)"
	@echo "  build    go build ./..."
	@echo "  test     go test ./..."
	@echo "  vet      go vet ./..."
	@echo "  fmt      gofmt -w on all .go files"
	@echo "  check    fmt + vet + test"
	@echo "  examples build examples executables"
	@echo "  clean    remove build artifacts"
