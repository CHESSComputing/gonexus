# nexus-s3-server (Python)

HTTP server exposing NeXus/HDF5 files from S3, built on `h5py`, `boto3`,
and Flask. It exposes the same API as the Go server in `../go` — see
`../README.md` for the full reference.

`h5py`'s wheels bundle their own HDF5 build, so unlike the Go server
there's no system HDF5 dev package to install first.

## Install & run

```bash
cd servers/py
pip install -r requirements.txt

export S3_BUCKET=nexus-data
export S3_PREFIX=chess/              # optional
export AWS_REGION=us-east-1
# plus normal AWS credential env vars / profile / instance role

python3 server.py
# nexus-s3-server listening on 0.0.0.0:8080 (bucket=nexus-data prefix='chess/' cache=/tmp/nexus-cache)
```

```bash
curl http://localhost:8080/healthz
curl http://localhost:8080/files
curl "http://localhost:8080/tree?key=chess/edd_test.nxs"
```

The bundled Flask dev server (`app.run(..., threaded=True)`) is fine for
local use and small deployments; for anything production-facing, run it
under a real WSGI server instead, e.g.:

```bash
pip install gunicorn
gunicorn -w 4 -b 0.0.0.0:8080 server:app
```

(`-w 4` workers each keep their own local disk cache and open-file cache;
that's fine since the disk cache is shared and keyed by ETag, so workers
converge on the same cached copy of any given object.)

## Layout

| File | Purpose |
|---|---|
| `server.py` | Flask app, routing, request handlers |
| `config.py` | Environment-variable configuration |
| `s3store.py` | S3 listing + ETag-based local disk cache |
| `nexusview.py` | Turns an open `h5py.File` into the JSON shapes in `../README.md`, plus the `/tree` text renderer |

`server.py` keeps a small in-process cache of open `h5py.File` handles
keyed by local cache path, so repeated requests against the same file
don't reopen it (and re-walk attributes) on every call.

## Tests

No bundled test S3; verified the same way as the Go server, against
[moto](https://github.com/getmoto/moto)'s S3 mock and MinIO:

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
python3 server.py &

curl "http://localhost:8080/tree?key=chess/your.nxs"
```
