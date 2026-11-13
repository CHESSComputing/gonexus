# Serving NeXus files from a local directory

Instead of an S3 bucket the server can read a directory tree on its own
filesystem (a data area, an NFS mount, ...). Set `data_dir` (or
`NEXUS_DATA_DIR`); when it is set the S3 settings (`s3_bucket`, endpoint,
credentials) are not used and not required.

```yaml
data_dir: /home/chessdata_svc/FOXDENDEV/data/nexus-data
s3_prefix: chess/        # still selects which keys /files lists by default
```

## Keys

A key is the file's path relative to `data_dir`, with `/` separators. The
file `/home/chessdata_svc/FOXDENDEV/data/nexus-data/chess/RE3/x.nxs` has the
key `chess/RE3/x.nxs`, exactly as in a bucket that mirrors the directory, so
existing links, UI deep links and client code keep working when you switch
between S3 and local mode. As with S3, `prefix` (config value or the
`?prefix=` query parameter of `/files`) is a plain string prefix of the key.

## Symbolic links

* Links to files **and to directories** are followed, including links that
  point outside `data_dir` (e.g. `chess/RE3 -> /nfs/chess/.../RE3`).
* Cycles (a link pointing back to a directory that is currently being
  listed) are detected and skipped with a log message; nesting is also
  limited to 64 levels.
* Broken links are ignored. A file reachable through two links is listed
  once per key.
* Hidden directories (names starting with `.`, e.g. `.snapshot`) are not
  entered.
* The server's user needs read permission on the link targets, not just on
  the links.

## What is served

Only files ending in `.nxs`, `.h5` or `.hdf5` are listed *and* opened; any
other key is rejected, as are keys containing `..`.

## Behavior differences from S3 mode

* **No download, no cache copy.** Files are opened in place, so a 10 GB file
  is available immediately and uses no `cache_dir` space. Combined with
  `lazy: auto|all` this is the lowest-footprint way to serve very large
  files. `head_ttl`, `cache_max_bytes` and `write_timeout` (for downloads) do
  not matter in this mode.
* **Change detection.** On every request the file's size and modification
  time are compared with the previous request; if they differ, the cached
  parsed tree (and any open HDF5 handle) is dropped and the file is re-read.
  A file that is still being written may fail to open until it is complete.
* **/files cost.** Listing walks the directory tree on every call (a narrow
  `prefix` limits the walk to matching directories). On a very large tree or
  a slow network filesystem prefer a specific prefix.
* `/files` reports `ETag` as `<mtime-ns>-<size>` in hex and the file's real
  size and modification time.
