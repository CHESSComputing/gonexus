"""Build JSON/text views of a NeXus (HDF5) file using plain h5py, mirroring
../go/nexusview.go so both server implementations expose identical shapes.
"""
import numpy as np
import h5py

from s3store import bad_request, not_found


def _py_scalar(v):
    """Convert a numpy scalar/bytes value to a plain JSON-serializable
    Python value."""
    if isinstance(v, bytes):
        return v.decode("utf-8", errors="replace")
    if isinstance(v, np.generic):
        return v.item()
    return v


def _attrs_map(obj):
    if obj.attrs is None or len(obj.attrs) == 0:
        return None
    out = {}
    for k in obj.attrs:
        out[k] = _py_scalar(obj.attrs[k])
    return out


def _units(obj):
    u = obj.attrs.get("units")
    if u is None:
        return None
    return _py_scalar(u)


def _dtype_name(dset: h5py.Dataset) -> str:
    return str(dset.dtype)


def _brief_of(name, obj):
    if isinstance(obj, h5py.Group):
        cls = obj.attrs.get("NX_class")
        cls = _py_scalar(cls) if cls is not None else "NXcollection"
        return {"name": name, "type": "group", "class": cls}
    # Dataset
    return {
        "name": name,
        "type": "field",
        "dtype": _dtype_name(obj),
        "shape": list(obj.shape) if obj.shape else [],
        "units": _units(obj),
    }


def build_entries(key, f: h5py.File):
    root_class = f.attrs.get("NX_class")
    root_class = _py_scalar(root_class) if root_class is not None else "NXroot"
    entries = []
    for name, obj in f.items():
        if isinstance(obj, h5py.Group):
            entries.append(_brief_of(name, obj))
    return {"key": key, "root_class": root_class, "entries": entries}


def _resolve_group_or_root(f: h5py.File, path: str):
    if not path or path == "/":
        return f
    try:
        obj = f[path]
    except KeyError:
        raise not_found(f"group not found at path {path!r}")
    if not isinstance(obj, h5py.Group):
        raise not_found(f"path {path!r} is not a group")
    return obj


def build_group(key, path, f: h5py.File):
    g = _resolve_group_or_root(f, path)
    cls = g.attrs.get("NX_class")
    cls = _py_scalar(cls) if cls is not None else ("NXroot" if g.name == "/" else "NXcollection")
    children = [_brief_of(name, obj) for name, obj in g.items()]
    return {
        "key": key,
        "path": path,
        "class": cls,
        "attrs": _attrs_map(g),
        "children": children,
    }


def build_field(key, path, f: h5py.File, offset: int, limit: int):
    if not path:
        raise bad_request("missing required query parameter 'path'")
    try:
        dset = f[path]
    except KeyError:
        raise not_found(f"field not found at path {path!r}")
    if not isinstance(dset, h5py.Dataset):
        raise not_found(f"path {path!r} is not a field")

    resp = {
        "key": key,
        "path": path,
        "dtype": _dtype_name(dset),
        "shape": list(dset.shape) if dset.shape else [],
        "units": _units(dset),
        "attrs": _attrs_map(dset),
    }

    if dset.shape == ():
        # Scalar field.
        resp["values"] = _py_scalar(dset[()])
        resp["offset"] = 0
        resp["count"] = 1
        resp["total"] = 1
        resp["truncated"] = False
        return resp

    # 1-D fields (and the leading axis of N-D fields) can be sliced
    # directly via h5py, which performs a partial HDF5 read rather than
    # loading the whole dataset into memory first.
    total = dset.shape[0]
    offset = max(0, offset)
    offset = min(offset, total)
    end = total
    if limit and limit > 0 and offset + limit < end:
        end = offset + limit
    chunk = dset[offset:end]
    truncated = end < total

    resp["offset"] = offset
    resp["total"] = total
    resp["truncated"] = truncated
    resp["count"] = int(chunk.shape[0]) if hasattr(chunk, "shape") else len(chunk)
    resp["values"] = _to_jsonable(chunk)
    return resp


def _to_jsonable(arr):
    if isinstance(arr, np.ndarray):
        if arr.dtype.kind in ("S", "O", "U"):
            return [
                v.decode("utf-8", errors="replace") if isinstance(v, bytes) else str(v)
                for v in arr.tolist()
            ]
        return arr.tolist()
    return arr


# ---------------------------------------------------------------------------
# Plain-text tree view, in the same spirit as gonexus's NXgroup.Tree().
# ---------------------------------------------------------------------------

_WELL_KNOWN_SKIP = {"NX_class"}


def _fmt_attrs(obj, indent):
    lines = []
    for k in obj.attrs:
        if k in _WELL_KNOWN_SKIP:
            continue
        v = _py_scalar(obj.attrs[k])
        lines.append(f"{indent}@{k} = {v}")
    return lines


def _fmt_node(name, obj, indent, lines):
    if isinstance(obj, h5py.Group):
        cls = obj.attrs.get("NX_class")
        cls = _py_scalar(cls) if cls is not None else "NXcollection"
        lines.append(f"{indent}{name}:{cls}")
        child_indent = indent + "  "
        lines.extend(_fmt_attrs(obj, child_indent))
        for child_name, child_obj in obj.items():
            _fmt_node(child_name, child_obj, child_indent, lines)
    else:
        shape = "x".join(str(d) for d in obj.shape) if obj.shape else ""
        shape_str = f"({shape})" if shape else ""
        lines.append(f"{indent}{name} = {obj.dtype}{shape_str}")
        for l in _fmt_attrs(obj, indent + "  "):
            lines.append(l)


def build_tree_text(f: h5py.File) -> str:
    lines = []
    root_cls = f.attrs.get("NX_class")
    root_cls = _py_scalar(root_cls) if root_cls is not None else "NXroot"
    lines.append(f"root:{root_cls}")
    lines.extend(_fmt_attrs(f, "  "))
    for name, obj in f.items():
        _fmt_node(name, obj, "  ", lines)
    return "\n".join(lines)
