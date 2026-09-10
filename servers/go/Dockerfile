# Build stage: compile against the real HDF5 C library (gonexus uses cgo).
FROM golang:1.22-bookworm AS build
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

# Runtime stage. In principle only the shared HDF5 runtime libs (not the
# -dev headers) are needed here, but the exact runtime package name has
# shifted across Debian/Ubuntu releases (e.g. libhdf5-103-1 vs
# libhdf5-103-1t64), so we pull in libhdf5-dev again for a name that's
# stable across releases; swap in your distro's slimmer runtime package
# if you want a smaller final image.
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    libhdf5-dev ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/nexus-s3-server /usr/local/bin/nexus-s3-server

ENV PORT=8080
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/nexus-s3-server"]
