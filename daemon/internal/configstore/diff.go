package configstore

import (
	"fmt"
	"reflect"
	"sort"
)

// FieldChange is one changed field between two payloads. Path uses state.yaml
// names ("options.compression", "members[2]"). Old or New is nil when the
// field was added or removed.
type FieldChange struct {
	Path string `json:"path"`
	Old  any    `json:"old"`
	New  any    `json:"new"`
}

// DiffPayloads lists the field changes from a to b (either may be nil: the
// resource did not exist or was deleted).
func DiffPayloads(a, b map[string]any) []FieldChange {
	var out []FieldChange
	diffValue("", toAny(normalize(a)), toAny(normalize(b)), &out)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func toAny(m map[string]any) any {
	if m == nil {
		return nil
	}
	return m
}

func join(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

func diffValue(path string, a, b any, out *[]FieldChange) {
	am, aIsMap := a.(map[string]any)
	bm, bIsMap := b.(map[string]any)
	if aIsMap && bIsMap {
		keys := map[string]bool{}
		for k := range am {
			keys[k] = true
		}
		for k := range bm {
			keys[k] = true
		}
		for k := range keys {
			diffValue(join(path, k), am[k], bm[k], out)
		}
		return
	}
	al, aIsList := a.([]any)
	bl, bIsList := b.([]any)
	if aIsList && bIsList && len(al) == len(bl) {
		for i := range al {
			diffValue(fmt.Sprintf("%s[%d]", path, i), al[i], bl[i], out)
		}
		return
	}
	if isEmpty(a) && isEmpty(b) {
		return
	}
	if !reflect.DeepEqual(a, b) {
		*out = append(*out, FieldChange{Path: path, Old: a, New: b})
	}
}

// isEmpty treats absent, null, "", false, 0 and empty lists alike, the way
// state.yaml omits default values.
func isEmpty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case bool:
		return !x
	case float64:
		return x == 0
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	}
	return false
}
