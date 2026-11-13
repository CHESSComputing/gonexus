package gonexus

import (
	"path/filepath"
	"reflect"
	"testing"
)

// gatherFlat emulates what the HDF5 hyperslab reads do: for each slab,
// copy the selected rectangle of a row-major array, in row-major order.
func gatherFlat(data []int, shape []int, sl slab) []int {
	nd := len(shape)
	strides := make([]int, nd)
	s := 1
	for i := nd - 1; i >= 0; i-- {
		strides[i] = s
		s *= shape[i]
	}
	var out []int
	idx := make([]int, nd)
	var rec func(d int)
	rec = func(d int) {
		if d == nd {
			pos := 0
			for k := 0; k < nd; k++ {
				pos += (int(sl.start[k]) + idx[k]) * strides[k]
			}
			out = append(out, data[pos])
			return
		}
		for i := 0; i < int(sl.count[d]); i++ {
			idx[d] = i
			rec(d + 1)
		}
	}
	rec(0)
	return out
}

func TestFlatSlabsMatchFlatRange(t *testing.T) {
	shapes := [][]int{{7}, {3, 4}, {2, 3, 4}, {2, 2, 3, 2}, {1, 5}, {5, 1}}
	for _, shape := range shapes {
		total := numElements(shape)
		data := make([]int, total)
		for i := range data {
			data[i] = i
		}
		for off := 0; off <= total; off++ {
			for cnt := 0; cnt <= total-off; cnt++ {
				var got []int
				slabs := flatSlabs(shape, off, cnt)
				if max := 2*(len(shape)-1) + 1; len(slabs) > max {
					t.Fatalf("shape %v off %d cnt %d: %d slabs, want <= %d", shape, off, cnt, len(slabs), max)
				}
				n := 0
				for _, sl := range slabs {
					got = append(got, gatherFlat(data, shape, sl)...)
					n += sl.n
				}
				if n != cnt || !reflect.DeepEqual(append([]int{}, got...), append([]int{}, data[off:off+cnt]...)) {
					if !(cnt == 0 && len(got) == 0) {
						t.Fatalf("shape %v off %d cnt %d: got %v want %v", shape, off, cnt, got, data[off:off+cnt])
					}
				}
			}
		}
	}
}

// writeLazyFixture saves a small file with 1-D, 2-D and 3-D arrays, a
// scalar and a string, and returns its path.
func writeLazyFixture(t *testing.T) string {
	t.Helper()
	f64 := make([]float64, 1000)
	for i := range f64 {
		f64[i] = float64(i) * 0.5
	}
	i32 := make([]int32, 50*20)
	for i := range i32 {
		i32[i] = int32(i * 3)
	}
	u16 := make([]uint16, 4*5*6)
	for i := range u16 {
		u16[i] = uint16(i + 1)
	}
	small := make([]float64, 10)
	for i := range small {
		small[i] = float64(i)
	}

	vf := NewField(f64, "v")
	mf := NewField(i32, "m")
	mf.Shape = []int{50, 20}
	cf := NewField(u16, "c")
	cf.Shape = []int{4, 5, 6}
	cf.SetUnits("counts")

	data := NewGroup("NXdata", "data")
	data.Insert("v", vf)
	data.Insert("m", mf)
	data.Insert("c", cf)
	data.Insert("small", NewField(small, "small"))
	data.Insert("scalar", NewField(3.5, "scalar"))
	data.Insert("label", NewField("hello", "label"))
	entry := NewGroup("NXentry", "entry")
	entry.Insert("data", data)
	root := NewNXroot(entry)

	path := filepath.Join(t.TempDir(), "lazy.nxs")
	if err := Save(path, root); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return path
}

func fieldAt(t *testing.T, root *NXgroup, path string) *NXfield {
	t.Helper()
	f, err := root.GetField(path)
	if err != nil {
		t.Fatalf("GetField(%q): %v", path, err)
	}
	return f
}

func TestLazyModes(t *testing.T) {
	path := writeLazyFixture(t)

	eager, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	cases := []struct {
		name string
		opts LoadOptions
		lazy map[string]bool
	}{
		{"off", LoadOptions{Mode: LazyOff}, map[string]bool{"v": false, "m": false, "c": false, "small": false, "scalar": false, "label": false}},
		{"auto", LoadOptions{Mode: LazyAuto, Threshold: 100}, map[string]bool{"v": true, "m": true, "c": true, "small": false, "scalar": false, "label": false}},
		{"auto-default-threshold", LoadOptions{Mode: LazyAuto}, map[string]bool{"v": false, "m": false, "c": false, "small": false, "scalar": false, "label": false}},
		{"all", LoadOptions{Mode: LazyAll}, map[string]bool{"v": true, "m": true, "c": true, "small": true, "scalar": false, "label": false}},
	}

	windows := [][2]int{{0, 1}, {0, 7}, {3, 40}, {19, 22}, {118, 2}, {0, 120}, {995, 100}, {-5, 4}, {5000, 3}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, err := LoadWithOptions(path, tc.opts)
			if err != nil {
				t.Fatalf("LoadWithOptions: %v", err)
			}
			defer root.Close()

			// Metadata must be identical to the eager tree.
			if got, want := root.Tree(), eager.Tree(); got != want {
				t.Fatalf("Tree differs:\n--- lazy\n%s\n--- eager\n%s", got, want)
			}

			for name, wantLazy := range tc.lazy {
				fp := "entry/data/" + name
				lf, ef := fieldAt(t, root, fp), fieldAt(t, eager, fp)
				if lf.Lazy() != wantLazy {
					t.Errorf("%s: Lazy() = %v, want %v", name, lf.Lazy(), wantLazy)
				}
				if lf.Dtype != ef.Dtype || !reflect.DeepEqual(lf.Shape, ef.Shape) {
					t.Errorf("%s: dtype/shape %s%v, want %s%v", name, lf.Dtype, lf.Shape, ef.Dtype, ef.Shape)
				}
				if lf.Units() != ef.Units() {
					t.Errorf("%s: units %q, want %q", name, lf.Units(), ef.Units())
				}
				if wantLazy && lf.Value != nil {
					t.Errorf("%s: lazy field has a Value", name)
				}
				if len(ef.Shape) == 0 {
					if !reflect.DeepEqual(lf.Value, ef.Value) {
						t.Errorf("%s: scalar value %v, want %v", name, lf.Value, ef.Value)
					}
					continue
				}
				for _, w := range windows {
					got, err := lf.ReadSlice(w[0], w[1])
					if err != nil {
						t.Fatalf("%s ReadSlice%v: %v", name, w, err)
					}
					want, _ := ef.ReadSlice(w[0], w[1])
					if !reflect.DeepEqual(got, want) {
						t.Errorf("%s ReadSlice%v = %v, want %v", name, w, got, want)
					}
				}
			}

			// Lazy fields refuse silent conversions; ReadData materializes.
			if tc.opts.Mode != LazyOff {
				v := fieldAt(t, root, "entry/data/v")
				if v.Lazy() {
					if _, err := v.Float64(); err == nil {
						t.Errorf("Float64() on a lazy field should fail")
					}
					if err := v.ReadData(); err != nil {
						t.Fatalf("ReadData: %v", err)
					}
					if v.Lazy() || !reflect.DeepEqual(v.Value, fieldAt(t, eager, "entry/data/v").Value) {
						t.Errorf("ReadData did not reproduce the eager value")
					}
				}
			}
		})
	}
}

func TestLazyReadAfterClose(t *testing.T) {
	path := writeLazyFixture(t)
	root, err := LoadWithOptions(path, LoadOptions{Mode: LazyAll})
	if err != nil {
		t.Fatal(err)
	}
	m := fieldAt(t, root, "entry/data/m")
	if _, err := m.ReadSlice(0, 5); err != nil {
		t.Fatalf("ReadSlice before Close: %v", err)
	}
	if err := root.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := root.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := m.ReadSlice(0, 5); err == nil {
		t.Errorf("ReadSlice after Close should fail")
	}
	if err := root.Save(filepath.Join(t.TempDir(), "x.nxs")); err == nil {
		t.Errorf("saving a tree with lazy fields should fail until ReadData is called")
	}
}

func TestParseLazyMode(t *testing.T) {
	for in, want := range map[string]LazyMode{"": LazyOff, "off": LazyOff, "AUTO": LazyAuto, " all ": LazyAll} {
		got, err := ParseLazyMode(in)
		if err != nil || got != want {
			t.Errorf("ParseLazyMode(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := ParseLazyMode("sometimes"); err == nil {
		t.Errorf("expected an error for an invalid mode")
	}
}
