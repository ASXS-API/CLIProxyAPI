package executor

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/jsonx"
)

// TestSanitizeAndRewriteCodexInput_EngineParity asserts the sonic-ast implementation
// is SEMANTICALLY equivalent to the std (gjson/sjson) one across the codex input
// shapes: same changed flag, same JSON result (key order / formatting ignored —
// sonic re-serializes).
func TestSanitizeAndRewriteCodexInput_EngineParity(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"system role rewritten", `[{"type":"message","role":"system","content":[{"type":"input_text","text":"x"}]},{"type":"message","role":"user","content":"hi"}]`},
		{"invalid encrypted_content dropped", `[{"type":"reasoning","summary":[],"encrypted_content":"INVALID_AAAA=="},{"type":"message","role":"user","content":"hi"}]`},
		{"null encrypted_content dropped", `[{"type":"reasoning","encrypted_content":null}]`},
		{"non-string encrypted_content dropped", `[{"type":"reasoning","encrypted_content":123}]`},
		{"empty encrypted_content dropped", `[{"type":"reasoning","encrypted_content":""}]`},
		{"whitespace encrypted_content dropped", `[{"type":"reasoning","encrypted_content":" abc "}]`},
		{"reasoning with id logged path", `[{"type":"reasoning","id":"rs_1","encrypted_content":"BAD"}]`},
		{"no change passthrough", `[{"type":"message","role":"user","content":"hi"},{"type":"message","role":"assistant","content":"yo"}]`},
		{"system + invalid reasoning combined", `[{"type":"message","role":"system","content":"sys"},{"type":"reasoning","encrypted_content":"BAD"},{"type":"message","role":"user","content":"q"}]`},
		{"non-reasoning encrypted_content kept", `[{"type":"message","role":"user","encrypted_content":"X"}]`},
		{"not an array", `{"not":"array"}`},
		{"empty array", `[]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			jsonx.Configure("")
			stdOut, stdChanged := sanitizeAndRewriteCodexInput(context.Background(), "test", []byte(tc.input))
			jsonx.Configure("all")
			sonicOut, sonicChanged := sanitizeAndRewriteCodexInput(context.Background(), "test", []byte(tc.input))
			jsonx.Configure("")
			if stdChanged != sonicChanged {
				t.Fatalf("changed mismatch: std=%v sonic=%v\ninput=%s", stdChanged, sonicChanged, tc.input)
			}
			if !jsonx.SemanticEqual(stdOut, sonicOut) {
				t.Fatalf("semantic mismatch:\n std=%s\nsonic=%s", stdOut, sonicOut)
			}
		})
	}
}
