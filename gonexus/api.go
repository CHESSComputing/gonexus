package gonexus

// Load opens the named NeXus/HDF5 file, reads it entirely into memory as a
// tree of NXgroup/NXfield objects, closes the file, and returns the root
// NXroot group. mode defaults to "r" (read-only) if omitted; pass "rw" to
// keep the tree modifiable and re-saveable in place. Equivalent to
// Python's `nxload(filename, mode)`. Load always reads all data into
// memory; for very large files see LoadWithOptions (lazy loading).
//
//	root, err := gonexus.Load("scan.nxs")
//	if err != nil { ... }
//	fmt.Println(root.Tree())
//	entry, _ := root.GetGroup("entry")
func Load(filename string, mode ...string) (*NXgroup, error) {
	m := "r"
	if len(mode) > 0 {
		m = mode[0]
	}
	h5mu.Lock() // libhdf5 is not assumed to be thread-safe; see lazy.go
	defer h5mu.Unlock()
	nf, err := OpenFile(filename, m)
	if err != nil {
		return nil, err
	}
	defer nf.Close()
	return nf.ReadFile()
}

// Open opens a NeXus/HDF5 file and returns the live *NXFile handle without
// reading its contents, for callers that want fine-grained control (e.g.
// calling ReadFile() themselves, or writing incrementally). The caller is
// responsible for calling Close(). Equivalent to Python's `nxopen()`.
func Open(filename string, mode ...string) (*NXFile, error) {
	m := "r"
	if len(mode) > 0 {
		m = mode[0]
	}
	return OpenFile(filename, m)
}

// Save writes an in-memory NeXus tree (as built with NewNXroot and friends,
// or loaded via Load) to a new HDF5 file at filename. mode defaults to "w"
// (create/truncate); pass "w-" or "x" to fail if the file already exists.
// Equivalent to Python's `nxsave(filename, root, mode)` / `root.save(filename)`.
//
//	root := gonexus.NewNXroot(gonexus.NewNXentry(
//	    gonexus.NewNXdata(gonexus.NewField(z, "z")),
//	))
//	if err := gonexus.Save("out.nxs", root); err != nil { ... }
func Save(filename string, root *NXgroup, mode ...string) error {
	m := "w"
	if len(mode) > 0 {
		m = mode[0]
	}
	nf, err := OpenFile(filename, m)
	if err != nil {
		return err
	}
	defer nf.Close()
	return nf.WriteFile(root)
}

// Duplicate copies a NeXus file from src to dst by reading it fully into
// memory and writing it back out, which also has the effect of
// consolidating any external links encountered along the way into a
// flat copy where they resolved successfully. Equivalent to Python's
// `nxduplicate(src, dst)`.
func Duplicate(src, dst string) error {
	root, err := Load(src)
	if err != nil {
		return err
	}
	return Save(dst, root)
}

// Note: streaming a NeXus tree from an io.Reader is intentionally not
// supported. NeXus files are HDF5 containers, which are not
// stream-oriented, so gonexus (like Python's nexusformat) always works
// against a named file on disk.
