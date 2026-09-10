# Makefile for servers/go (nexus-s3-server)
#
# Common usage:
#   make deps         # one-time: install libhdf5-dev (Linux) via apt, or
#                      # print instructions for other platforms
#   make build        # build bin/nexus-s3-server
#   make run          # build + run it (reads S3_* / AWS_* env vars, see below)
#   make fmt          # gofmt -w on this directory's Go source
#   make vet          # go vet on this directory
#   make check        # fmt + vet + build
#   make docker-build # build the Docker image (context = repo root)
#   make docker-run   # run the Docker image
#   make smoke        # spin up a local moto S3 mock, upload a test file,
#                      # run the server against it, and curl every endpoint
#   make clean        # remove build artifacts
#
# Configuration (all read from the environment at `make run` / `make
# docker-run` time, not baked into the Makefile - see ../README.md for
# the full list):
#   S3_BUCKET (required), S3_PREFIX, S3_ENDPOINT, S3_REGION,
#   S3_FORCE_PATH_STYLE, NEXUS_CACHE_DIR, PORT, plus normal AWS
#   credential env vars (AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY / ...).

GO       ?= go
BIN_DIR  := bin
BINARY   := $(BIN_DIR)/nexus-s3-server
PKG      := .
REPO_ROOT:= ../..

IMAGE    ?= nexus-s3-server-go
PORT     ?= 8080

# HDF5 is a cgo dependency of gonexus; its headers/libs often aren't on
# the default search path, so detect common package managers and point
# CGO at them automatically (mirrors ../../Makefile).
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
else ifeq ($(UNAME_S),Linux)
  ifneq ($(wildcard /usr/include/hdf5/serial/hdf5.h),)
    # Debian/Ubuntu's libhdf5-dev installs under a /serial subdirectory
    export CGO_CFLAGS  := -I/usr/include/hdf5/serial $(CGO_CFLAGS)
    export CGO_LDFLAGS := -L/usr/lib/x86_64-linux-gnu/hdf5/serial $(CGO_LDFLAGS)
  endif
endif

.PHONY: all
all: build

# ---------------------------------------------------------------------------
# System dependencies
# ---------------------------------------------------------------------------
.PHONY: deps
deps:
	$(MAKE) -C $(REPO_ROOT) deps

# ---------------------------------------------------------------------------
# Build / run
# ---------------------------------------------------------------------------
.PHONY: build
build:
	mkdir -p $(BIN_DIR)
	$(GO) build -o $(BINARY) $(PKG)

.PHONY: run
run: build
	./$(BINARY)

.PHONY: fmt
fmt:
	gofmt -l -w $$(find . -name '*.go')

.PHONY: vet
vet:
	$(GO) vet $(PKG)

.PHONY: check
check: fmt vet build

# ---------------------------------------------------------------------------
# Docker
# ---------------------------------------------------------------------------
.PHONY: docker-build
docker-build:
	docker build -f Dockerfile -t $(IMAGE) $(REPO_ROOT)

.PHONY: docker-run
docker-run:
	@if [ -z "$(S3_BUCKET)" ]; then echo "S3_BUCKET is required, e.g. make docker-run S3_BUCKET=nexus-data"; exit 1; fi
	docker run --rm -p $(PORT):8080 \
		-e S3_BUCKET=$(S3_BUCKET) \
		-e S3_PREFIX=$(S3_PREFIX) \
		-e S3_ENDPOINT=$(S3_ENDPOINT) \
		-e S3_REGION=$(S3_REGION) \
		-e S3_FORCE_PATH_STYLE=$(S3_FORCE_PATH_STYLE) \
		-e AWS_ACCESS_KEY_ID=$(AWS_ACCESS_KEY_ID) \
		-e AWS_SECRET_ACCESS_KEY=$(AWS_SECRET_ACCESS_KEY) \
		-e AWS_SESSION_TOKEN=$(AWS_SESSION_TOKEN) \
		$(IMAGE)

# ---------------------------------------------------------------------------
# Local smoke test against a moto S3 mock (no real AWS account needed)
#
#   make smoke NXS_FILE=/path/to/your.nxs
#
# Requires `pip install 'moto[server]'` and curl. Uses port 5099 for the
# mock S3 and 8099 for the server so it won't collide with anything real.
# ---------------------------------------------------------------------------
SMOKE_S3_PORT    := 5099
SMOKE_HTTP_PORT  := 8099
SMOKE_BUCKET     := nexus-smoke-test
SMOKE_KEY        := smoke/test.nxs
NXS_FILE         ?=

.PHONY: smoke
smoke: build
	@if [ -z "$(NXS_FILE)" ]; then echo "usage: make smoke NXS_FILE=/path/to/your.nxs"; exit 1; fi
	@echo "--- starting moto S3 mock on :$(SMOKE_S3_PORT) ---"
	moto_server -p $(SMOKE_S3_PORT) >/tmp/nexus-smoke-moto.log 2>&1 & echo $$! > /tmp/nexus-smoke-moto.pid
	@sleep 2
	AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test python3 -c "\
import boto3; \
s3 = boto3.client('s3', endpoint_url='http://127.0.0.1:$(SMOKE_S3_PORT)', region_name='us-east-1'); \
s3.create_bucket(Bucket='$(SMOKE_BUCKET)'); \
s3.upload_file('$(NXS_FILE)', '$(SMOKE_BUCKET)', '$(SMOKE_KEY)')"
	@echo "--- starting server on :$(SMOKE_HTTP_PORT) ---"
	S3_BUCKET=$(SMOKE_BUCKET) S3_ENDPOINT=http://127.0.0.1:$(SMOKE_S3_PORT) \
		S3_FORCE_PATH_STYLE=true AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test \
		AWS_REGION=us-east-1 PORT=$(SMOKE_HTTP_PORT) \
		./$(BINARY) >/tmp/nexus-smoke-server.log 2>&1 & echo $$! > /tmp/nexus-smoke-server.pid
	@sleep 2
	@echo "--- /healthz ---"; curl -sf http://127.0.0.1:$(SMOKE_HTTP_PORT)/healthz; echo
	@echo "--- /files ---"; curl -sf http://127.0.0.1:$(SMOKE_HTTP_PORT)/files; echo
	@echo "--- /tree ---"; curl -sf "http://127.0.0.1:$(SMOKE_HTTP_PORT)/tree?key=$(SMOKE_KEY)"; echo
	@$(MAKE) smoke-stop

.PHONY: smoke-stop
smoke-stop:
	-kill `cat /tmp/nexus-smoke-server.pid 2>/dev/null` 2>/dev/null
	-kill `cat /tmp/nexus-smoke-moto.pid 2>/dev/null` 2>/dev/null
	-rm -f /tmp/nexus-smoke-server.pid /tmp/nexus-smoke-moto.pid

# ---------------------------------------------------------------------------
# Housekeeping
# ---------------------------------------------------------------------------
.PHONY: clean
clean:
	rm -rf $(BIN_DIR)

.PHONY: help
help:
	@echo "Targets:"
	@echo "  deps         install the HDF5 C library needed to build gonexus"
	@echo "  build        build $(BINARY)"
	@echo "  run          build + run the server (reads S3_*/AWS_* env vars)"
	@echo "  fmt          gofmt -w on this directory's .go files"
	@echo "  vet          go vet on this directory"
	@echo "  check        fmt + vet + build"
	@echo "  docker-build build the Docker image (context: repo root)"
	@echo "  docker-run   run the Docker image (needs S3_BUCKET=...)"
	@echo "  smoke        run against a local moto S3 mock (needs NXS_FILE=...)"
	@echo "  clean        remove build artifacts"
