"""Environment-variable configuration shared across the Python server.

Mirrors ../go/config.go so both implementations expose the same knobs.
"""
import os
import tempfile


def _env_bool(name, default):
    v = os.environ.get(name)
    if v is None or v == "":
        return default
    return v.strip().lower() in ("1", "true", "yes", "on")


def _env_int(name, default):
    v = os.environ.get(name)
    if v is None or v == "":
        return default
    try:
        return int(v)
    except ValueError:
        return default


class Config:
    def __init__(self):
        self.bucket = os.environ.get("S3_BUCKET", "")
        self.prefix = os.environ.get("S3_PREFIX", "")
        self.region = os.environ.get("S3_REGION") or os.environ.get("AWS_REGION") or "us-east-1"
        self.endpoint = os.environ.get("S3_ENDPOINT", "")
        self.force_path_style = _env_bool("S3_FORCE_PATH_STYLE", False)

        self.cache_dir = os.environ.get(
            "NEXUS_CACHE_DIR", os.path.join(tempfile.gettempdir(), "nexus-cache")
        )

        self.default_field_limit = _env_int("FIELD_DEFAULT_LIMIT", 10_000)
        self.max_field_limit = _env_int("FIELD_MAX_LIMIT", 1_000_000)

        self.port = _env_int("PORT", 8080)
        self.host = os.environ.get("HOST", "0.0.0.0")

        if not self.bucket:
            raise RuntimeError("required environment variable S3_BUCKET is not set")

        os.makedirs(self.cache_dir, exist_ok=True)
