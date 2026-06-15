package jsonx

import "testing"

func TestToggle(t *testing.T) {
	Configure("")
	if UseSonic("anything") {
		t.Fatal("empty spec must default to std (no sonic)")
	}
	Configure("all")
	if !UseSonic("anything") {
		t.Fatal(`"all" must enable sonic for every component`)
	}
	Configure("codex.req.build, auth.read")
	if !UseSonic("codex.req.build") || !UseSonic("auth.read") {
		t.Fatal("listed components must use sonic")
	}
	if UseSonic("other.thing") {
		t.Fatal("unlisted component must stay std")
	}
	Configure("")
}

// GetString must return identical results on both engines for string-valued
// fields — this is the parity guarantee the toggle relies on.
func TestGetStringEngineParity(t *testing.T) {
	data := []byte(`{"service_tier":"priority","metadata":{"user_id":"u_123"},"reasoning":{"effort":"high"},"arr":[{"k":"v0"},{"k":"v1"}]}`)
	paths := []string{"service_tier", "metadata.user_id", "reasoning.effort", "arr.1.k", "missing", "metadata.absent"}
	for _, p := range paths {
		Configure("")
		std := GetString("c", data, p)
		Configure("all")
		son := GetString("c", data, p)
		if std != son {
			t.Errorf("GetString(%q): std=%q sonic=%q (mismatch)", p, std, son)
		}
	}
	Configure("")
}

func TestSemanticEqual(t *testing.T) {
	a := []byte(`{"a":1,"b":[1,2],"c":"x"}`)
	bReordered := []byte(`{"c":"x","b":[1,2],"a":1.0}`) // key order + 1 vs 1.0
	if !SemanticEqual(a, bReordered) {
		t.Fatal("reordered keys / equal numbers should be semantically equal")
	}
	if SemanticEqual(a, []byte(`{"a":1,"b":[2,1],"c":"x"}`)) {
		t.Fatal("array order is significant; must NOT be equal")
	}
}
