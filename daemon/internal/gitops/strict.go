package gitops

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// unknownKeys returns the settings of a parsed state.yaml that are not part
// of its format, as paths ("datasets[2].qouta", "system.ssh.prot").
//
// The format is what DesiredState declares (the yaml tags): the parser reads
// those keys and no others, which a test holds it to.
func unknownKeys(raw map[string]yamlNode) []string {
	var out []string
	walkKnown(reflect.TypeOf(DesiredState{}), raw, "", &out)
	sort.Strings(out)
	return out
}

func walkKnown(t reflect.Type, node yamlNode, path string, out *[]string) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		m, ok := node.(map[string]yamlNode)
		if !ok {
			return // wrong shape: reported by the parser or the validation
		}
		fields := map[string]reflect.Type{}
		for i := 0; i < t.NumField(); i++ {
			name, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ",")
			if name != "" && name != "-" {
				fields[name] = t.Field(i).Type
			}
		}
		for key, value := range m {
			ft, known := fields[key]
			if !known {
				*out = append(*out, joinPath(path, key))
				continue
			}
			walkKnown(ft, value, joinPath(path, key), out)
		}
	case reflect.Slice:
		seq, ok := node.([]yamlNode)
		if !ok {
			return
		}
		for i, el := range seq {
			walkKnown(t.Elem(), el, fmt.Sprintf("%s[%d]", path, i), out)
		}
	case reflect.Map:
		// Free keys (interface names, pool property names); the values have a format.
		m, ok := node.(map[string]yamlNode)
		if !ok {
			return
		}
		for key, value := range m {
			walkKnown(t.Elem(), value, joinPath(path, key), out)
		}
	}
}

func joinPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}
