package config

import (
	"encoding"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// checkUnknownKeys rejects every mapping key in data, at any depth, that the
// rawConfig struct tree has no field for. yaml.v3 drops such a key without a
// word, so a misspelled or misplaced setting reads as "has no effect" instead
// of as an error; the operator who wrote `server.drian_timeout` keeps the
// default and never learns why. The check mirrors the decoder's own rules: a
// struct accepts exactly its tagged field names, a map accepts any key, and a
// type with its own UnmarshalYAML or UnmarshalText owns its shape and is not
// descended into. data must already have decoded into rawConfig, so a parse
// failure here is impossible and is reported as "nothing unknown".
func checkUnknownKeys(data []byte) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil
	}
	root := reflect.TypeOf(rawConfig{})
	var found []unknownKey
	walkUnknownKeys(&doc, root, "", &found)
	if len(found) == 0 {
		return nil
	}
	index := acceptedKeyPaths(root)
	parts := make([]string, 0, len(found))
	for _, k := range found {
		parts = append(parts, k.describe(index))
	}
	noun := "key"
	if len(parts) > 1 {
		noun = "keys"
	}
	return fmt.Errorf("config: unknown %s: %s", noun, strings.Join(parts, "; "))
}

// unknownKey is one rejected mapping key: where it sits, which line wrote it,
// and which keys the enclosing level does accept.
type unknownKey struct {
	parent   string
	name     string
	line     int
	accepted []string
}

func (k unknownKey) path() string { return joinKeyPath(k.parent, k.name) }

// describe renders the key with the most useful hint available. A close
// sibling means a typo; the same name accepted elsewhere means a misplaced
// section; failing both, the accepted keys at this level are listed so the
// operator can see the spelling the loader expects.
func (k unknownKey) describe(index map[string][]string) string {
	s := fmt.Sprintf("%q (line %d)", k.path(), k.line)
	if sibling := closestKey(k.name, k.accepted); sibling != "" {
		return s + fmt.Sprintf(" (did you mean %q?)", joinKeyPath(k.parent, sibling))
	}
	if elsewhere := index[k.name]; len(elsewhere) > 0 && len(elsewhere) <= 3 {
		quoted := make([]string, len(elsewhere))
		for i, p := range elsewhere {
			quoted[i] = fmt.Sprintf("%q", p)
		}
		return s + fmt.Sprintf(" (did you mean %s?)", strings.Join(quoted, " or "))
	}
	level := "at the top level"
	if k.parent != "" {
		level = fmt.Sprintf("under %q", k.parent)
	}
	return s + fmt.Sprintf("; valid keys %s are: %s", level, strings.Join(k.accepted, ", "))
}

func joinKeyPath(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "." + name
}

var (
	yamlUnmarshalerType = reflect.TypeFor[yaml.Unmarshaler]()
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
)

// walkUnknownKeys descends node and t together, recording every mapping key
// that the struct at that position does not declare.
func walkUnknownKeys(node *yaml.Node, t reflect.Type, path string, out *[]unknownKey) {
	for node != nil && (node.Kind == yaml.DocumentNode || node.Kind == yaml.AliasNode) {
		if node.Kind == yaml.DocumentNode {
			if len(node.Content) == 0 {
				return
			}
			node = node.Content[0]
		} else {
			node = node.Alias
		}
	}
	if node == nil {
		return
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if ownsItsShape(t) {
		return
	}
	switch t.Kind() {
	case reflect.Struct:
		if node.Kind != yaml.MappingNode {
			return
		}
		fields, open := yamlFields(t)
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, value := node.Content[i], node.Content[i+1]
			if key.Tag == "!!merge" {
				continue
			}
			ft, ok := fields[key.Value]
			if !ok {
				if !open {
					*out = append(*out, unknownKey{parent: path, name: key.Value, line: key.Line, accepted: sortedKeys(fields)})
				}
				continue
			}
			walkUnknownKeys(value, ft, joinKeyPath(path, key.Value), out)
		}
	case reflect.Map:
		if node.Kind != yaml.MappingNode {
			return
		}
		for i := 0; i+1 < len(node.Content); i += 2 {
			walkUnknownKeys(node.Content[i+1], t.Elem(), joinKeyPath(path, node.Content[i].Value), out)
		}
	case reflect.Slice, reflect.Array:
		if node.Kind != yaml.SequenceNode {
			return
		}
		for i, item := range node.Content {
			walkUnknownKeys(item, t.Elem(), fmt.Sprintf("%s[%d]", path, i), out)
		}
	}
}

// ownsItsShape reports whether t decodes itself, so its sub-keys are its own
// business rather than the struct tree's.
func ownsItsShape(t reflect.Type) bool {
	pt := reflect.PointerTo(t)
	return pt.Implements(yamlUnmarshalerType) || pt.Implements(textUnmarshalerType)
}

// yamlFields maps the YAML key of every decodable field of struct t to the
// field's type, following yaml.v3's rules: unexported fields and `yaml:"-"`
// are skipped, an untagged field is keyed by its lowercased name, an inlined
// struct contributes its own fields, and an inlined map makes the struct
// accept any key (open).
func yamlFields(t reflect.Type) (fields map[string]reflect.Type, open bool) {
	fields = map[string]reflect.Type{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" && !f.Anonymous {
			continue
		}
		tag := f.Tag.Get("yaml")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if strings.Contains(","+opts+",", ",inline,") {
			ft := f.Type
			for ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			switch ft.Kind() {
			case reflect.Map:
				open = true
			case reflect.Struct:
				inner, innerOpen := yamlFields(ft)
				for k, v := range inner {
					fields[k] = v
				}
				open = open || innerOpen
			}
			continue
		}
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		fields[name] = f.Type
	}
	return fields, open
}

func sortedKeys(m map[string]reflect.Type) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// acceptedKeyPaths indexes every key name the struct tree accepts by the
// paths at which it is accepted, so a key written under the wrong section
// can be pointed at the right one. List elements appear as `tiers[]`.
func acceptedKeyPaths(root reflect.Type) map[string][]string {
	index := map[string][]string{}
	var visit func(t reflect.Type, path string, depth int)
	visit = func(t reflect.Type, path string, depth int) {
		for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
			if t.Kind() != reflect.Pointer {
				path += "[]"
			}
			t = t.Elem()
		}
		if depth > 8 || t.Kind() != reflect.Struct || ownsItsShape(t) {
			return
		}
		fields, _ := yamlFields(t)
		for _, name := range sortedKeys(fields) {
			full := joinKeyPath(path, name)
			index[name] = append(index[name], full)
			visit(fields[name], full, depth+1)
		}
	}
	visit(root, "", 0)
	return index
}

// closestKey returns the accepted key within a small edit distance of name,
// or "" when none is close enough to be a plausible typo.
func closestKey(name string, accepted []string) string {
	limit := 2
	if len(name) < 5 {
		limit = 1
	}
	best, bestDist := "", limit+1
	for _, candidate := range accepted {
		if d := editDistance(name, candidate); d < bestDist {
			best, bestDist = candidate, d
		}
	}
	return best
}
