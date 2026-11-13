package gonexus

import (
	"fmt"
	"os"
	"time"

	"gonum.org/v1/hdf5"
)

// NXFile wraps an HDF5 file handle and knows how to read/write a NeXus tree
// (NXgroup/NXfield/NXattr/NXlink) to and from it. It is the Go equivalent
// of Python's NXFile class, minus file locking - see the package README for the list of
// intentional differences. Lazy (on-demand) reading of large datasets is
// opt-in through LoadWithOptions; ReadFile itself reads eagerly unless the
// file was opened that way.
//
// Most callers do not need NXFile directly; use the top-level Load/Save/
// Open helpers in api.go instead.
type NXFile struct {
	Name string
	mode string // "r" or "rw"
	h5   *hdf5.File

	// lazy is set by LoadWithOptions before ReadFile; the zero value
	// (LazyOff) means "read everything eagerly".
	lazy LoadOptions
}

// OpenFile opens (or, for write modes, creates) an HDF5/NeXus file. mode is
// one of "r" (read-only, default), "rw"/"r+" (read-write, must exist), "w"
// (create, truncating if it exists), "w-"/"x" (create, error if it exists),
// or "a" (read-write, creating if necessary). Equivalent to Python's
// `NXFile(name, mode)`.
func OpenFile(name string, mode string) (*NXFile, error) {
	if mode == "" {
		mode = "r"
	}
	nf := &NXFile{Name: name}

	switch mode {
	case "r":
		f, err := hdf5.OpenFile(name, hdf5.F_ACC_RDONLY)
		if err != nil {
			return nil, newError("could not open %q for reading: %v", name, err)
		}
		nf.h5, nf.mode = f, "r"
	case "r+", "rw":
		f, err := hdf5.OpenFile(name, hdf5.F_ACC_RDWR)
		if err != nil {
			return nil, newError("could not open %q for read/write: %v", name, err)
		}
		nf.h5, nf.mode = f, "rw"
	case "a":
		if _, statErr := os.Stat(name); statErr == nil {
			f, err := hdf5.OpenFile(name, hdf5.F_ACC_RDWR)
			if err != nil {
				return nil, newError("could not open %q for read/write: %v", name, err)
			}
			nf.h5, nf.mode = f, "rw"
		} else {
			f, err := hdf5.CreateFile(name, hdf5.F_ACC_TRUNC)
			if err != nil {
				return nil, newError("could not create %q: %v", name, err)
			}
			nf.h5, nf.mode = f, "rw"
		}
	case "w":
		f, err := hdf5.CreateFile(name, hdf5.F_ACC_TRUNC)
		if err != nil {
			return nil, newError("could not create %q: %v", name, err)
		}
		nf.h5, nf.mode = f, "rw"
	case "w-", "x":
		if _, statErr := os.Stat(name); statErr == nil {
			return nil, newError("%q already exists", name)
		}
		f, err := hdf5.CreateFile(name, hdf5.F_ACC_EXCL)
		if err != nil {
			return nil, newError("could not create %q: %v", name, err)
		}
		nf.h5, nf.mode = f, "rw"
	default:
		return nil, newError("invalid file mode %q", mode)
	}
	return nf, nil
}

// Close releases the underlying HDF5 file handle.
func (nf *NXFile) Close() error {
	if nf.h5 == nil {
		return nil
	}
	err := nf.h5.Close()
	nf.h5 = nil
	if err != nil {
		return newError("error closing %q: %v", nf.Name, err)
	}
	return nil
}

// rootGroup opens "/" as an *hdf5.Group. Attribute methods in gonum/hdf5
// are only defined on *hdf5.Group and *hdf5.Dataset (not on *hdf5.File
// itself), so root-level attributes (file_name, file_time, NX_class, ...)
// must go through this. Callers must Close() the returned group.
func (nf *NXFile) rootGroup() (*hdf5.Group, error) {
	g, err := nf.h5.OpenGroup("/")
	if err != nil {
		return nil, newError("could not open root group of %q: %v", nf.Name, err)
	}
	return g, nil
}

// ---------------------------------------------------------------------
// Reading
// ---------------------------------------------------------------------

// ReadFile reads the entire HDF5 file into a tree of NXgroup/NXfield
// objects rooted at an NXroot group, and returns it. Equivalent to
// Python's `NXFile.readfile()`. gonexus always reads eagerly/recursively
// (see Config.Recursive in config.go).
func (nf *NXFile) ReadFile() (*NXgroup, error) {
	if nf.h5 == nil {
		return nil, newError("file %q is not open", nf.Name)
	}
	rg, err := nf.rootGroup()
	if err != nil {
		return nil, err
	}
	defer rg.Close()

	root := NewGroup("NXroot", "root")
	root.file = nf
	if err := nf.readAttrsInto(rg, root.attrs); err != nil {
		return nil, err
	}
	root.attrs.Delete("NX_class") // redundant with root.class

	names, err := groupChildNames(rg)
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		child, err := nf.readObject(rg, name)
		if err != nil {
			return nil, err
		}
		root.Insert(name, child)
	}
	return root, nil
}

func (nf *NXFile) readObject(parent container, name string) (NXobject, error) {
	if g, err := openChildGroup(parent, name); err == nil {
		return nf.readGroup(g, name)
	}
	if d, err := openChildDataset(parent, name); err == nil {
		return nf.readDataset(d, name)
	}
	// Could not resolve as a group or dataset - treat as a dangling or
	// external HDF5 link, which this binding cannot resolve directly
	// (see hdf5io.go). Recorded as an unresolved link stub rather than
	// failing the whole read.
	return &NXlinkfield{
		NXfield: NewField(nil, name),
		Link:    &NXlink{Target: name},
	}, nil
}

func (nf *NXFile) readGroup(h5g *hdf5.Group, name string) (*NXgroup, error) {
	defer h5g.Close()
	attrs := NewAttrSet()
	if err := nf.readAttrsInto(h5g, attrs); err != nil {
		return nil, err
	}
	class := "NXgroup"
	if a, ok := attrs.Get("NX_class"); ok {
		if s, ok := a.Value.(string); ok && s != "" {
			class = s
		}
		attrs.Delete("NX_class")
	}
	g := NewGroup(class, name)
	g.attrs = attrs

	names, err := groupChildNames(h5g)
	if err != nil {
		return nil, err
	}
	for _, childName := range names {
		child, err := nf.readObject(h5g, childName)
		if err != nil {
			return nil, err
		}
		g.Insert(childName, child)
	}
	return g, nil
}

func (nf *NXFile) readDataset(ds *hdf5.Dataset, name string) (*NXfield, error) {
	defer ds.Close()

	attrs := NewAttrSet()
	if err := nf.readAttrsInto(ds, attrs); err != nil {
		return nil, err
	}

	shape, err := datasetShape(ds)
	if err != nil {
		return nil, err
	}

	if nf.lazy.Mode != LazyOff {
		lf, ok, err := nf.tryLazyField(ds, shape, name, attrs)
		if err != nil {
			return nil, newError("could not open dataset %q lazily: %v", name, err)
		}
		if ok {
			return lf, nil
		}
	}

	value, dtype, err := readDatasetValue(ds, shape, name)
	if err != nil {
		if isUnsupportedVlenString(err) {
			// Field exists but this binding can't decode it yet - keep
			// the rest of the tree loading rather than failing outright.
			f := NewField(fmt.Sprintf("<unreadable: %s>", err), name)
			f.Dtype = "string"
			f.Shape = shape
			f.attrs = attrs
			return f, nil
		}
		return nil, newError("could not read dataset %q: %v", name, err)
	}

	f := NewField(value, name)
	f.Dtype = dtype
	f.Shape = shape
	f.attrs = attrs
	return f, nil
}

// ---------------------------------------------------------------------
// Writing
// ---------------------------------------------------------------------

// WriteFile writes an in-memory NeXus tree (rooted at an NXroot group, as
// returned by NewNXroot) to the underlying HDF5 file, which is assumed to
// be empty/newly created. Equivalent to Python's `NXFile.writefile(root)`.
func (nf *NXFile) WriteFile(root *NXgroup) error {
	if nf.h5 == nil {
		return newError("file %q is not open", nf.Name)
	}
	if nf.mode != "rw" {
		return newError("file %q was not opened for writing", nf.Name)
	}
	root.attrs.Set("HDF5_Version", hdf5Version())
	root.attrs.Set("NeXus_version", "gonexus")
	root.attrs.Set("file_name", nf.Name)
	root.attrs.Set("file_time", time.Now().Format(time.RFC3339))

	rg, err := nf.rootGroup()
	if err != nil {
		return err
	}
	defer rg.Close()

	if err := nf.writeAttrs(rg, root.attrs); err != nil {
		return err
	}
	for _, e := range root.Entries() {
		if err := nf.writeObject(rg, e.Name, e.Object); err != nil {
			return err
		}
	}
	root.file = nf
	return nil
}

func (nf *NXFile) writeObject(parent container, name string, obj NXobject) error {
	switch o := obj.(type) {
	case *NXgroup:
		return nf.writeGroup(parent, name, o)
	case *NXlinkgroup:
		return nf.writeGroup(parent, name, o.NXgroup)
	case *NXfield:
		return nf.writeField(parent, name, o)
	case *NXlinkfield:
		return createSoftLink(parent, name, o.Link.Target)
	default:
		return newError("unsupported object type for %q: %T", name, obj)
	}
}

func (nf *NXFile) writeGroup(parent container, name string, g *NXgroup) error {
	h5g, err := createChildGroup(parent, name)
	if err != nil {
		return newError("could not create group %q: %v", name, err)
	}
	defer h5g.Close()

	attrs := g.attrs.Clone()
	attrs.Set("NX_class", g.class)
	if err := nf.writeAttrs(h5g, attrs); err != nil {
		return err
	}
	for _, e := range g.Entries() {
		if err := nf.writeObject(h5g, e.Name, e.Object); err != nil {
			return err
		}
	}
	return nil
}

func (nf *NXFile) writeField(parent container, name string, f *NXfield) error {
	if f.lazy != nil {
		return newError("could not write field %q: it is lazily loaded; call ReadData() first", name)
	}
	ds, err := writeDatasetValue(parent, name, f.Value, f.Shape)
	if err != nil {
		return newError("could not write field %q: %v", name, err)
	}
	defer ds.Close()
	return nf.writeAttrs(ds, f.attrs)
}

func hdf5Version() string {
	v, err := hdf5.LibVersion()
	if err != nil {
		return "unknown"
	}
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Release)
}
