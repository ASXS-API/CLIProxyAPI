package executor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	codexresponses "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/codex/openai/responses"
)

// foldValidEnc returns a structurally-valid GPT reasoning signature (73-byte decoded
// payload, version 0x80 -> base64url begins with gAAAA).
func foldValidEnc() string {
	decoded := make([]byte, 73)
	decoded[0] = 0x80
	return base64.RawURLEncoding.EncodeToString(decoded)
}

func foldInputJSONEqual(t *testing.T, a, b []byte) {
	t.Helper()
	var va, vb interface{}
	if err := json.Unmarshal(a, &va); err != nil {
		t.Fatalf("unmarshal a: %v\n%s", err, a)
	}
	if err := json.Unmarshal(b, &vb); err != nil {
		t.Fatalf("unmarshal b: %v\n%s", err, b)
	}
	if !reflect.DeepEqual(va, vb) {
		t.Fatalf("not JSON-equivalent:\n old=%s\n new=%s", a, b)
	}
}

// TestSanitizeAndRewriteCodexInputEquivalence proves the combined single-walk
// (RewriteObjectForCodexFast + sanitizeAndRewriteCodexInput) yields a JSON-equivalent
// final body to the old two-walk (RewriteObjectForCodex full + sanitizeReasoningEncryptedContentInput).
func TestSanitizeAndRewriteCodexInputEquivalence(t *testing.T) {
	ctx := context.Background()
	enc := foldValidEnc()
	if !signature.IsValidGPTReasoningSignature(enc) {
		t.Fatalf("test fixture foldValidEnc is not a valid signature: %q", enc)
	}
	corpus := []string{
		`{"model":"m"}`,                 // no input
		`{"input":[]}`,                  // empty array
		`{"input":"plain string"}`,      // string input (wrapped)
		`{"input":[{"type":"message","role":"user","content":"hi"}]}`,                                  // no system, no reasoning
		`{"input":[{"type":"message","role":"system","content":"sys"},{"type":"message","role":"user","content":"hi"}]}`, // system role
		fmt.Sprintf(`{"input":[{"type":"reasoning","encrypted_content":%q}]}`, enc),                    // valid reasoning (kept)
		`{"input":[{"type":"reasoning","encrypted_content":"badsig"}]}`,                                // invalid reasoning (dropped)
		`{"input":[{"type":"reasoning","encrypted_content":null}]}`,                                    // null (dropped)
		`{"input":[{"type":"reasoning","encrypted_content":123}]}`,                                     // non-string (dropped)
		`{"input":[{"type":"reasoning"}]}`,                                                             // reasoning without encrypted_content
		// mixed: system role + valid reasoning + invalid reasoning + plain message
		fmt.Sprintf(`{"model":"gpt-5-codex","input":[{"type":"message","role":"system","content":"s"},{"type":"reasoning","id":"r1","encrypted_content":%q},{"type":"reasoning","id":"r2","encrypted_content":"  spaced  "},{"type":"message","role":"user","content":"q"}],"reasoning":{"effort":"high"}}`, enc),
		// multiple system roles + multiple invalid reasonings
		`{"input":[{"type":"message","role":"system","content":"a"},{"type":"reasoning","encrypted_content":""},{"type":"message","role":"system","content":"b"},{"type":"reasoning","encrypted_content":"nope"}]}`,
	}

	for _, p := range corpus {
		payload := []byte(p)
		if !json.Valid(payload) {
			t.Fatalf("corpus body not valid JSON: %s", p)
		}

		objOld, ok1 := codexresponses.RewriteOpenAIResponsesRequestObjectForCodex(append([]byte(nil), payload...))
		objNew, ok2 := codexresponses.RewriteOpenAIResponsesRequestObjectForCodexFast(append([]byte(nil), payload...))
		if ok1 != ok2 {
			t.Fatalf("ok mismatch for %s: old=%v new=%v", p, ok1, ok2)
		}
		if !ok1 {
			continue
		}

		if rawInput, ex := objOld["input"]; ex {
			if s, ch := sanitizeReasoningEncryptedContentInput(ctx, "p", rawInput); ch {
				objOld["input"] = s
			}
		}
		if rawInput, ex := objNew["input"]; ex {
			if s, ch := sanitizeAndRewriteCodexInput(ctx, "p", rawInput); ch {
				objNew["input"] = s
			}
		}

		bodyOld, err := codexresponses.MarshalCodexRequestObjectFast(objOld)
		if err != nil {
			t.Fatalf("marshal old: %v", err)
		}
		bodyNew, err := codexresponses.MarshalCodexRequestObjectFast(objNew)
		if err != nil {
			t.Fatalf("marshal new: %v", err)
		}
		foldInputJSONEqual(t, bodyOld, bodyNew)
	}
}

func BenchmarkCodexInputWalk(b *testing.B) {
	ctx := context.Background()
	enc := foldValidEnc()

	mkBody := func(withSystem bool) []byte {
		body := []byte(`{"model":"gpt-5-codex","input":[`)
		if withSystem {
			body = append(body, []byte(`{"type":"message","role":"system","content":"sys"}`)...)
		} else {
			body = append(body, []byte(`{"type":"message","role":"user","content":"first"}`)...)
		}
		for i := 0; i < 60; i++ {
			body = append(body, []byte(fmt.Sprintf(`,{"type":"reasoning","id":"r%d","encrypted_content":%q}`, i, enc))...)
		}
		body = append(body, []byte(`,{"type":"message","role":"user","content":"q"}]}`)...)
		return body
	}

	for _, tc := range []struct {
		name       string
		withSystem bool
	}{{"no_system_common", false}, {"with_system", true}} {
		body := mkBody(tc.withSystem)
		// OLD fast path: full RewriteObjectForCodex (decode + system-role rewrite) + sanitize.
		b.Run(tc.name+"/old", func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				obj, _ := codexresponses.RewriteOpenAIResponsesRequestObjectForCodex(append([]byte(nil), body...))
				if in, ok := obj["input"]; ok {
					if s, ch := sanitizeReasoningEncryptedContentInput(ctx, "p", in); ch {
						obj["input"] = s
					}
				}
			}
		})
		// NEW fast path: RewriteObjectForCodexFast (decode, deferred rewrite) + combined walk.
		b.Run(tc.name+"/new", func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				obj, _ := codexresponses.RewriteOpenAIResponsesRequestObjectForCodexFast(append([]byte(nil), body...))
				if in, ok := obj["input"]; ok {
					if s, ch := sanitizeAndRewriteCodexInput(ctx, "p", in); ch {
						obj["input"] = s
					}
				}
			}
		})
	}
}
