# NeXus Browser (web UI)

Interactive front end for the gonexus HTTP server. It is plain HTML/CSS/JS
(no build step, no npm, no CDN) embedded into the binary with `go:embed`,
so the server remains a single executable and works on air-gapped hosts.

```
servers/go/
  ui.go          go:embed + registerUI(mux)   (serves /ui/, redirects / to /ui/)
  ui/index.html  page skeleton
  ui/app.css     styles (light/dark)
  ui/app.js      tree, field viewer, plots
```

## Enable

`main.go` needs a single line after the other `mux.HandleFunc` calls
(`main.go.patch` has it):

```go
registerUI(mux) // browser UI at /ui/ (see ui.go)
```

Then rebuild and open `http://localhost:8080/`.

## Features

* File picker from `/files` (or type any key), `Open`.
* Lazy tree: each group is fetched from `/group` when expanded; attributes,
  class, dtype, shape and units are shown. Keyboard: arrows / Enter.
* Filter box, *Expand all*, *Collapse*, resizable sidebar, light/dark theme.
* Group page: attributes + clickable children table.
* Field page: dtype/shape/units/attributes, then
  * scalar: value (JSON strings such as `config` are pretty-printed);
  * 1-D: line plot (min/max decimated, drag-zoom, dbl-click reset, log-y,
    hover read-out), paged table, `offset`/`limit` window controls;
  * N-D: row slider (row = one `offset=row*ncols&limit=ncols` request),
    heatmap of a block of rows (viridis, log / 1-99 % clip, hover read-out),
    table; x axis can be taken from any sibling 1-D field of matching length
    (e.g. `radial` for `I`).
  * CSV / JSON download of what is loaded, *Copy API URL*.
* *Raw tree* tab shows `/tree` text.
* Deep links: `/ui/#key=chess/results.nxs&path=saxs_azimuthal/data/I`.

## Notes

* Multi-dimensional data are flat row-major, so `offset`/`limit` are in
  elements; the UI relies on that for rows and heatmap blocks.
* `sliceValues` in `nexusview.go` only slices `float64/float32/int64/int32/
  string/bool`; other dtypes (`uint8`, `int16`, ...) fall into the
  `default:` branch and are returned whole with `total: 1`, which the UI
  cannot page. Add cases there if you have such fields.
* To serve the UI from another origin use `/ui/?api=http://host:8080`; the
  API server must then send CORS headers.
