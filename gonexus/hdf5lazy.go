package gonexus

// This file holds the lazy-loading code that calls into gonum.org/v1/hdf5.
// Like hdf5io.go it is a deliberately small, isolated surface.
//
// Binding API used here (as in upstream gonum.org/v1/hdf5):
//
//	(*Dataset).Space() *Dataspace
//	(*Dataspace).SelectHyperslab(offset, stride, count, block []uint) error
//	hdf5.CreateSimpleDataspace(dims, maxDims []uint) (*Dataspace, error)
//	(*Dataset).ReadSubset(data interface{}, memspace, filespace *Dataspace) error
//
// If the pinned CHESSComputing/hdf5 fork differs, adapt readSlab below -
// nothing else in the lazy code depends on those signatures.

import (
	"fmt"
	"reflect"

	"gonum.org/v1/hdf5"
)

// tryLazyField decides, for a dataset being read during ReadFile, whether
// it should stay on disk under the active LoadOptions. If so it returns a
// metadata-only NXfield (Value == nil, Dtype/Shape/attrs set) and true. If
// the dataset should be read eagerly it returns (nil, false, nil).
func (nf *NXFile) tryLazyField(ds *hdf5.Dataset, shape []int, name string, attrs *AttrSet) (*NXfield, bool, error) {
	if len(shape) == 0 {
		return nil, false, nil // scalars are always eager
	}
	dt, err := ds.Datatype()
	if err != nil {
		return nil, false, err
	}
	defer dt.Close()

	if dt.Class() == hdf5.T_STRING {
		return nil, false, nil // strings are always eager
	}
	goType := elementGoType(dt)
	if goType == nil {
		return nil, false, nil // let the eager path report the error
	}
	if nf.lazy.Mode == LazyAuto && numElements(shape) <= nf.lazy.Threshold {
		return nil, false, nil // small enough to just read
	}
	if err := checkElemSize(dt, int(goType.Size()), fmt.Sprintf("lazy dataset %q of Go type %v", name, goType)); err != nil {
		return nil, false, err
	}

	f := NewField(nil, name)
	f.Dtype = elemDtype(goType)
	f.Shape = shape
	f.attrs = attrs
	f.lazy = &lazySource{file: nf, goType: goType}
	return f, true, nil
}

// readLazyRange reads the flat window [offset, offset+count) of a lazy
// field from its file. offset/count have already been clamped.
func readLazyRange(f *NXfield, offset, count int) (interface{}, error) {
	src := f.lazy
	h5mu.Lock()
	defer h5mu.Unlock()

	if src.file == nil || src.file.h5 == nil {
		return nil, newError("cannot read field %q: its file has been closed", f.name)
	}
	path := f.NXPath()
	ds, err := src.file.h5.OpenDataset(path)
	if err != nil {
		return nil, newError("could not open dataset %q: %v", path, err)
	}
	defer ds.Close()

	// Re-validate against what the file says now, so a replaced or
	// changed file cannot make us read with the wrong element size or
	// geometry (which would corrupt memory across the cgo boundary).
	dt, err := ds.Datatype()
	if err != nil {
		return nil, newError("could not read datatype of %q: %v", path, err)
	}
	defer dt.Close()
	if err := checkElemSize(dt, int(src.goType.Size()), fmt.Sprintf("dataset %q", path)); err != nil {
		return nil, err
	}
	shape, err := datasetShape(ds)
	if err != nil {
		return nil, err
	}
	if !equalInts(shape, f.Shape) {
		return nil, newError("dataset %q changed shape since it was opened (%v -> %v)", path, f.Shape, shape)
	}

	return readFlatRange(ds, shape, src.goType, offset, count)
}

// readFlatRange allocates a []goType of length count and fills it with the
// flat row-major element window [offset, offset+count) of ds, using one
// hyperslab read per slab from flatSlabs.
func readFlatRange(ds *hdf5.Dataset, shape []int, goType reflect.Type, offset, count int) (interface{}, error) {
	sliceType := reflect.SliceOf(goType)
	out := reflect.MakeSlice(sliceType, count, count)
	pos := 0
	for _, sl := range flatSlabs(shape, offset, count) {
		if err := readSlab(ds, sliceType, out.Slice(pos, pos+sl.n), sl); err != nil {
			return nil, err
		}
		pos += sl.n
	}
	return out.Interface(), nil
}

// readSlab reads one hyperslab of ds into dst (a slice value of exactly
// sl.n elements, aliasing part of the caller's output buffer).
func readSlab(ds *hdf5.Dataset, sliceType reflect.Type, dst reflect.Value, sl slab) error {
	// A fresh file dataspace per slab, so the selection never depends on
	// whether SelectHyperslab replaces or adds to a previous selection.
	fspace := ds.Space()
	defer fspace.Close()

	ones := onesUints(len(sl.start))
	if err := fspace.SelectHyperslab(sl.start, ones, sl.count, ones); err != nil {
		return newError("selecting hyperslab %v+%v: %v", sl.start, sl.count, err)
	}
	mspace, err := hdf5.CreateSimpleDataspace([]uint{uint(sl.n)}, nil)
	if err != nil {
		return newError("creating memory dataspace: %v", err)
	}
	defer mspace.Close()

	// Same calling convention as the eager read in readDatasetValue: a
	// pointer to a slice header whose backing array receives the data.
	ptr := reflect.New(sliceType)
	ptr.Elem().Set(dst)
	if err := ds.ReadSubset(ptr.Interface(), mspace, fspace); err != nil {
		return newError("reading hyperslab %v+%v: %v", sl.start, sl.count, err)
	}
	return nil
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
