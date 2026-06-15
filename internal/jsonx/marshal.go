package jsonx

import (
	"encoding/json"

	"github.com/bytedance/sonic"
)

// Marshal serializes v using the engine selected for component.
// NOTE: sonic and encoding/json are NOT guaranteed byte-identical (escaping,
// number formatting). Callers whose output is forwarded upstream MUST be guarded
// by a semantic-equivalence differential test (see SemanticEqual).
func Marshal(component string, v any) ([]byte, error) {
	if UseSonic(component) {
		return sonic.Marshal(v)
	}
	return json.Marshal(v)
}

// Unmarshal parses data into v using the engine selected for component.
func Unmarshal(component string, data []byte, v any) error {
	if UseSonic(component) {
		return sonic.Unmarshal(data, v)
	}
	return json.Unmarshal(data, v)
}

// Valid reports whether data is valid JSON, using the engine selected for component.
func Valid(component string, data []byte) bool {
	if UseSonic(component) {
		return sonic.Valid(data)
	}
	return json.Valid(data)
}
