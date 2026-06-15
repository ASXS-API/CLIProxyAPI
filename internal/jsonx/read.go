package jsonx

import (
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/tidwall/gjson"
)

// sonicCompatiblePath reports whether a gjson dotted path is a plain
// key/index path that sonic.Get can resolve. gjson-specific syntax
// (modifiers @, wildcards *?, array-count #, escapes \) is NOT supported by
// sonic's path API, so for those we stay on gjson even when sonic is toggled on.
func sonicCompatiblePath(path string) bool {
	return path != "" && !strings.ContainsAny(path, "*?#@\\")
}

// splitPath turns a simple dotted gjson path into sonic's []interface{} path
// (numeric segments become array indices).
func splitPath(path string) []interface{} {
	parts := strings.Split(path, ".")
	out := make([]interface{}, len(parts))
	for i, p := range parts {
		if n, err := strconv.Atoi(p); err == nil {
			out[i] = n
		} else {
			out[i] = p
		}
	}
	return out
}

// GetString returns the STRING value at a simple dotted path (gjson semantics
// for string-valued fields: missing → ""), using the engine selected for
// component. Intended for the read-only "extract*" helpers that pull string
// fields (service_tier, metadata.user_id, reasoning.effort, …). Number/bool
// coercion is intentionally not mirrored here — those sites migrate explicitly.
func GetString(component string, data []byte, path string) string {
	if UseSonic(component) && sonicCompatiblePath(path) {
		node, err := sonic.Get(data, splitPath(path)...)
		if err != nil {
			return ""
		}
		if s, err := node.String(); err == nil {
			return s
		}
		return ""
	}
	return gjson.GetBytes(data, path).String()
}

// Exists reports whether a value exists at a simple dotted path.
func Exists(component string, data []byte, path string) bool {
	if UseSonic(component) && sonicCompatiblePath(path) {
		_, err := sonic.Get(data, splitPath(path)...)
		return err == nil
	}
	return gjson.GetBytes(data, path).Exists()
}
