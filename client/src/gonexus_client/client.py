"""
Core of gonexus_client: a small, tiled-like Python client for the gonexus HTTP server.

    from gonexus_client import from_uri

    client = from_uri("http://localhost:8080")          # api_key=... optional
    print(list(client))                                  # available NeXus files
    nexus  = client["chess/results.nxs"]                 # a file (root group)
    print(list(nexus))                                   # ['entry', 'saxs_azimuthal', ...]
    data   = nexus["saxs_azimuthal"]["data"]["I"]        # or nexus["saxs_azimuthal/data/I"]
    print(data.shape, data.dtype, data.units)
    arr    = data.read()                                 # whole array  -> numpy
    row    = data[10]                                    # one row      -> only that row is downloaded
    block  = data[100:200, 50:80]                        # sub-block    -> rows 100..199 downloaded

Only numpy is required; `requests` is used if installed, otherwise urllib.

Data are transferred through  GET /field?key=&path=&offset=&limit=  in chunks.
gonexus stores N-d datasets flat in row-major order, so slicing along the first
axis maps to a contiguous [offset, offset+count) window and is fetched
efficiently; trailing axes are cut client-side with numpy.
"""
from __future__ import annotations

import json
import os
import sys
import urllib.error
import urllib.parse
import urllib.request
from typing import Any, Dict, Iterator, List, Optional, Tuple

import numpy as np

try:                                  # optional, gives connection re-use
    import requests
except ImportError:                   # pragma: no cover
    requests = None

__all__ = ["from_uri", "Client", "Group", "Field", "NexusFile", "GonexusError"]

_slice = slice                        # read(slice=...) shadows the builtin inside that method
DEFAULT_CHUNK = 500_000               # elements requested per /field call


class GonexusError(RuntimeError):
    """Raised for HTTP/API errors reported by the server."""


# ---------------------------------------------------------------- transport
class _Transport:
    def __init__(self, base_url: str, api_key: Optional[str], timeout: float):
        self.base = base_url.rstrip("/")
        self.timeout = timeout
        self.headers = {"Accept": "application/json"}
        if api_key:
            # tiled convention; ignored by the stock server, useful behind a proxy
            self.headers["Authorization"] = f"Apikey {api_key}"
        self.session = requests.Session() if requests else None
        if self.session:
            self.session.headers.update(self.headers)

    def get(self, path: str, params: Optional[dict] = None, text: bool = False):
        params = {k: v for k, v in (params or {}).items() if v is not None}
        url = f"{self.base}{path}"
        try:
            if self.session:
                r = self.session.get(url, params=params, timeout=self.timeout)
                status, body = r.status_code, r.text
            else:
                full = url + ("?" + urllib.parse.urlencode(params) if params else "")
                try:
                    with urllib.request.urlopen(
                        urllib.request.Request(full, headers=self.headers), timeout=self.timeout
                    ) as resp:
                        status, body = resp.status, resp.read().decode("utf-8")
                except urllib.error.HTTPError as e:
                    status, body = e.code, e.read().decode("utf-8", "replace")
        except (OSError, ValueError) as e:     # connection refused, DNS, timeout...
            raise GonexusError(f"cannot reach {url}: {e}") from e
        if status >= 400:
            msg = body
            try:
                msg = json.loads(body).get("error", body)
            except ValueError:
                pass
            raise GonexusError(f"{status} for {path} {params}: {msg}")
        return body if text else json.loads(body)


# ---------------------------------------------------------------- helpers
def _np_dtype(name: str):
    if name in ("string", "object", "", None):
        return object
    return np.dtype(name)


def _norm_path(path: str) -> str:
    return "/".join(p for p in path.split("/") if p)


class _Mapping:
    """Read-only mapping protocol over self._items() -> {name: node}."""

    def _items(self) -> Dict[str, Any]:          # pragma: no cover
        raise NotImplementedError

    def __iter__(self):
        return iter(self._items())

    def __len__(self):
        return len(self._items())

    def keys(self):
        return list(self._items().keys())

    def values(self):
        return [self[k] for k in self._items()]

    def items(self):
        return [(k, self[k]) for k in self._items()]

    def __contains__(self, name):
        try:
            self[name]
            return True
        except KeyError:
            return False


# ---------------------------------------------------------------- Field
class Field:
    """A NeXus dataset. Use .read() or numpy-style indexing to get data."""

    def __init__(self, tp: _Transport, key: str, path: str, brief: dict):
        self._tp, self.key, self.path = tp, key, path
        self.name = path.rsplit("/", 1)[-1]
        self.dtype_name = brief.get("dtype") or ""
        self.dtype = _np_dtype(self.dtype_name)
        self.shape: Tuple[int, ...] = tuple(brief.get("shape") or ())
        self.units: Optional[str] = brief.get("units") or None
        self._attrs: Optional[dict] = None
        self._scalar_cache: Any = None

    # -- metadata
    @property
    def size(self) -> int:
        return int(np.prod(self.shape, dtype=np.int64)) if self.shape else 1

    @property
    def ndim(self) -> int:
        return len(self.shape)

    def __len__(self):
        if not self.shape:
            raise TypeError("len() of a scalar field")
        return self.shape[0]

    @property
    def attrs(self) -> dict:
        if self._attrs is None:
            r = self._tp.get("/field", {"key": self.key, "path": self.path, "offset": 0, "limit": 1})
            self._attrs = r.get("attrs") or {}
            if self.units is None:
                self.units = r.get("units") or None
        return self._attrs

    metadata = attrs

    def __repr__(self):
        u = f" [{self.units}]" if self.units else ""
        return f"<Field {self.path!r} {self.dtype_name} {self.shape if self.shape else 'scalar'}{u}>"

    # -- data access
    def _fetch_flat(self, offset: int, count: int, chunk: int = DEFAULT_CHUNK) -> np.ndarray:
        """Download elements [offset, offset+count) of the flat array."""
        out = np.empty(count, dtype=self.dtype)
        pos = 0
        while pos < count:
            want = min(chunk, count - pos)
            r = self._tp.get("/field", {"key": self.key, "path": self.path,
                                        "offset": offset + pos, "limit": want})
            vals = r.get("values")
            got = len(vals) if isinstance(vals, list) else 0
            if got == 0:
                raise GonexusError(f"server returned no data for {self.path} at offset {offset + pos}")
            if got > want:     # server ignored offset/limit (dtype it cannot slice)
                raise GonexusError(f"server returned {got} values (asked {want}) for {self.path}; "
                                   f"its dtype {self.dtype_name!r} is probably not sliceable server-side")
            out[pos:pos + got] = vals
            pos += got
        return out

    def _read_rows(self, start: int, stop: int, chunk: int = DEFAULT_CHUNK) -> np.ndarray:
        """Rows [start, stop) along axis 0, with full trailing dimensions."""
        row = int(np.prod(self.shape[1:], dtype=np.int64)) if self.ndim > 1 else 1
        flat = self._fetch_flat(start * row, max(0, stop - start) * row, chunk)
        return flat.reshape((max(0, stop - start),) + self.shape[1:])

    def read(self, slice=None, chunk_size: int = DEFAULT_CHUNK):
        """Return the data as a numpy array (scalars as numpy scalars).

        `slice` is anything numpy indexing accepts for the first-axis forms
        (int, slice, Ellipsis, tuples of those); it is equivalent to field[slice].
        """
        if not self.shape:
            if self._scalar_cache is None:
                r = self._tp.get("/field", {"key": self.key, "path": self.path})
                v = r.get("values")
                if isinstance(v, list) and len(v) == 1:
                    v = v[0]
                self._scalar_cache = np.asarray(v, dtype=self.dtype if self.dtype is not object else None)[()]
            return self._scalar_cache
        if slice is None or slice is Ellipsis:
            return self._read_rows(0, self.shape[0], chunk_size)
        return self[slice]

    def __getitem__(self, idx):
        if not self.shape:
            if idx in ((), Ellipsis):
                return self.read()
            raise IndexError("too many indices for a scalar field")
        if not isinstance(idx, tuple):
            idx = (idx,)
        # expand Ellipsis to full slices
        if any(i is Ellipsis for i in idx):
            k = [i is Ellipsis for i in idx].index(True)
            fill = (_slice(None),) * (self.ndim - (len(idx) - 1))
            idx = idx[:k] + fill + idx[k + 1:]
        first, rest = idx[0], idx[1:]
        n0 = self.shape[0]
        if isinstance(first, (int, np.integer)):
            r = int(first) + (n0 if first < 0 else 0)
            if not 0 <= r < n0:
                raise IndexError(f"index {first} out of range for axis 0 with size {n0}")
            block = self._read_rows(r, r + 1)[0]
            return block[rest] if rest else block
        if isinstance(first, _slice):
            start, stop, step = first.indices(n0)
            if step > 0:
                lo, hi = start, max(start, stop)
                block = self._read_rows(lo, hi)[::step]
            else:                                    # negative step: fetch covering range
                rng = range(start, stop, step)
                if len(rng) == 0:
                    return self._read_rows(0, 0)
                lo, hi = rng[-1], rng[0] + 1
                block = self._read_rows(lo, hi)[::-1][::-step]
            return block[(_slice(None),) + rest] if rest else block
        # fancy / boolean indexing: download everything, let numpy do it
        return self.read()[idx]

    def iter_chunks(self, rows: int = 1000) -> Iterator[Tuple[int, np.ndarray]]:
        """Yield (first_row, block) pairs; handy for arrays larger than memory."""
        for s in range(0, self.shape[0], rows):
            yield s, self._read_rows(s, min(self.shape[0], s + rows))

    def __array__(self, dtype=None, copy=None):
        a = np.asarray(self.read())
        return a.astype(dtype) if dtype is not None else a


# ---------------------------------------------------------------- Group
class Group(_Mapping):
    """A NeXus group (NXentry, NXdata, ...). Behaves like a read-only dict."""

    def __init__(self, tp: _Transport, key: str, path: str = "", nx_class: str = ""):
        self._tp, self.key, self.path = tp, key, path
        self.name = path.rsplit("/", 1)[-1] if path else ""
        self.nx_class = nx_class
        self._attrs: Optional[dict] = None
        self._children: Optional[Dict[str, dict]] = None
        self._nodes: Dict[str, Any] = {}

    def _load(self):
        if self._children is None:
            g = self._tp.get("/group", {"key": self.key, "path": self.path})
            self._attrs = g.get("attrs") or {}
            self.nx_class = g.get("class") or self.nx_class
            self._children = {c["name"]: c for c in (g.get("children") or [])}
        return self._children

    def _items(self):
        return self._load()

    @property
    def attrs(self) -> dict:
        self._load()
        return self._attrs

    metadata = attrs

    def __getitem__(self, name: str):
        parts = [p for p in str(name).split("/") if p]
        if not parts:
            return self
        node: Any = self
        for p in parts:
            if not isinstance(node, Group):
                raise KeyError(f"{node.path!r} is a field, cannot descend into {p!r}")
            node = node._child(p)
        return node

    def _child(self, name: str):
        if name in self._nodes:
            return self._nodes[name]
        children = self._load()
        if name not in children:
            raise KeyError(f"{name!r} not found in {self.path or '/'!r}; available: {list(children)}")
        b = children[name]
        path = f"{self.path}/{name}" if self.path else name
        node = (Group(self._tp, self.key, path, b.get("class", "")) if b.get("type") == "group"
                else Field(self._tp, self.key, path, b))
        self._nodes[name] = node
        return node

    def __repr__(self):
        n = len(self._children) if self._children is not None else "?"
        return f"<Group {self.path or '/'!r} {self.nx_class} [{n} children]>"

    # -- convenience
    def walk(self, max_depth: Optional[int] = None) -> Iterator[Tuple[str, Any]]:
        """Depth-first iteration over (path, node) for everything below this group."""
        def rec(g: "Group", depth: int):
            for k in g:
                node = g[k]
                yield node.path, node
                if isinstance(node, Group) and (max_depth is None or depth + 1 < max_depth):
                    yield from rec(node, depth + 1)
        yield from rec(self, 0)

    def tree(self, max_depth: Optional[int] = None, attrs: bool = False) -> str:
        """Text rendering of the hierarchy, in the same style as the server's /tree."""
        lines: List[str] = []

        def rec(g: "Group", depth: int):
            for k in g:
                n = g[k]
                ind = "  " * (depth + 1)
                if isinstance(n, Group):
                    lines.append(f"{ind}{k}:{n.nx_class}")
                    if attrs:
                        lines.extend(f"{ind}  @{a} = {v}" for a, v in n.attrs.items())
                    if max_depth is None or depth + 1 < max_depth:
                        rec(n, depth + 1)
                else:
                    shp = "x".join(map(str, n.shape))
                    lines.append(f"{ind}{k} = {n.dtype_name}({shp})" if shp else f"{ind}{k} = {n.dtype_name}")
        lines.append(f"{self.name or 'root'}:{self.nx_class}")
        rec(self, 0)
        return "\n".join(lines)


class NexusFile(Group):
    """Root group of one NeXus file; also exposes server-side helpers."""

    def __init__(self, tp: _Transport, key: str, info: Optional[dict] = None):
        super().__init__(tp, key, "")
        self.info = info or {}

    def __repr__(self):
        return f"<NexusFile {self.key!r} {self.nx_class} [{len(self)} children]>"

    def tree_text(self) -> str:
        """Full tree exactly as rendered by the server's  GET /tree."""
        return self._tp.get("/tree", {"key": self.key}, text=True)

    def entries(self) -> List[str]:
        """Names of the top-level NXentry groups (GET /entries)."""
        r = self._tp.get("/entries", {"key": self.key})
        ents = r.get("entries", r) if isinstance(r, dict) else r
        return [e["name"] if isinstance(e, dict) else str(e) for e in ents]


# ---------------------------------------------------------------- Client
class Client(_Mapping):
    """Entry point: a mapping from file key -> NexusFile."""

    def __init__(self, uri: str, api_key: Optional[str] = None, timeout: float = 60.0):
        self.uri = uri.rstrip("/")
        self._tp = _Transport(self.uri, api_key or os.environ.get("GONEXUS_API_KEY"), timeout)
        self._files: Optional[Dict[str, dict]] = None
        self._open: Dict[str, NexusFile] = {}

    def refresh(self):
        """Re-read the file list from the server."""
        self._files = None
        self._open.clear()

    def healthy(self) -> bool:
        try:
            self._tp.get("/healthz", text=True)
            return True
        except GonexusError:
            return False

    def _items(self):
        if self._files is None:
            r = self._tp.get("/files")
            self._files = {f["key"]: f for f in (r.get("files") or [])}
        return self._files

    def _resolve(self, name: str) -> str:
        files = self._items()
        if name in files:
            return name
        # forgiving lookup: 'results.nxs' or 'results' when unambiguous
        cand = [k for k in files if k.rsplit("/", 1)[-1] in (name, name + ".nxs", name + ".h5", name + ".nx")]
        if len(cand) == 1:
            return cand[0]
        if len(cand) > 1:
            raise KeyError(f"{name!r} is ambiguous: {cand}")
        raise KeyError(f"{name!r} not found; available: {list(files)}")

    def __getitem__(self, name: str) -> NexusFile:
        key = self._resolve(name)
        if key not in self._open:
            self._open[key] = NexusFile(self._tp, key, self._items()[key])
        return self._open[key]

    def __repr__(self):
        return f"<Client {self.uri} [{len(self._files) if self._files is not None else '?'} files]>"

    def open(self, key: str) -> NexusFile:
        """Open a file by exact key even if it is not in /files (e.g. outside the listed prefix)."""
        return NexusFile(self._tp, key)


def from_uri(uri: str, api_key: Optional[str] = None, timeout: float = 60.0) -> Client:
    """Connect to a gonexus server, e.g. from_uri("http://localhost:8080")."""
    return Client(uri, api_key=api_key, timeout=timeout)
