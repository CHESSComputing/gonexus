# S3 APIs examples
Based on results.nxs file here are particular examples of accessing data
through S3 APIs (please note the response data will vary based on content
of results.nxs file):

## 1. See what entries exist

```bash
curl -s "http://localhost:8080/entries?key=chess/results.nxs"
```
```json
{
  "key": "chess/results.nxs",
  "root_class": "NXroot",
  "entries": [
    { "name": "entry", "type": "group", "class": "NXentry" }
  ]
}
```

## 2. Root-level attributes (file_name, file_time, etc.)

Root attrs live on the group itself — pass an empty/omitted `path`:

```bash
curl -s "http://localhost:8080/group?key=chess/results.nxs"
```
```json
{
  "key": "chess/results.nxs",
  "path": "",
  "class": "NXroot",
  "attrs": {
    "file_name": "/nfs/chess/scratch/user/kls286/saxswaxs_chap/results.nxs",
    "file_time": "2024-11-05T12:36:50.359295",
    "HDF5_Version": "1.14.2",
    "h5py_version": "3.11.0"
  },
  "children": [ ... ]
}
```

## 3. Browse into `entry` and `entry/data`

```bash
curl -s "http://localhost:8080/group?key=chess/results.nxs&path=entry"
```
gives you `data` (NXdata group), `scan_number`, `spec_file`.

```bash
curl -s "http://localhost:8080/group?key=chess/results.nxs&path=entry/data"
```
gives you the list of 1D fields (`diode`, `ic0`, `ic1`, `ic1_ni`, `ic3`, `icv_hi`, `icv_lo`, `mcs0`, `xrfx`, `xrfz`), each `float64(6587)`.

## 4. Pull one field's data (with pagination)

```bash
curl -s "http://localhost:8080/field?key=chess/results.nxs&path=entry/data/diode&offset=0&limit=50"
```
```json
{
  "key": "chess/results.nxs",
  "path": "entry/data/diode",
  "dtype": "float64",
  "shape": [6587],
  "offset": 0,
  "count": 50,
  "total": 6587,
  "truncated": true,
  "values": [0.123, 0.456, ...]
}
```
Bump `offset`/`limit` (or omit `limit` to use `FIELD_DEFAULT_LIMIT`/`FIELD_MAX_LIMIT` from your config) to page through the rest.

## 5. Scalar fields (scan_number, spec_file)

```bash
curl -s "http://localhost:8080/field?key=chess/results.nxs&path=entry/scan_number"
curl -s "http://localhost:8080/field?key=chess/results.nxs&path=entry/spec_file"
```
Scalars ignore offset/limit and come back as a single value with `"total": 1`.

## 6. Processing groups — `saxs_azimuthal` / `waxs_azimuthal`

```bash
curl -s "http://localhost:8080/group?key=chess/results.nxs&path=saxs_azimuthal"
```
gives you `config`, `data`, `date`.

The `config` field is a JSON-encoded string — fetch it as a scalar and parse client-side:
```bash
curl -s -s "http://localhost:8080/field?key=chess/results.nxs&path=saxs_azimuthal/config" | jq -r '.values' | jq .
```

`radial` is a plain 1D field, works exactly like `diode` above:
```bash
curl -s "http://localhost:8080/field?key=chess/results.nxs&path=saxs_azimuthal/data/radial"
```

Get units/long_name via `attrs` on a field:
```bash
curl -s "http://localhost:8080/field?key=chess/results.nxs&path=saxs_azimuthal/data/I" | jq '.attrs'
# { "units": "a.u", "long_name": "Intensity (a.u)" }
```

## ⚠️ One real limitation: the 2D `I` field

`I` is `int64(6587x300)` — a genuine 2D array. Look at `sliceValues` in `nexus.go`:

```go
func sliceValues(value interface{}, offset, limit int) (interface{}, int, bool, error) {
	switch v := value.(type) {
	case []float64:
		return sliceGeneric(v, offset, limit)
	...
	default:
		// Fall back ... report it as-is, unsliced
		return value, 1, false, nil
	}
}
```

`gonexus` likely represents a 2D `int64` array as `[][]int64` (or a flat `[]int64` with `Shape: [6587,300]` — depends on the library), and neither matches any case in that switch. So a request like:

```bash
curl -s "http://localhost:8080/field?key=chess/results.nxs&path=saxs_azimuthal/data/I&limit=10"
```

silently ignores `offset`/`limit` and dumps the **entire** 6587×300 array (≈2M ints) in one response, with `"total": 1` — which is both misleading and a potential memory/latency problem for bigger scans.

If you want, I can add proper row-based pagination for 2D fields (e.g. `offset`/`limit` slicing over the first axis, so `I&offset=0&limit=100` returns 100 rows of 300 columns each) — just say the word and I'll patch `sliceValues`/`buildField` accordingly.
