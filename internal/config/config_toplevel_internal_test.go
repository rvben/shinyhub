package config

import (
	"reflect"
	"strings"
	"testing"
)

// TestValidTopLevelKeys_MatchRawConfig guards against drift: validTopLevelKeys
// must list exactly the rawConfig fields' yaml tags. A tag missing from the map
// would make configs using that section fail to load; a stale map entry would
// let a removed section through.
func TestValidTopLevelKeys_MatchRawConfig(t *testing.T) {
	rt := reflect.TypeOf(rawConfig{})
	structKeys := map[string]bool{}
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("yaml")
		if c := strings.IndexByte(tag, ','); c >= 0 {
			tag = tag[:c]
		}
		if tag == "" || tag == "-" {
			continue
		}
		structKeys[tag] = true
	}
	for k := range structKeys {
		if !validTopLevelKeys[k] {
			t.Errorf("rawConfig yaml key %q is missing from validTopLevelKeys (configs using it would be wrongly rejected)", k)
		}
	}
	for k := range validTopLevelKeys {
		if !structKeys[k] {
			t.Errorf("validTopLevelKeys has %q with no matching rawConfig field (stale)", k)
		}
	}
}
