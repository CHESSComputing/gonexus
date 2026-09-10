# gonexus

`gonexus` is a Go port of the core data model and file I/O of
[`nexusformat.nexus`](https://github.com/nexpy/nexusformat/tree/main/src/nexusformat/nexus),
the Python package that provides an API for reading, creating, and
manipulating [NeXus](https://www.nexusformat.org) scientific data files.
NeXus is a set of conventions layered on top of HDF5, used across neutron,
X-ray, and muon science facilities.

Like the Python original, `gonexus` maps the hierarchical structure of a
NeXus/HDF5 file directly onto in-memory values:

| HDF5 concept | Python (`nexusformat`) | Go (`gonexus`)     |
|---|---|---|
| Group        | `NXgroup`               | `*gonexus.NXgroup`  |
| Dataset      | `NXfield`               | `*gonexus.NXfield`  |
| Attribute    | `NXattr`                | `*gonexus.NXattr`   |
| Internal/external link | `NXlink`, `NXlinkfield`, `NXlinkgroup` | `gonexus.NXlink`, `*gonexus.NXlinkfield`, `*gonexus.NXlinkgroup` |
| File handle  | `NXFile`                | `*gonexus.NXFile`   |
| `nxload`/`nxsave` | module functions   | `gonexus.Load` / `gonexus.Save` |

## Scope of this port

The upstream repository is organized as:

```
src/nexusformat/
  nexus/       <- ported here, as package gonexus
  definitions/ <- NeXus base-class XML schema definitions (not ported)
  scripts/     <- CLI tools (not ported)
```

Per the request that motivated this port, only `nexusformat/nexus` (the
`tree.py` data model, `NXFile` I/O, configuration, and the NeXus base-class
registry) is ported. `nexusformat.nexus.plot` (matplotlib plotting) and the
`definitions`/`scripts`/`examples`/notebooks are intentionally **not**
ported — plotting is out of scope for a headless Go library, and the base
class *definitions* (XML schema used for validation) can be added later as
a separate `gonexus/definitions` package if needed.

`nexusformat/nexus/tree.py` alone is roughly 8,700 lines covering lazy
per-field disk access, cross-process file locking, virtual datasets, and
~100 auto-generated NeXus base-class subclasses. Rather than a mechanical
line-for-line translation (which would fight Go's type system at every
turn), `gonexus` reimplements the same *data model and public API shape*
idiomatically in Go. See [Differences from Python](#differences-from-python-nexusformat)
below for the specific things that were simplified or left out, and why.

## Installation

```sh
go get github.com/CHESSComputing/gonexus
```

`gonexus` needs a working HDF5 C library (>= 1.8) available at build time,
because it uses [`gonum.org/v1/hdf5`](https://pkg.go.dev/gonum.org/v1/hdf5),
a cgo binding, to talk to HDF5 files — the same underlying library `h5py`
(and therefore Python's `nexusformat`) uses.

```sh
# Debian/Ubuntu
sudo apt-get install libhdf5-dev

# macOS (Homebrew)
brew install hdf5
```

Then, as with any cgo package that isn't in the default search path, you
may need to point CGO at your HDF5 install, e.g. on Apple Silicon Homebrew:

```sh
export CGO_CFLAGS="-I$(brew --prefix hdf5)/include"
export CGO_LDFLAGS="-L$(brew --prefix hdf5)/lib"
```

> **Note on this repository's build status:** this code was written and
> reviewed against the documented API of `gonum.org/v1/hdf5`, but the
> sandbox it was produced in has neither network access nor an HDF5
> installation, so it has **not** been compiled here. Before relying on it,
> run `go build ./...` and the smoke test in `example/` in an environment
> with HDF5 installed, and adjust `go.mod`'s module path (`github.com/CHESSComputing/gonexus`)
> to match your own repository.

## Quick start

### Reading a file

```go
package main

import (
	"fmt"
	"log"

	"github.com/CHESSComputing/gonexus/gonexus"
)

func main() {
	root, err := gonexus.Load("ARCS_7326.nxs")
	if err != nil {
		log.Fatal(err)
	}

	// Print the whole tree, like Python's `print(root.tree)`.
	fmt.Println(root.Tree())

	// Access by path, like Python's root['entry/run_number'].
	runNumber, err := root.GetField("entry/run_number")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("run number:", runNumber.Scalar())

	// Access by NeXus class, like Python's root.NXentry[0].
	for _, entry := range root.Component("NXentry") {
		fmt.Println("entry:", entry.NXName())
	}
}
```

### Building and writing a file

```go
package main

import (
	"log"
	"math"

	"github.com/CHESSComputing/gonexus/gonexus"
)

func main() {
	x := make([]float64, 101)
	y := make([]float64, 101)
	for i := range x {
		x[i] = 2 * math.Pi * float64(i) / 100
		y[i] = x[i]
	}
	z := make([]float64, len(x)*len(y))
	for i, yi := range y {
		for j, xj := range x {
			z[i*len(x)+j] = math.Sin(xj) * math.Sin(yi)
		}
	}
	zField := gonexus.NewField(z, "z")
	zField.Shape = []int{len(y), len(x)}

	// NewNXdata wires up the NeXus plotting convention (@signal, @axes)
	// automatically, like Python's NXdata(z, [x, y]).
	data := gonexus.NewNXdata(zField,
		gonexus.NewField(x, "x"),
		gonexus.NewField(y, "y"))

	entry := gonexus.NewNXentry()
	entry.Insert("data", data)

	sample := gonexus.NewNXsample()
	sample.InsertField("temperature", 40.0).SetUnits("K")
	entry.Insert("sample", sample)

	root := gonexus.NewNXroot(entry)

	if err := root.Save("example.nxs"); err != nil {
		log.Fatal(err)
	}
}
```

A runnable version of both of the above lives in [`example/main.go`](example/main.go).

## API guide

### Groups (`*gonexus.NXgroup`)

```go
g := gonexus.NewGroup("NXsample")            // like Python's NXsample()
g := gonexus.NewGroup("NXsample", "sample1")  // explicit name

g.Insert("temperature", gonexus.NewField(40.0))    // g['temperature'] = 40.0
f := g.InsertField("temperature", 40.0)             // shorthand, returns the field
g.Delete("temperature")                              // del g['temperature']

child := g.Child("temperature")                      // direct child only
obj, err := g.Get("instrument/detector/distance")    // full path, follows links
grp, err := g.GetGroup("instrument/detector")
field, err := g.GetField("instrument/detector/distance")

for _, e := range g.Entries() {                       // ordered iteration
    fmt.Println(e.Name, e.Object.NXClass())
}

detectors := g.Component("NXdetector") // every NXdetector anywhere below g

fmt.Println(g.Tree())  // full recursive pretty-print, like Python's `g.tree`
fmt.Println(g.Dir())   // one level, like Python's `g.dir()`
```

Convenience constructors exist for the most common NeXus base classes
(`NewNXroot`, `NewNXentry`, `NewNXdata`, `NewNXsample`, `NewNXinstrument`,
`NewNXdetector`, `NewNXmonitor`, `NewNXsource`, `NewNXuser`, `NewNXprocess`,
`NewNXnote`, `NewNXcollection`, `NewNXparameters`, `NewNXmonochromator`,
`NewNXcollimator`, `NewNXaperture`, `NewNXbeam`, `NewNXenvironment`,
`NewNXlog`, `NewNXgeometry`, `NewNXtransformations`, `NewNXsubentry`) — see
[`gonexus/classes.go`](gonexus/classes.go). For any of the ~100 other
classes defined by the NeXus standard, just call `NewGroup("NXwhatever")`
directly; it behaves identically.

### Fields (`*gonexus.NXfield`)

```go
f := gonexus.NewField(3.14)              // scalar float64 field
f := gonexus.NewField([]float64{1,2,3})  // 1-D array field, Shape inferred
f.Shape = []int{2, 3}                    // set explicitly for >1-D data
                                          // (Value stays a flat, row-major slice)

f.SetUnits("K")                    // f.attrs['units'] = 'K'
f.SetAttr("long_name", "Sample T") // arbitrary attribute
fmt.Println(f.Units())

scalar := f.Scalar()               // interface{}, nil if f is an array
floats, err := f.Float64()         // []float64, converting if needed
ints, err := f.Int64()             // []int64, converting if needed
```

### Attributes (`*gonexus.NXattr` / `*gonexus.AttrSet`)

Every group and field has an `NXAttrs() *AttrSet`:

```go
attrs := f.NXAttrs()
attrs.Set("units", "K")
val, ok := attrs.Get("units")
units := attrs.GetString("units") // "" if absent
attrs.Delete("units")
for _, name := range attrs.Keys() { ... } // insertion order
```

### Links

```go
link := gonexus.NewLinkField("distance", "/entry/instrument/detector/distance")
group.Insert("distance", link)
```

See [Known Limitations](#known-limitations) — reading external/soft links
and writing internal soft links both depend on HDF5 link APIs not exposed
by the pinned `gonum.org/v1/hdf5` binding version, so link support here is
best-effort.

### Top-level functions

| Python | Go |
|---|---|
| `nxload(filename, mode)` | `gonexus.Load(filename, mode...)` |
| `nxopen(filename, mode)` | `gonexus.Open(filename, mode...)` |
| `nxsave(filename, root, mode)` / `root.save(filename)` | `gonexus.Save(filename, root, mode...)` / `root.Save(filename, mode...)` |
| `nxduplicate(src, dst)` | `gonexus.Duplicate(src, dst)` |
| `nxgetconfig()` / `nxsetconfig(**kw)` | `gonexus.GetConfig()` / `gonexus.SetConfig(cfg)` |
| `nxgetcompression()` / `nxsetcompression()` | `gonexus.GetCompression()` / `gonexus.SetCompression()` |

File `mode` strings match Python's: `"r"` (default, read-only), `"rw"`/`"r+"`
(read-write, must exist), `"w"` (create/truncate), `"w-"`/`"x"` (create,
error if exists), `"a"` (read-write, create if missing).

## Differences from Python `nexusformat`

This is an idiomatic re-implementation, not a transliteration. The
significant, deliberate differences:

- **Eager loading only.** Python's `nxload` defaults to lazily loading only
  the first two tree levels, fetching the rest from disk on first access
  (`NX_CONFIG['recursive'] = False`). `gonexus.Load` always reads the whole
  file into memory in one pass (`Config.Recursive` exists for future use
  but currently has no effect). For very large files, open with
  `gonexus.Open` and add your own on-demand reads against the returned
  `*NXFile` if lazy access matters to you.
- **No file locking.** Python's `NXFile`/`NXLock` support optional
  cross-process advisory locking (`NX_LOCK` etc.) for concurrent access to
  a shared file. `gonexus` does not implement this; coordinate concurrent
  writers yourself.
- **No virtual datasets.** Python's `NXvirtualfield` (HDF5 VDS support) is
  not ported.
- **Static typing instead of dynamic attribute injection.** Python exposes
  every NeXus base class as its own `NXgroup` subclass with class-specific
  Python attribute access (e.g. `entry.sample.temperature`). Go has no
  runtime attribute injection, so `gonexus` represents every base class as
  a `*NXgroup` with `NXClass()` set accordingly, and you navigate with
  `Insert`/`Get`/`GetField`/`GetGroup` instead of dotted attribute access.
- **No NumPy-style arithmetic overloading.** Python `NXfield` objects
  support `+`, `-`, broadcasting, `numpy` ufunc interop, and slicing
  assignment. Go has no operator overloading; use `Float64()`/`Int64()` to
  get a plain slice and do arithmetic explicitly.
- **A narrower attribute type set on read.** `Field.Value`/`NXattr.Value`
  are decoded as one of `string`, `float64`, `int64`, `bool`, or a flat
  slice of one of those, rather than preserving every HDF5/NumPy dtype
  (`int8`, `uint16`, compound types, enums, ...) bit-for-bit. `Dtype`
  records the best-effort NumPy-style type name.
- **Limited attribute round-tripping (binding limitation).** The pinned
  `gonum.org/v1/hdf5` version does not expose HDF5's attribute-iteration
  API (`H5Aiterate`), only "open an attribute by name I already know."
  `gonexus` therefore only reads back attributes whose names are in the
  `wellKnownAttrs` list in
  [`gonexus/hdf5io.go`](gonexus/hdf5io.go) (`NX_class`, `signal`, `axes`,
  `units`, `long_name`, ... — the ones nexusformat itself writes most
  often). **Add your custom attribute names to that list** if your files
  carry attributes not on it, or switch to an HDF5 binding that exposes
  attribute enumeration (e.g. by extending `gonum.org/v1/hdf5` with a
  cgo call to `H5Aiterate2`) and swap it in — all such calls are isolated
  to `gonexus/hdf5io.go` for exactly this reason.
- **No true HDF5 soft-link writing (binding limitation).** Similarly, the
  pinned binding does not expose `H5Lcreate_soft`/`H5Lcreate_external`.
  `NewLinkField`/`NewLinkGroup` let you *build* a tree containing link
  placeholders, and reading recognizes existing links it cannot resolve as
  `NXlinkfield`/`NXlinkgroup` stubs, but `NXFile.WriteFile` currently
  returns an error if it encounters one to write, rather than silently
  writing a real copy or a broken link. Extend `createSoftLink` in
  `gonexus/hdf5io.go` if/when you add link-creation support to the binding
  layer.
- **No plotting.** `nexusformat.nexus.plot` (matplotlib) is out of scope,
  per the request that produced this port.
- **No `definitions`/`scripts`/notebooks.** Only the core `nexus` subpackage
  was ported, again per the request that produced this port.

## Package layout

```
gonexus/
  errors.go     NeXusError
  config.go     package-wide settings (compression, encoding, ...)
  attr.go       NXattr, AttrSet
  object.go     NXobject interface, shared base struct, path resolution
  field.go      NXfield
  group.go      NXgroup
  link.go       NXlink, NXlinkfield, NXlinkgroup
  classes.go    convenience constructors for common NeXus base classes
  convert.go    numeric conversion helpers (Float64()/Int64())
  util.go       dtype inference, value formatting, natural sort
  file.go       NXFile: open/close/read/write orchestration
  hdf5io.go     the only file that calls gonum.org/v1/hdf5 directly
  api.go        Load, Save, Open, Duplicate
example/
  main.go       runnable end-to-end demo (build + save + reload + inspect)
```

## License

This port follows the license of the original `nexusformat` project
(Modified BSD). See `LICENSE` for details; if this repository doesn't
already have one, copy in the BSD license text from
https://github.com/nexpy/nexusformat/blob/main/COPYING and update the
copyright line to reflect this Go port.
