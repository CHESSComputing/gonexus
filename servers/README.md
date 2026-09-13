# NeXus-over-S3 HTTP servers

Two equivalent HTTP servers — one in Go (`go/`, built on the `gonexus`
library in this repo), one in Python (`py/`, built on `h5py`) — that serve
NeXus/HDF5 files stored in an S3 bucket over a small, JSON-based REST API.

Both implementations expose **the same endpoints, query parameters, and
response shapes**, so a client can talk to either one interchangeably.
Pick whichever fits your deployment; the Go server produces a single
static-ish binary, the Python server is easier to patch/inspect ad hoc.

## Why a server at all?

HDF5 (and therefore NeXus) files must be opened from a real path on a
local filesystem — the C library underneath both `gonexus` and `h5py`
does not read from an arbitrary byte stream. So instead of asking every
client to download a whole `.nxs` file from S3 before it can even list
what's in it, each server:

1. Lists candidate objects in the bucket (`*.nxs`, `*.h5`, `*.hdf5`).
2. On first request for a given object, downloads it once to a local
   disk cache, keyed by the object's S3 ETag (so a changed object in S3
   is transparently re-downloaded, and an unchanged one is never
   re-fetched).
3. Parses the cached local copy with `gonexus`/`h5py` and serves tree
   metadata and field data as JSON (or, for `/tree`, as plain text).

## API

All endpoints are `GET`. `key` is always the S3 object key of the NeXus
file being queried (e.g. `chess/edd_test.nxs`); `path` is a `/`-separated
path *inside* that file (e.g. `entry/data/counts`), matching the paths
you'd see printed by `examples/reader`.

| Endpoint   | Query params                    | Returns |
|------------|----------------------------------|---------|
| `/healthz` | –                                 | `{"status":"ok"}` |
| `/files`   | `prefix` (optional)               | List of NeXus objects under the bucket/prefix |
| `/tree`    | `key`                             | Full tree, `text/plain`, in the same format `gonexus.Tree()` / the reader examples print |
| `/entries` | `key`                             | Top-level groups (e.g. `NXentry`s) directly under root |
| `/group`   | `key`, `path` (optional, default root) | A group's attributes and direct children |
| `/field`   | `key`, `path`, `offset`, `limit`  | A field's dtype/shape/units/attrs plus (possibly sliced) data |

### Examples

```bash
curl "$HOST/files"
curl "$HOST/tree?key=chess/edd_test.nxs"
curl "$HOST/entries?key=chess/edd_test.nxs"
curl "$HOST/group?key=chess/edd_test.nxs&path=testflight-0212-b_dataset1/data"
curl "$HOST/field?key=chess/edd_test.nxs&path=testflight-0212-b_dataset1/data/labx"

# Large fields: page through them instead of pulling the whole array
curl "$HOST/field?key=chess/edd_test.nxs&path=.../intensity&offset=0&limit=100"
```

`/field` response shape:

```json
{
  "key": "chess/edd_test.nxs",
  "path": "testflight-0212-b_dataset1/data/labx",
  "dtype": "float64",
  "shape": [3],
  "units": "mm",
  "attrs": {"long_name": "labx (mm)"},
  "offset": 0,
  "count": 3,
  "total": 3,
  "truncated": false,
  "values": [0.12, 0.45, 0.81]
}
```

`offset`/`limit` slice along the first axis only (rows of a 2-D field, for
example the `intensity = float64(3x1807)` array in the EDD example file);
this keeps responses for big per-scan-point arrays bounded without
requiring the whole array to be read or serialized at once. A field
response is `"truncated": true` whenever fewer than `total` elements were
returned, so a client knows to keep paging with a larger `offset`.

Errors are always `{"error": "..."}` with a matching HTTP status
(`400` for a bad/missing parameter, `404` when the key/path doesn't
exist, `500` for anything else, e.g. an S3 or HDF5 failure).

## Configuration

Both servers read the same environment variables:

| Variable | Required | Default | Meaning |
|---|---|---|---|
| `S3_BUCKET` | yes | – | Bucket to serve NeXus files from |
| `S3_PREFIX` | no | `""` | Default key prefix for `/files` |
| `S3_ENDPOINT` | no | AWS default | Custom S3-compatible endpoint, e.g. MinIO |
| `S3_REGION` / `AWS_REGION` | no | `us-east-1` | S3 region |
| `S3_FORCE_PATH_STYLE` | no | `false` | Set `true` for most non-AWS S3 (MinIO, etc.) |
| S3_TLS_INSECURE_SKIP_VERIFY | no | `true` | set `true` to bypass TLS | 
| S3_TLS_CA_FILE| no | `/path/to/server.crt` | set to certificate path |
| `NEXUS_CACHE_DIR` | no | `$TMPDIR/nexus-cache` | Local disk cache for downloaded files |
| `FIELD_DEFAULT_LIMIT` | no | `10000` | Rows returned by `/field` when `limit` isn't given |
| `FIELD_MAX_LIMIT` | no | `1000000` | Hard cap on `/field`'s `limit` |
| `PORT` | no | `8080` | Listen port |

AWS credentials themselves are **not** read from a custom variable —
both servers use their SDK's normal default credential chain (env vars
`AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`/`AWS_SESSION_TOKEN`, a shared
credentials file, an EC2/ECS/EKS role, etc.), so anything that already
works with the `aws` CLI or `boto3`/`aws-sdk-go-v2` on your machine works
here unchanged.

## Running against MinIO or another non-AWS S3

```bash
export S3_BUCKET=nexus-data
export S3_ENDPOINT=http://localhost:9000
export S3_FORCE_PATH_STYLE=true
export AWS_ACCESS_KEY_ID=minioadmin
export AWS_SECRET_ACCESS_KEY=minioadmin
export AWS_REGION=us-east-1

# to use TLS/HTTPs
export S3_TLS_INSECURE_SKIP_VERIFY=true   # or 
export S3_TLS_CA_FILE=/path/to/server.crt
```

Then run either server (see `go/README.md` / `py/README.md`).

## Docker

Each sub-directory has its own `Dockerfile`.

```bash
# from the repo root
docker build -f servers/go/Dockerfile -t nexus-s3-server-go .
docker build -f servers/py/Dockerfile -t nexus-s3-server-py .

docker run --rm -p 8080:8080 \
  -e S3_BUCKET=nexus-data -e S3_ENDPOINT=http://host.docker.internal:9000 \
  -e S3_FORCE_PATH_STYLE=true \
  -e AWS_ACCESS_KEY_ID=minioadmin -e AWS_SECRET_ACCESS_KEY=minioadmin \
  nexus-s3-server-go
```

## What's intentionally out of scope

- **Auth on the HTTP API itself.** Both servers are unauthenticated HTTP
  services; put them behind a reverse proxy / API gateway / VPN if the
  data needs access control beyond S3's own permissions.
- **Write endpoints.** These servers are read-only viewers over data
  already landed in S3, mirroring `examples/reader`.
- **Streaming multi-GB downloads.** The first request for a very large
  object will block on a full S3 download before it can be parsed;
  that's the same cost `examples/reader` pays for a local copy, just
  moved to "first request after a cold cache" instead of "every run".
