#!/usr/bin/env python3
"""nexus-s3-server (Python): a small HTTP server that exposes NeXus (HDF5)
files stored in an S3 bucket for browsing over plain REST/JSON, using
h5py to read the file's tree structure and data.

Since HDF5 files must be read from a real filesystem path, each object is
transparently downloaded to a local disk cache (keyed by its S3 ETag) on
first request and re-used afterwards.

Endpoints (identical to the Go implementation in ../go):

    GET /healthz
    GET /files                                  list available NeXus files
    GET /tree?key=...                           full tree, text/plain
    GET /entries?key=...                        top-level NXentry groups
    GET /group?key=...&path=...                 a group's direct children
    GET /field?key=...&path=...&offset=&limit=  a field's metadata + data

See ../README.md for configuration (all via environment variables) and
full endpoint documentation.
"""
import logging
import threading
import time

import h5py
from flask import Flask, jsonify, request, Response

from config import Config
from s3store import Store, ApiError
import nexusview

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(message)s")
log = logging.getLogger("nexus-s3-server")

app = Flask(__name__)

cfg = Config()
store = Store(cfg)

_tree_lock = threading.Lock()
_tree_cache = {}  # local_path -> h5py.File (kept open)


def _load(key):
    """Ensure the S3 object for key is cached locally and return an open
    h5py.File for it, opening/caching it on first request."""
    if not key:
        raise ApiError(400, "missing required query parameter 'key'")
    local_path = store.ensure(key)
    with _tree_lock:
        f = _tree_cache.get(local_path)
        if f is None:
            try:
                f = h5py.File(local_path, "r")
            except OSError as e:
                raise ApiError(500, f"reading nexus file {local_path}: {e}")
            _tree_cache[local_path] = f
        return f


@app.errorhandler(ApiError)
def handle_api_error(err: ApiError):
    return jsonify({"error": err.message}), err.status


@app.before_request
def _start_timer():
    request._start_time = time.time()


@app.after_request
def _log_request(resp):
    dt = time.time() - getattr(request, "_start_time", time.time())
    log.info("%s %s %.3fs", request.method, request.full_path, dt)
    return resp


@app.get("/healthz")
def healthz():
    return jsonify({"status": "ok"})


@app.get("/files")
def files():
    prefix = request.args.get("prefix", "")
    result = store.list(prefix or None)
    return jsonify(
        {
            "bucket": cfg.bucket,
            "prefix": prefix or cfg.prefix,
            "count": len(result),
            "files": result,
        }
    )


@app.get("/tree")
def tree():
    key = request.args.get("key", "")
    f = _load(key)
    text = nexusview.build_tree_text(f) + "\n"
    return Response(text, mimetype="text/plain")


@app.get("/entries")
def entries():
    key = request.args.get("key", "")
    f = _load(key)
    return jsonify(nexusview.build_entries(key, f))


@app.get("/group")
def group():
    key = request.args.get("key", "")
    path = request.args.get("path", "")
    f = _load(key)
    return jsonify(nexusview.build_group(key, path, f))


@app.get("/field")
def field():
    key = request.args.get("key", "")
    path = request.args.get("path", "")
    offset = request.args.get("offset", default=0, type=int)
    limit = request.args.get("limit", default=cfg.default_field_limit, type=int)
    if limit <= 0 or limit > cfg.max_field_limit:
        limit = cfg.max_field_limit
    f = _load(key)
    return jsonify(nexusview.build_field(key, path, f, offset, limit))


if __name__ == "__main__":
    log.info(
        "nexus-s3-server listening on %s:%s (bucket=%s prefix=%r cache=%s)",
        cfg.host,
        cfg.port,
        cfg.bucket,
        cfg.prefix,
        cfg.cache_dir,
    )
    app.run(host=cfg.host, port=cfg.port, threaded=True)
