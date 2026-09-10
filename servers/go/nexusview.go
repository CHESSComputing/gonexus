package main

import (
	"github.com/vkuznet/gonexus/gonexus"
)

// EntriesResp is the /entries response body.
type EntriesResp struct {
	Key       string      `json:"key"`
	RootClass string      `json:"root_class"`
	Entries   []NodeBrief `json:"entries"`
}

// NodeBrief describes a direct child without descending into it.
type NodeBrief struct {
	Name  string `json:"name"`
	Type  string `json:"type"` // "group" or "field"
	Class string `json:"class,omitempty"`
	Dtype string `json:"dtype,omitempty"`
	Shape []int  `json:"shape,omitempty"`
	Units string `json:"units,omitempty"`
}

// GroupResp is the /group response body.
type GroupResp struct {
	Key      string                 `json:"key"`
	Path     string                 `json:"path"`
	Class    string                 `json:"class"`
	Attrs    map[string]interface{} `json:"attrs,omitempty"`
	Children []NodeBrief            `json:"children"`
}

// FieldResp is the /field response body. Values is always present but may
// be a truncated slice of the full array; see Total/Truncated.
type FieldResp struct {
	Key       string                 `json:"key"`
	Path      string                 `json:"path"`
	Dtype     string                 `json:"dtype"`
	Shape     []int                  `json:"shape,omitempty"`
	Units     string                 `json:"units,omitempty"`
	Attrs     map[string]interface{} `json:"attrs,omitempty"`
	Offset    int                    `json:"offset"`
	Count     int                    `json:"count"`
	Total     int                    `json:"total"`
	Truncated bool                   `json:"truncated"`
	Values    interface{}            `json:"values"`
}

func attrsMap(a *gonexus.AttrSet) map[string]interface{} {
	if a == nil || a.Len() == 0 {
		return nil
	}
	out := make(map[string]interface{}, a.Len())
	for _, k := range a.Keys() {
		v, ok := a.Get(k)
		if ok {
			out[k] = v.Value
		}
	}
	return out
}

func briefOf(name string, obj gonexus.NXobject) NodeBrief {
	switch o := obj.(type) {
	case *gonexus.NXfield:
		return NodeBrief{
			Name:  name,
			Type:  "field",
			Dtype: o.Dtype,
			Shape: o.Shape,
			Units: o.Units(),
		}
	case *gonexus.NXgroup:
		return NodeBrief{
			Name:  name,
			Type:  "group",
			Class: o.NXClass(),
		}
	default:
		return NodeBrief{Name: name, Type: obj.NXClass()}
	}
}

// buildEntries lists the NXentry groups directly under root (root is
// itself the NXroot group returned by gonexus.Load).
func buildEntries(key string, root *gonexus.NXgroup) *EntriesResp {
	resp := &EntriesResp{Key: key, RootClass: root.NXClass()}
	for _, e := range root.Entries() {
		resp.Entries = append(resp.Entries, briefOf(e.Name, e.Object))
	}
	return resp
}

// resolveGroupOrRoot looks up path under root, defaulting to root itself
// when path is empty, and asserts the result is a group.
func resolveGroupOrRoot(root *gonexus.NXgroup, path string) (*gonexus.NXgroup, error) {
	if path == "" || path == "/" {
		return root, nil
	}
	g, err := root.GetGroup(path)
	if err != nil {
		return nil, notFound("group not found at path %q: %v", path, err)
	}
	return g, nil
}

func buildGroup(key, path string, root *gonexus.NXgroup) (*GroupResp, error) {
	g, err := resolveGroupOrRoot(root, path)
	if err != nil {
		return nil, err
	}
	resp := &GroupResp{
		Key:   key,
		Path:  path,
		Class: g.NXClass(),
		Attrs: attrsMap(g.NXAttrs()),
	}
	for _, e := range g.Entries() {
		resp.Children = append(resp.Children, briefOf(e.Name, e.Object))
	}
	return resp, nil
}

func buildField(key, path string, root *gonexus.NXgroup, offset, limit int) (*FieldResp, error) {
	if path == "" {
		return nil, badRequest("missing required query parameter 'path'")
	}
	f, err := root.GetField(path)
	if err != nil {
		return nil, notFound("field not found at path %q: %v", path, err)
	}

	resp := &FieldResp{
		Key:   key,
		Path:  path,
		Dtype: f.Dtype,
		Shape: f.Shape,
		Units: f.Units(),
		Attrs: attrsMap(f.NXAttrs()),
	}

	if len(f.Shape) == 0 {
		// Scalar field: offset/limit don't apply.
		resp.Values = f.Value
		resp.Count = 1
		resp.Total = 1
		return resp, nil
	}

	values, total, truncated, err := sliceValues(f.Value, offset, limit)
	if err != nil {
		return nil, badRequest("%v", err)
	}
	resp.Values = values
	resp.Offset = offset
	resp.Total = total
	resp.Truncated = truncated
	switch v := values.(type) {
	case []float64:
		resp.Count = len(v)
	case []int64:
		resp.Count = len(v)
	case []string:
		resp.Count = len(v)
	case []bool:
		resp.Count = len(v)
	}
	return resp, nil
}

// sliceValues applies [offset:offset+limit] to a flat slice value of any
// of the types gonexus.NXfield.Value may hold, returning the slice, the
// total element count, and whether the slice is shorter than the total
// (i.e. the response is truncated).
func sliceValues(value interface{}, offset, limit int) (interface{}, int, bool, error) {
	switch v := value.(type) {
	case []float64:
		return sliceGeneric(v, offset, limit)
	case []int64:
		return sliceGeneric(v, offset, limit)
	case []int32:
		return sliceGeneric(toInt64s(v), offset, limit)
	case []float32:
		return sliceGeneric(toFloat64s(v), offset, limit)
	case []string:
		return sliceGeneric(v, offset, limit)
	case []bool:
		return sliceGeneric(v, offset, limit)
	default:
		// Fall back to Float64()/Int64() conversion via the field itself
		// is not available here (we only have the raw value), so just
		// report it as-is, unsliced, rather than fail outright.
		return value, 1, false, nil
	}
}

func toInt64s(v []int32) []int64 {
	out := make([]int64, len(v))
	for i, x := range v {
		out[i] = int64(x)
	}
	return out
}

func toFloat64s(v []float32) []float64 {
	out := make([]float64, len(v))
	for i, x := range v {
		out[i] = float64(x)
	}
	return out
}

func sliceGeneric[T any](v []T, offset, limit int) ([]T, int, bool, error) {
	total := len(v)
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	end := total
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	out := v[offset:end]
	truncated := end < total
	return out, total, truncated, nil
}
