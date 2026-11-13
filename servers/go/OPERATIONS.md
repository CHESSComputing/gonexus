# Operating the server with large files

Settings that matter when files are big (all optional; the config file
overrides environment variables). Lazy loading itself is described in
`LAZY.md`.

| config key | environment variable | default | meaning |
|---|---|---|---|
| `write_timeout` | `NEXUS_WRITE_TIMEOUT` | `600` | Seconds one response may take to write. This includes the S3 download triggered by the first request for a file (previously a fixed 60 s). `0` disables the limit. |
| `head_ttl` | `NEXUS_HEAD_TTL` | `30` | After a cached file has been verified against S3 (`HeadObject`), skip re-checking for this many seconds. `0` checks on every request (the previous behavior). |
| `cache_max_bytes` | `NEXUS_CACHE_MAX_BYTES` | `0` | Cap on the total size of `cache_dir`. After a download pushes it over the cap, least-recently-used files are deleted. `0` means unlimited. |

## Behavior

* **First access to a large object.** The download is no longer tied to the
  request that started it: if the client gives up or `write_timeout` expires,
  the download still completes (6 h safety limit), and a retry is then served
  from the cache. Concurrent requests for the same file wait for the one
  download; other files are not blocked.
* **File replaced in S3.** When the ETag changes, the file is re-downloaded
  and any cached parsed tree of the old version is dropped (previously the
  old tree kept being served until restart).
* **Cache eviction.** Files used within the last minute, and the file just
  downloaded, are never evicted. Evicted files are re-downloaded on next
  use. If everything left is in use the cap can be exceeded temporarily; a
  warning is logged.
* **`/field` responses** are now compact JSON (no indentation), roughly a
  third of the previous size for large windows. Other endpoints are unchanged.
