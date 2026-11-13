# Lazy loading in the gonexus S3 server

By default the server reads **every dataset of a file into memory** the first
time the file is requested and keeps it there. That is fine for files up to a
few hundred MB but not for files of O(10 GB). Lazy loading keeps big datasets
on disk and reads only the window requested through `/field`.

It is opt-in and off by default; with `lazy: off` the server behaves exactly
as before.

## Modes

| `lazy` (env `NEXUS_LAZY`) | Behavior |
|---|---|
| `off` (default) | All datasets are read into memory on first access. |
| `auto` | Datasets with more than `lazy_threshold` elements (default 100000) stay on disk; smaller ones are loaded into memory (fast, no disk read per request). |
| `all` | Every array dataset stays on disk. |

Scalars and string datasets are always loaded (they are small).

## Settings

| config key | environment variable | default | meaning |
|---|---|---|---|
| `lazy` | `NEXUS_LAZY` | `off` | `off`, `auto` or `all` |
| `lazy_threshold` | `NEXUS_LAZY_THRESHOLD` | `100000` | element count above which `auto` keeps a dataset on disk |
| `max_open_files` | `NEXUS_MAX_OPEN_FILES` | `8` | lazy trees keep their HDF5 file open; beyond this number the least recently used is closed (reopened on next use) |

The config file overrides environment variables, as for all other settings.
The startup log line shows the active values.

## What changes for clients

Nothing. `/files`, `/tree`, `/entries`, `/group` and `/field` return the same
JSON as before, including `shape`, `dtype`, `total`, `offset`, `count` and
`truncated`; `offset`/`limit` still count flat, row-major elements (row `r` of
an `R x C` field is `offset=r*C&limit=C`). The web UI and the Python client
work unmodified.

## Memory and performance

* Opening a file costs only walking its tree (metadata), not reading its data.
* A `/field` request reads just `offset..offset+limit` from disk, using HDF5
  hyperslab selection, so memory per request is about `limit x element size`
  (plus the JSON encoding of that window). Keep `field_max_limit` in mind: it
  bounds that.
* All HDF5 calls are serialized by one lock (libhdf5 is normally not
  thread-safe), so concurrent `/field` requests are processed one at a time.
* Chunked and compressed datasets are decompressed chunk by chunk; for fast
  row access, files written with contiguous layout or chunks aligned to rows
  perform best.
* The file is still downloaded from S3 into `cache_dir` in full (HDF5 needs a
  seekable file); lazy loading saves RAM, not disk or first-access time.

## Trying it

```bash
NEXUS_LAZY=auto NEXUS_LAZY_THRESHOLD=100000 ./bin/nexus-s3-server -config config.yaml
curl 'localhost:8081/field?key=chess/results.nxs&path=saxs_azimuthal/data/I&offset=3000&limit=300'
```
