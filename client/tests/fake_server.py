"""Minimal in-process imitation of the gonexus HTTP API for tests."""
import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

ROWS, COLS = 40, 5
KEY = "chess/results.nxs"
FLAT = [r * 100 + c for r in range(ROWS) for c in range(COLS)]

TREE = {
    "": ("group", "NXroot", {"file_name": "results.nxs"}, ["entry", "proc"]),
    "entry": ("group", "NXentry", {}, ["scan_number", "title"]),
    "entry/scan_number": ("field", 7, "int64", []),
    "entry/title": ("field", '{"a": 1}', "string", []),
    "proc": ("group", "NXprocess", {}, ["data"]),
    "proc/data": ("group", "NXdata", {}, ["I", "radial"]),
    "proc/data/I": ("field", FLAT, "int64", [ROWS, COLS]),
    "proc/data/radial": ("field", [c * 0.5 for c in range(COLS)], "float64", [COLS]),
}
MAX_LIMIT = 37          # deliberately small and odd: exercises client-side chunking


def _brief(path):
    t = TREE[path]
    name = path.rsplit("/", 1)[-1]
    if t[0] == "group":
        return {"name": name, "type": "group", "class": t[1]}
    d = {"name": name, "type": "field", "dtype": t[2]}
    if t[3]:
        d["shape"] = t[3]
    if name == "I":
        d["units"] = "a.u"
    return d


class _Handler(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def _send(self, code, obj, text=False):
        body = (obj if text else json.dumps(obj)).encode()
        self.send_response(code)
        self.send_header("Content-Type", "text/plain" if text else "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        u = urlparse(self.path)
        q = {k: v[0] for k, v in parse_qs(u.query).items()}
        self.server.last_headers = dict(self.headers)
        if u.path == "/healthz":
            return self._send(200, "ok", text=True)
        if u.path == "/files":
            return self._send(200, {"files": [{"key": KEY, "size": 1234}]})
        if q.get("key") != KEY:
            return self._send(400, {"error": "file not found"})
        path = "/".join(p for p in q.get("path", "").split("/") if p)
        if u.path == "/tree":
            return self._send(200, "root:NXroot\n", text=True)
        if u.path == "/group":
            if path not in TREE or TREE[path][0] != "group":
                return self._send(404, {"error": "group not found"})
            t = TREE[path]
            pre = path + "/" if path else ""
            return self._send(200, {"class": t[1], "attrs": t[2],
                                    "children": [_brief(pre + c) for c in t[3]]})
        if u.path == "/field":
            if path not in TREE or TREE[path][0] != "field":
                return self._send(404, {"error": "field not found"})
            t = TREE[path]
            r = {"key": KEY, "path": path, "dtype": t[2]}
            if path.endswith("/I"):
                r.update(units="a.u", attrs={"long_name": "Intensity"})
            if not t[3]:
                r.update(values=t[1], count=1, total=1)
                return self._send(200, r)
            off = int(q.get("offset", 0))
            lim = min(int(q.get("limit", 10)), MAX_LIMIT)
            vals = t[1][off:off + lim]
            r.update(shape=t[3], offset=off, count=len(vals), total=len(t[1]), values=vals,
                     truncated=off + len(vals) < len(t[1]))
            return self._send(200, r)
        self._send(404, {"error": "no such endpoint"})


def start():
    srv = ThreadingHTTPServer(("127.0.0.1", 0), _Handler)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return srv, f"http://127.0.0.1:{srv.server_address[1]}"
