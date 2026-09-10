# nexus-s3-server (Go)

HTTP server exposing NeXus/HDF5 files from S3, built on the `gonexus`
library in this repo (`../../gonexus`) and `aws-sdk-go-v2`.

See `../README.md` for the full API reference and configuration
variables — this file only covers building/running the Go binary itself.

## Build

Building `gonexus` requires the HDF5 C headers/libraries (it uses cgo),
same as any other consumer of this library — see the top-level
`README.md`/`Makefile` for platform-specific `CGO_CFLAGS`/`CGO_LDFLAGS`.
On Debian/Ubuntu:

```bash
sudo apt-get install -y libhdf5-dev pkg-config
export CGO_CFLAGS="-I/usr/include/hdf5/serial"
export CGO_LDFLAGS="-L/usr/lib/x86_64-linux-gnu/hdf5/serial"

# from the repo root
go build -o nexus-s3-server ./servers/go/
```

## Run
To access S3 storage we need to set it up. For this we can use either minio or
versitygw. Here is how to run your versitygw pointing to your local file system
path:
```
# use HTTP protocol
ROOT_ACCESS_KEY=myaccess ROOT_SECRET_KEY=mysecret ./versitygw posix /Users/vk/Work/CHESS/FOXDEN/s3

# use HTTPs protocol
ROOT_ACCESS_KEY=myaccess ROOT_SECRET_KEY=mysecret ./versitygw --cert $PWD/server.crt --key $PWD/server.key --port :7070 posix /Users/vk/Work/CHESS/FOXDEN/s3
```

Now, we can start our go server with proper configuration:

```bash
# setup all env variables configuring access to S3 server
export S3_BUCKET=nexus-data
export S3_PREFIX=chess/
export S3_ENDPOINT=http://127.0.0.1:7070
export S3_FORCE_PATH_STYLE=true
export AWS_ACCESS_KEY_ID=myaccess
export AWS_SECRET_ACCESS_KEY=mysecret
export AWS_REGION=us-east-1

./nexus-s3-server
# nexus-s3-server listening on :8080 (bucket=nexus-data prefix="chess/" cache=/tmp/nexus-cache)
```

```bash
curl http://localhost:8080/healthz
curl http://localhost:8080/files
curl "http://localhost:8080/tree?key=chess/edd_test.nxs"
```

## Layout

| File | Purpose |
|---|---|
| `main.go` | HTTP server, routing, request handlers |
| `config.go` | Environment-variable configuration |
| `s3store.go` | S3 listing + ETag-based local disk cache |
| `nexusview.go` | Turns a `*gonexus.NXgroup` into the JSON shapes in `../README.md` |
| `errors.go` | Small `apiError` type carrying an HTTP status code |

`main.go` also keeps a small in-process cache of parsed `*gonexus.NXgroup`
trees keyed by local cache path, so repeated requests against the same
file (browsing around one dataset) don't re-parse the HDF5 file on every
call. It's invalidated implicitly whenever `Store.Ensure` downloads a new
copy of the object (a new local path is used, since the file is
re-cached under the same path but the in-memory tree for the *old*
content is simply left to be garbage collected — see the comment on
`treeCache` for the exact behavior).

## Tests

There's no bundled test S3 — during development this was verified against
[moto](https://github.com/getmoto/moto)'s S3 mock (`moto_server`) and
MinIO; both work via `S3_ENDPOINT` + `S3_FORCE_PATH_STYLE=true`. A minimal
local check:

```bash
pip install 'moto[server]'
moto_server -p 5001 &

python3 - <<'EOF'
import boto3
s3 = boto3.client("s3", endpoint_url="http://127.0.0.1:5001",
                   region_name="us-east-1",
                   aws_access_key_id="test", aws_secret_access_key="test")
s3.create_bucket(Bucket="nexus-data")
s3.upload_file("path/to/your.nxs", "nexus-data", "chess/your.nxs")
EOF

S3_BUCKET=nexus-data S3_ENDPOINT=http://127.0.0.1:5001 \
S3_FORCE_PATH_STYLE=true AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test \
./nexus-s3-server &

curl "http://localhost:8080/tree?key=chess/your.nxs"
```
