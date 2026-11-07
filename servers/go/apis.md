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

## 7. Multi-dimensional fields (the 2D `I` field)

`I` is `int64(6587x300)` — a genuine 2D array. gonexus stores every
multi-dimensional dataset as a **flat slice in row-major (C) order** and keeps
the real dimensions in `shape`. Consequently `offset` and `limit` always count
*elements*, not rows, and `total` is the product of all dimensions
(`6587*300 = 1976100`). `values` is a flat JSON array; use `shape` to
reshape it on the client.

Row `r` of a `rows x cols` array is therefore `offset=r*cols&limit=cols`.
For `I` (`cols = 300`):

```bash
# first frame/row (elements 0..299)
curl -s "http://localhost:8080/field?key=chess/results.nxs&path=saxs_azimuthal/data/I&offset=0&limit=300"

# row 10 (elements 3000..3299)
curl -s "http://localhost:8080/field?key=chess/results.nxs&path=saxs_azimuthal/data/I&offset=3000&limit=300"

# rows 100..199 as one block (30000 elements), reshape with shape [100, 300]
curl -s "http://localhost:8080/field?key=chess/results.nxs&path=saxs_azimuthal/data/I&offset=30000&limit=30000"
```
```json
{
  "key": "chess/results.nxs",
  "path": "saxs_azimuthal/data/I",
  "dtype": "int64",
  "shape": [6587, 300],
  "units": "a.u",
  "offset": 0,
  "count": 300,
  "total": 1976100,
  "truncated": true,
  "values": [ ... 300 numbers ... ]
}
```

Notes:

* If `limit` is omitted the server uses `FIELD_DEFAULT_LIMIT` (10000 elements
  by default), i.e. only the first ~33 rows of `I`. `limit` is capped by
  `FIELD_MAX_LIMIT`.
* Scalars (`shape` absent) ignore `offset`/`limit` and return `"total": 1`.
* Supported element types are `float32/64`, `int8/16/32/64`,
  `uint8/16/32/64`, `string` and `bool`; `uint8` data are returned as plain
  numbers (not base64).
* In Python: `np.array(resp["values"], dtype=resp["dtype"]).reshape(resp["shape"])`
  (when the whole array, or a whole number of rows, was fetched).
