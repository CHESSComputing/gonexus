package gonexus

// Lazy loading.
//
// By default gonexus.Load reads every dataset in the file into memory
// (Value holds the whole array). For very large files that is not
// affordable, so LoadWithOptions offers two additional, strictly opt-in
// modes in which big datasets stay on disk and are read on demand:
//
//	LazyOff   - today's behavior: everything is read eagerly (default).
//	LazyAuto  - datasets with more than Threshold elements stay on disk;
//	            smaller ones are read eagerly.
//	LazyAll   - every non-scalar, non-string dataset stays on disk.
//
// A lazy NXfield has Value == nil but a valid Dtype, Shape and attributes,
// so tree/metadata operations work exactly as before. Its data are read
// with ReadSlice (a flat, row-major window - the same offset/limit
// contract the HTTP server uses) or all at once with ReadData.
//
// Because lazy fields read from the file later, the file stays open for as
// long as the tree is in use: call Close on the root group when done.
//
// All HDF5 calls made through this API are serialized by one package-wide
// mutex, since libhdf5 is normally built without thread safety.
//
// Scalars and string datasets are always read eagerly (they are small).
//
// This file is binding-free; everything that touches gonum/hdf5 lives in
// hdf5lazy.go.

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// h5mu serializes every call into the HDF5 C library made by the lazy
// loading code path (and by Load / LoadWithOptions).
var h5mu sync.Mutex

// LazyMode selects how LoadWithOptions treats dataset contents.
type LazyMode int

const (
	// LazyOff reads all data eagerly (the behavior of Load).
	LazyOff LazyMode = iota
	// LazyAuto leaves datasets larger than LoadOptions.Threshold on disk.
	LazyAuto
	// LazyAll leaves every non-scalar numeric/bool dataset on disk.
	LazyAll
)

// DefaultLazyThreshold is the element-count threshold used by LazyAuto
// when LoadOptions.Threshold is not set.
const DefaultLazyThreshold = 100_000

func (m LazyMode) String() string {
	switch m {
	case LazyOff:
		return "off"
	case LazyAuto:
		return "auto"
	case LazyAll:
		return "all"
	}
	return fmt.Sprintf("LazyMode(%d)", int(m))
}

// ParseLazyMode converts "off", "auto" or "all" (case-insensitive; "" means
// "off") into a LazyMode.
func ParseLazyMode(s string) (LazyMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "off", "false", "no", "0":
		return LazyOff, nil
	case "auto":
		return LazyAuto, nil
	case "all", "on", "true", "yes", "1":
		return LazyAll, nil
	}
	return LazyOff, newError("invalid lazy mode %q (want off, auto or all)", s)
}

// LoadOptions configures LoadWithOptions.
type LoadOptions struct {
	// Mode is LazyOff (default), LazyAuto or LazyAll.
	Mode LazyMode
	// Threshold is the element count above which LazyAuto keeps a dataset
	// on disk. Values <= 0 mean DefaultLazyThreshold. Ignored by the other
	// modes.
	Threshold int
}

// lazySource is attached to an NXfield whose data have not been read.
type lazySource struct {
	file   *NXFile
	goType reflect.Type // element type used for reads
}

// LoadWithOptions is like Load but honors opts.Mode. With LazyOff it is
// identical to Load(filename) and the file is closed before returning.
// With LazyAuto or LazyAll the returned tree keeps the file open so lazy
// fields can be read later; the caller must call root.Close() when done.
func LoadWithOptions(filename string, opts LoadOptions) (*NXgroup, error) {
	if opts.Mode == LazyOff {
		return Load(filename)
	}
	if opts.Mode == LazyAuto && opts.Threshold <= 0 {
		opts.Threshold = DefaultLazyThreshold
	}

	h5mu.Lock()
	defer h5mu.Unlock()

	nf, err := OpenFile(filename, "r")
	if err != nil {
		return nil, err
	}
	nf.lazy = opts
	root, err := nf.ReadFile()
	if err != nil {
		nf.Close()
		return nil, err
	}
	return root, nil
}

// Close releases the HDF5 file held open by a lazily loaded tree. It is
// safe to call on any group (a no-op unless it is the root of a tree
// returned by LoadWithOptions), and safe to call more than once. After
// Close, ReadSlice/ReadData on the tree's lazy fields return an error;
// eagerly loaded values stay usable.
func (g *NXgroup) Close() error {
	if g == nil || g.file == nil {
		return nil
	}
	h5mu.Lock()
	defer h5mu.Unlock()
	return g.file.Close()
}

// Lazy reports whether the field's data are still on disk (Value is nil).
func (f *NXfield) Lazy() bool { return f.lazy != nil }

// Size returns the total number of elements (1 for a scalar).
func (f *NXfield) Size() int { return numElements(f.Shape) }

// ReadSlice returns the flat, row-major element window
// [offset, offset+count) as a typed slice ([]float64, []int32, ...).
// offset is clamped to [0, Size()]; a negative count, or one running past
// the end, is shortened to what is left. Multi-dimensional fields are
// indexed as a flat C-order array: row r of an (R x C) field is
// ReadSlice(r*C, C).
//
// For a lazy field only the requested window is read from disk. For an
// eagerly loaded field the result is a sub-slice of Value (not a copy).
// Scalars have no slice; use Scalar.
func (f *NXfield) ReadSlice(offset, count int) (interface{}, error) {
	if len(f.Shape) == 0 {
		return nil, newError("field %q is a scalar; use Scalar()", f.name)
	}
	total := f.Size()
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	if count < 0 || offset+count > total {
		count = total - offset
	}

	if f.lazy == nil {
		rv := reflect.ValueOf(f.Value)
		if !rv.IsValid() || rv.Kind() != reflect.Slice {
			return nil, newError("field %q has no array data", f.name)
		}
		return rv.Slice(offset, offset+count).Interface(), nil
	}
	return readLazyRange(f, offset, count)
}

// ReadData reads a lazy field's entire contents into Value, after which
// the field behaves like an eagerly loaded one. It is a no-op for fields
// that are not lazy. The caller is responsible for the memory this takes,
// and must not call it concurrently with other use of the same field.
func (f *NXfield) ReadData() error {
	if f.lazy == nil {
		return nil
	}
	v, err := f.ReadSlice(0, f.Size())
	if err != nil {
		return err
	}
	f.Value = v
	f.lazy = nil
	return nil
}

func (f *NXfield) errIfLazy(what string) error {
	if f.lazy != nil {
		return newError("field %q is lazily loaded; call ReadData() or ReadSlice() before %s", f.name, what)
	}
	return nil
}

// ---------------------------------------------------------------------
// Flat range -> hyperslabs
// ---------------------------------------------------------------------

// slab is one rectangular HDF5 hyperslab: start/count per dimension, and
// the number of elements it covers.
type slab struct {
	start []uint
	count []uint
	n     int
}

// flatSlabs decomposes the flat, row-major element range
// [offset, offset+count) of an array with the given shape into the
// minimal list of rectangular hyperslabs, in ascending flat order. The
// slabs, read one after the other, concatenate to exactly that range.
// There are at most 2*(ndim-1)+1 of them: a partial row at the start, a
// block of whole rows, and a partial row at the end, recursively. This
// keeps memory proportional to count (never to a whole row/plane).
func flatSlabs(shape []int, offset, count int) []slab {
	if count <= 0 || len(shape) == 0 {
		return nil
	}
	var out []slab
	flatSlabsRec(shape, 0, nil, offset, count, &out)
	return out
}

func flatSlabsRec(shape []int, level int, prefix []uint, off, n int, out *[]slab) {
	nd := len(shape)
	if level == nd-1 {
		start := append(copyUints(prefix), uint(off))
		cnt := append(onesUints(len(prefix)), uint(n))
		*out = append(*out, slab{start: start, count: cnt, n: n})
		return
	}

	inner := 1 // elements per index of dimension `level`
	for _, d := range shape[level+1:] {
		inner *= d
	}
	i := off / inner
	within := off % inner

	if within != 0 { // partial first row
		m := inner - within
		if m > n {
			m = n
		}
		flatSlabsRec(shape, level+1, append(copyUints(prefix), uint(i)), within, m, out)
		n -= m
		i++
	}
	if k := n / inner; k > 0 { // whole rows in one block
		start := append(copyUints(prefix), uint(i))
		cnt := append(onesUints(len(prefix)), uint(k))
		for _, d := range shape[level+1:] {
			start = append(start, 0)
			cnt = append(cnt, uint(d))
		}
		*out = append(*out, slab{start: start, count: cnt, n: k * inner})
		n -= k * inner
		i += k
	}
	if n > 0 { // partial last row
		flatSlabsRec(shape, level+1, append(copyUints(prefix), uint(i)), 0, n, out)
	}
}

func copyUints(a []uint) []uint {
	out := make([]uint, len(a), len(a)+len(a)+2)
	copy(out, a)
	return out
}

func onesUints(n int) []uint {
	out := make([]uint, n, n+1)
	for i := range out {
		out[i] = 1
	}
	return out
}
