"""S3 object listing + local disk caching, mirroring ../go/s3store.go.

NeXus/HDF5 files must be read from a real filesystem path (h5py, like
gonexus, reads via the HDF5 C library, not an arbitrary stream), so every
S3 object is downloaded to a local cache directory on first access and
re-used on subsequent requests, keyed by the S3 ETag.
"""
import hashlib
import os
import threading
import time

import boto3
from botocore.client import Config as BotoConfig
from botocore.exceptions import ClientError

NEXUS_SUFFIXES = (".nxs", ".h5", ".hdf5")


class ApiError(Exception):
    """Carries an HTTP status code, mirroring go/errors.go's apiError."""

    def __init__(self, status, message):
        super().__init__(message)
        self.status = status
        self.message = message


def bad_request(msg):
    return ApiError(400, msg)


def not_found(msg):
    return ApiError(404, msg)


def internal(msg):
    return ApiError(500, msg)


def has_nexus_suffix(key: str) -> bool:
    lower = key.lower()
    return any(lower.endswith(suf) for suf in NEXUS_SUFFIXES)


class Store:
    def __init__(self, cfg):
        self.bucket = cfg.bucket
        self.prefix = cfg.prefix
        self.cache_dir = cfg.cache_dir

        boto_kwargs = {"region_name": cfg.region}
        if cfg.endpoint:
            boto_kwargs["endpoint_url"] = cfg.endpoint
        addressing_style = "path" if cfg.force_path_style else "auto"
        boto_kwargs["config"] = BotoConfig(s3={"addressing_style": addressing_style})

        # TLS handling for a self-signed S3_ENDPOINT (e.g. local
        # VersityGW/MinIO). boto3's `verify` takes either False (skip
        # verification entirely) or a path to a CA bundle/cert to trust
        # in addition to the system roots.
        if cfg.tls_insecure_skip_verify:
            import logging

            logging.getLogger("nexus-s3-server").warning(
                "S3_TLS_INSECURE_SKIP_VERIFY is set - TLS certificate verification "
                "is DISABLED for %s. Only use this against a trusted local/dev endpoint.",
                cfg.endpoint,
            )
            boto_kwargs["verify"] = False
        elif cfg.tls_ca_file:
            boto_kwargs["verify"] = cfg.tls_ca_file

        self.client = boto3.client("s3", **boto_kwargs)

        self._lock = threading.Lock()
        self._key_locks = {}

    def _lock_for(self, key):
        with self._lock:
            lk = self._key_locks.get(key)
            if lk is None:
                lk = threading.Lock()
                self._key_locks[key] = lk
            return lk

    def list(self, prefix_override=None):
        """List every object under bucket/prefix whose key ends in a
        recognized NeXus/HDF5 suffix."""
        prefix = prefix_override if prefix_override else self.prefix
        out = []
        paginator = self.client.get_paginator("list_objects_v2")
        try:
            for page in paginator.paginate(Bucket=self.bucket, Prefix=prefix):
                for obj in page.get("Contents", []):
                    key = obj["Key"]
                    if not has_nexus_suffix(key):
                        continue
                    out.append(
                        {
                            "key": key,
                            "size": obj.get("Size", 0),
                            "last_modified": obj["LastModified"].isoformat()
                            if obj.get("LastModified")
                            else None,
                            "etag": (obj.get("ETag") or "").strip('"'),
                        }
                    )
        except ClientError as e:
            raise internal(f"listing s3://{self.bucket}/{prefix}: {e}")
        out.sort(key=lambda f: f["key"])
        return out

    def _local_path(self, key: str) -> str:
        h = hashlib.sha1(key.encode("utf-8")).hexdigest()[:8]
        safe = key.replace("..", "_")
        return os.path.join(self.cache_dir, h, safe)

    def ensure(self, key: str) -> str:
        """Make sure the S3 object at key is present locally (downloading
        or re-downloading it if its ETag changed) and return its local
        path. Concurrent requests for the same key are serialized."""
        if not key:
            raise bad_request("missing required query parameter 'key'")

        lock = self._lock_for(key)
        with lock:
            try:
                head = self.client.head_object(Bucket=self.bucket, Key=key)
            except ClientError as e:
                raise not_found(f"object s3://{self.bucket}/{key} not found: {e}")
            etag = (head.get("ETag") or "").strip('"')

            local_path = self._local_path(key)
            etag_path = local_path + ".etag"

            if os.path.exists(local_path) and os.path.exists(etag_path):
                with open(etag_path) as f:
                    cached_etag = f.read()
                if cached_etag == etag:
                    return local_path

            os.makedirs(os.path.dirname(local_path), exist_ok=True)
            tmp_path = f"{local_path}.download-{os.getpid()}-{int(time.time() * 1000)}"
            try:
                self.client.download_file(self.bucket, key, tmp_path)
            except ClientError as e:
                if os.path.exists(tmp_path):
                    os.remove(tmp_path)
                raise internal(f"downloading s3://{self.bucket}/{key}: {e}")

            os.replace(tmp_path, local_path)
            with open(etag_path, "w") as f:
                f.write(etag)
            return local_path
