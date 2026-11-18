# Serving files by path (`file_system_mode`)

Use this mode when the data you want to show live in many unrelated places on
the server's filesystem (for example under `/nfs/...`) and cannot be gathered
under one directory. A file's **key is its absolute path**, so a record that
says "the data are in `/nfs/chess/id4b/2026-2/scan7.nxs`" can be turned straight
into a link:

```
http://gonexus-host:8081/view?file=/nfs/chess/id4b/2026-2/scan7.nxs
```

```yaml
file_system_mode: true      # NEXUS_FILE_SYSTEM_MODE=true
data_dir: /nfs              # optional but strongly recommended: where files may be read
```

S3 is not used in this mode, and nothing is copied: files are opened in
place, so even multi-GB files are available at once (combine with
`lazy: auto|all`, see `LAZY.md`).

## The three ways to get files

| setting | a file's key is | `data_dir` means |
|---|---|---|
| (default) | its key in the S3 bucket | – |
| `data_dir: /x` | its path **relative** to `/x` (`chess/RE3/a.nxs`) | the root of the tree being served (`LOCAL.md`) |
| `file_system_mode: true` | its **absolute path** (`/nfs/chess/a.nxs`) | an **allow-list**: only files below it may be read. Several directories can be given, separated by `:` (`/nfs:/mnt/chessdata`). Without it, every path is allowed. |

## Using it

The key is the path, so every endpoint takes it as `key`:

```bash
curl -G localhost:8081/tree  --data-urlencode 'key=/nfs/chess/id4b/scan7.nxs'
curl -G localhost:8081/group --data-urlencode 'key=/nfs/chess/id4b/scan7.nxs' --data-urlencode 'path=entry/data'
curl -G localhost:8081/field --data-urlencode 'key=/nfs/chess/id4b/scan7.nxs' --data-urlencode 'path=entry/data/I' \
     --data-urlencode offset=0 --data-urlencode limit=300
```

`/view?file=<path>[&path=<group or field>]` redirects to the web UI with that
file (and node) open. A `file://` URL is accepted too. **URL-encode the path
when building a link**, since paths may contain spaces, `#`, `&` or `+`:

```python
from urllib.parse import quote            # Python
url = "http://gonexus-host:8081/view?file=" + quote(path, safe="/")
```
```go
u := "http://gonexus-host:8081/view?file=" + url.QueryEscape(path)   // Go
```
```js
const u = "http://gonexus-host:8081/view?file=" + encodeURIComponent(path);  // JavaScript
```

The Python client works as well: `client.open("/nfs/chess/id4b/scan7.nxs")["entry/data/I"][10:20]`.

## How access is decided

For every request naming a file, in this order:

1. The path must be **absolute**, contain no NUL byte and no `..` element
   (refused with 400, never "cleaned" into something else).
2. Only **NeXus/HDF5 names** are served: `.nxs`, `.h5`, `.hdf5` (400 otherwise).
3. If `data_dir` is set, the path must lie **inside an allowed directory**
   (403 otherwise).
4. The file must exist and be a regular file (404 / 400).
5. If `data_dir` is set, the path **with all symlinks resolved** must still lie
   inside an allowed directory, so a link inside `/nfs` cannot be used to read
   something outside it (403). If `/nfs/chess` is itself a link to
   `/mnt/chess`, add `/mnt/chess` to `data_dir`.
6. The operating system has the last word: if the user the server runs as may
   not read the file, the answer is 403 "permission denied".

## Security: read this before enabling it

* **The server reads files with its own permissions, not the viewer's.**
  Anyone who can reach the server can open every NeXus file in the allowed
  directories that the server's user can read, whatever their own account
  or their access rights in FOXDEN allow. gonexus has no authentication.
* Therefore: **set `data_dir`** (without it, *any* readable NeXus/HDF5 file on
  the machine can be opened, and the server logs a warning at startup); run the
  server as a dedicated unprivileged user that has read access only to the data
  you intend to publish; keep it on an internal network or behind a VPN or
  firewall; use HTTPS (`TLS.md`); and, if per-user access matters, put it
  behind a reverse proxy that authenticates users (for example against FOXDEN)
  before forwarding.
* A symlink swapped for another between the check and the read cannot be
  excluded; do not let untrusted users write into the allowed directories.
* Only the file's *contents* are exposed; the server never writes.

## `/files` in this mode

The whole tree of a shared filesystem is far too large to walk blindly, so
`/files` lists **nothing unless you give a prefix**: `/files?prefix=/nfs/chess/id4b/`
lists the NeXus files below that directory (a prefix is a plain string prefix,
so `/nfs/chess/id4b/sc` also works). A single call returns at most 5000 files,
examines at most 200000 directory entries and stops after 20 seconds; if it
hits a bound it returns what it has found and logs it, so use a longer prefix.
Hidden directories (`.snapshot`, ...) are skipped, symlinks are followed
(cycles are detected), and nothing outside the allowed directories is listed.
A prefix naming a directory that does not exist gives an empty list, not an
error. `s3_prefix`, if it is an absolute directory, serves as the default
prefix.

## In the web UI

Open a file by link (`/view?file=...`), or paste a full path into the file box
and press Enter. As you type an absolute path the box asks the server for the
NeXus files below it, so typing `/nfs/chess/` and then a few letters narrows the
list like a path completion; the box shows the server's message if a directory
may not be listed.

## Changes to files

The size and modification time of a file are checked on every request; if they
changed, the cached parsed tree is dropped and the file is read again.
