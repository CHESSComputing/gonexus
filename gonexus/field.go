package gonexus

import (
	"fmt"
)

// NXfield represents a single piece of data in a NeXus tree - the Go
// equivalent of an HDF5 dataset and of Python's NXfield class. Fields carry
// a value (scalar or array), a dtype, a shape, and a set of attributes such
// as "units".
//
// Supported Value types: bool, string, int8/16/32/64, uint8/16/32/64,
// float32/64, and slices of those for 1-D arrays. For multi-dimensional
// arrays, store the data flattened (row-major / C order) in Value and set
// Shape explicitly, e.g.:
//
//	f := gonexus.NewField([]float64{1, 2, 3, 4, 5, 6})
//	f.Shape = []int{2, 3}
type NXfield struct {
	base
	// Value holds the field's data. See type docs for supported types.
	Value interface{}
	// Dtype is the NeXus/NumPy-style type name, inferred from Value
	// unless set explicitly (e.g. after NewField()).
	Dtype string
	// Shape is the array shape. A nil or empty Shape means a scalar
	// field. For 1-D slices, NewField infers Shape automatically.
	Shape []int

	// lazy is non-nil while the field's data are still on disk (Value is
	// nil). See LoadWithOptions, ReadSlice and ReadData in lazy.go.
	lazy *lazySource
}

// NewField creates an NXfield from a raw Go value, inferring Dtype and
// (for 1-D slices) Shape automatically. This is the Go equivalent of
// Python's `NXfield(value)` constructor.
func NewField(value interface{}, name ...string) *NXfield {
	n := ""
	if len(name) > 0 {
		n = name[0]
	}
	dtype, shape := inferDtype(value)
	return &NXfield{
		base:  newBase(n),
		Value: value,
		Dtype: dtype,
		Shape: shape,
	}
}

// NXClass always returns "NXfield" for fields, matching Python's
// NXfield.nxclass.
func (f *NXfield) NXClass() string { return "NXfield" }

// NXPath returns the field's absolute path in its tree.
func (f *NXfield) NXPath() string { return nxPath(f) }

// SetUnits is a convenience for the very common `field.attrs['units'] = ...`
// pattern.
func (f *NXfield) SetUnits(units string) *NXfield {
	f.attrs.Set("units", units)
	return f
}

// Units returns the field's "units" attribute, or "" if unset.
func (f *NXfield) Units() string {
	return f.attrs.GetString("units")
}

// SetAttr sets an arbitrary attribute and returns the field, for chaining:
//
//	entry.Sample.Insert("temperature", gonexus.NewField(40.0).SetAttr("units", "K"))
func (f *NXfield) SetAttr(name string, value interface{}) *NXfield {
	f.attrs.Set(name, value)
	return f
}

// Scalar returns the field's value as a plain Go interface{}, e.g. for use
// in arithmetic. It panics-free returns nil for array fields; use Value
// directly for arrays.
func (f *NXfield) Scalar() interface{} {
	if len(f.Shape) > 0 {
		return nil
	}
	return f.Value
}

// Float64 returns the field's data as a []float64, converting from the
// underlying numeric type if necessary. It returns an error if the field
// does not hold numeric data.
func (f *NXfield) Float64() ([]float64, error) {
	if err := f.errIfLazy("converting to float64"); err != nil {
		return nil, err
	}
	return toFloat64Slice(f.Value)
}

// Int64 returns the field's data as a []int64, converting from the
// underlying integer type if necessary.
func (f *NXfield) Int64() ([]int64, error) {
	if err := f.errIfLazy("converting to int64"); err != nil {
		return nil, err
	}
	return toInt64Slice(f.Value)
}

// String returns the field's value formatted as a string (for scalars) or
// as a bracketed list (for arrays), matching Python's NXfield.__repr__ used
// in tree output.
func (f *NXfield) String() string {
	if f.lazy != nil {
		return fmt.Sprintf("<lazy %s(%s)>", f.Dtype, shapeString(f.Shape))
	}
	return formatValue(f.Value)
}

func (f *NXfield) treeLines(prefix string, depth int) []string {
	indent := indentString(depth)
	var line string
	if len(f.Shape) > 0 {
		line = fmt.Sprintf("%s%s = %s(%s)", indent, f.name, f.Dtype, shapeString(f.Shape))
	} else {
		line = fmt.Sprintf("%s%s = %s", indent, f.name, formatValue(f.Value))
	}
	lines := []string{line}
	attrIndent := indentString(depth + 1)
	for _, k := range f.attrs.Keys() {
		a, _ := f.attrs.Get(k)
		lines = append(lines, fmt.Sprintf("%s@%s = %v", attrIndent, k, a.Value))
	}
	return lines
}

func indentString(depth int) string {
	s := ""
	for i := 0; i < depth; i++ {
		s += "  "
	}
	return s
}
