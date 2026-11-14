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

## Files larger than RAM: what is loaded, and how to get the next chunk

**The tree is always complete.** In every mode (`off`, `auto`, `all`) the
server reads the whole structure when a file is first opened: every group, its
class, every field's name, dtype, shape, units and attributes. So the tree in
the web UI (`root:NXroot` and everything below it), `/tree`, `/entries` and
`/group` look identical in all three modes. The modes differ only in what happens
to the *array contents*:

| mode | array contents |
|---|---|
| `off` | all read into RAM when the file is opened. A file that is larger than RAM cannot be served in this mode. |
| `auto` | fields with more than `lazy_threshold` elements stay on disk; **smaller fields are read into RAM at open time** and sliced from memory afterwards. |
| `all` | every array stays on disk; only scalars and strings (small) are kept in RAM. |

Scalars and string fields are always in RAM.

**Getting the next chunk.** There is no server-side "cursor": each request
names the window it wants, and only that window is read from disk. `/field`
takes `offset` and `limit`, counted in elements of the flat, row-major array,
and tells you where you are:

```json
{ "offset": 0, "count": 10000, "total": 1976100, "truncated": true, "values": [ ... ] }
```

`truncated: true` means more data follows; the next chunk is
`offset = offset + count`. Row `r` of an `R x C` field is `offset=r*C&limit=C`.
If `limit` is omitted the server uses `field_default_limit` (10000), and it
never returns more than `field_max_limit` (default 1000000) elements per request.

```bash
# walk a big field 100000 elements at a time
off=0; while :; do
  r=$(curl -s "localhost:8081/field?key=chess/big.nxs&path=entry/data/I&offset=$off&limit=100000")
  echo "$r" | jq '{offset, count, total, truncated}'
  [ "$(echo "$r" | jq .truncated)" = true ] || break
  off=$((off + $(echo "$r" | jq .count)))
done
```

From the other front ends:

* **Python client:** `field[100:200]` downloads just those rows, and
  `for first_row, block in field.iter_chunks(rows=1000): ...` walks the whole
  array in bounded memory. (`field.read()` allocates the entire array in the
  *client's* memory, so avoid it for data larger than the client's RAM.)
* **Web UI:** a 1-D field shows a window (default 50000 elements) with
  *offset / limit / prev / next*; an N-d field has a row slider and prev/next
  (one row per request); the heatmap loads a block of rows (default about
  200000 elements) with *prev / next*. Only the visible window is fetched.

**Sizing.** In `all` mode, server memory is bounded by the number of in-flight
`/field` requests times `limit x element size` (plus its JSON text), regardless
of file size. In `auto` mode the *sum of all fields at or below the threshold*
is held in RAM, so many small fields can add up: lower `lazy_threshold`, or use
`all`, if that total could approach your RAM. Setting the `GOMEMLIMIT`
environment variable (for example `GOMEMLIMIT=2GiB`) makes the Go runtime give
memory back sooner.

## Trying it

```bash
NEXUS_LAZY=auto NEXUS_LAZY_THRESHOLD=100000 ./bin/nexus-s3-server -config config.yaml
curl 'localhost:8081/field?key=chess/results.nxs&path=saxs_azimuthal/data/I&offset=3000&limit=300'
```
