# Build stage: compile against the real HDF5 C library (gonexus uses cgo).
FROM golang:1.24-bookworm AS build
RUN apt-get update && apt-get install -y --no-install-recommends \
    libhdf5-dev pkg-config \
    && rm -rf /var/lib/apt/lists/*
ENV CGO_CFLAGS="-I/usr/include/hdf5/serial" \
    CGO_LDFLAGS="-L/usr/lib/x86_64-linux-gnu/hdf5/serial"

WORKDIR /src
COPY go.mod go.sum ./
COPY gonexus/ ./gonexus/
COPY servers/go/ ./servers/go/
RUN go build -o /out/nexus-s3-server ./servers/go/

# Runtime stage: Use slim runtime shared libraries instead of -dev headers
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    libhdf5-103-1 \
    libhdf5-hl-100 \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/nexus-s3-server /usr/local/bin/nexus-s3-server

ENV PORT=8081
EXPOSE 8081
ENTRYPOINT ["/usr/local/bin/nexus-s3-server"]
