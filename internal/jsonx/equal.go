package jsonx

import (
	"encoding/json"
	"reflect"
)

// SemanticEqual reports whether two JSON documents are semantically equal
// (key order, whitespace, and number formatting ignored; array order IS
// significant).
//
// This is the migration's core equivalence check: sonic re-serializes JSON in
// its own formatting, so a sonic-produced body is NOT byte-identical to the
// gjson/sjson/encoding-json output it replaces. Every migrated operation whose
// result is compared against the std stack — especially anything forwarded
// upstream — must assert SemanticEqual(std, sonic), not byte equality.
//
// Numbers are compared as float64, so trivial formatting differences (1 vs 1.0
// vs 1e0) and engine-specific float rendering do not cause false negatives.
// Caveat: integer JSON numbers above 2^53 lose precision under float64 — these
// APIs carry such values as strings, not JSON numbers, so this is safe here.
func SemanticEqual(a, b []byte) bool {
	va, err := decodeCanonical(a)
	if err != nil {
		return false
	}
	vb, err := decodeCanonical(b)
	if err != nil {
		return false
	}
	return reflect.DeepEqual(va, vb)
}

func decodeCanonical(data []byte) (any, error) {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	return v, nil
}
