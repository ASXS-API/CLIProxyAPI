package responses

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"testing"
)

// valueEquivalent reports whether two json.RawMessage values are JSON-equivalent
// (ignoring insignificant whitespace / key order).
func valueEquivalent(t *testing.T, a, b json.RawMessage) bool {
	t.Helper()
	var va, vb interface{}
	if err := json.Unmarshal(a, &va); err != nil {
		t.Fatalf("unmarshal a=%q: %v", string(a), err)
	}
	if err := json.Unmarshal(b, &vb); err != nil {
		t.Fatalf("unmarshal b=%q: %v", string(b), err)
	}
	return reflect.DeepEqual(va, vb)
}

func assertDecodersAgree(t *testing.T, body []byte) {
	t.Helper()
	std, okStd := decodeTopLevelStdlib(body)
	gj, okGj := decodeTopLevelGjson(body)
	go2, okGo := decodeTopLevelGoJSON(body)

	if okStd != okGj || okStd != okGo {
		t.Fatalf("ok mismatch on valid body: stdlib=%v gjson=%v gojson=%v body=%q", okStd, okGj, okGo, string(body))
	}
	if !okStd {
		return // all three rejected (e.g. top-level null / array)
	}
	// Same key sets.
	for _, pair := range []struct {
		name string
		m    CodexRequestObject
	}{{"gjson", gj}, {"gojson", go2}} {
		if len(pair.m) != len(std) {
			t.Fatalf("%s key count %d != stdlib %d (body=%q)", pair.name, len(pair.m), len(std), string(body))
		}
		for k, sv := range std {
			pv, ok := pair.m[k]
			if !ok {
				t.Fatalf("%s missing key %q (body=%q)", pair.name, k, string(body))
			}
			if !valueEquivalent(t, sv, pv) {
				t.Fatalf("%s value mismatch key=%q stdlib=%q got=%q (body=%q)", pair.name, k, string(sv), string(pv), string(body))
			}
		}
	}
}

func TestCodexDecodersEquivalence(t *testing.T) {
	curated := []string{
		`{}`,
		`   {}   `,
		`{"model":"gpt-5-codex"}`,
		`{"reasoning":{"effort":"high"},"input":[{"type":"reasoning","encrypted_content":"abc"},{"type":"message","role":"user","content":"hi"}]}`,
		`{"a":1,"b":2.5,"c":-3e10,"d":true,"e":false,"f":null,"g":"str"}`,
		`{"weird.key":1,"a b":2,"":3}`,                                    // dotted / spaced / empty keys
		`{"s":"has } and ] and \" and , inside","arr":[1,"]",{"x":"}"}]}`, // delimiters inside strings
		`{"u":"日本語 é ￿ \n\t","model":"x"}`,                       // unicode + escapes
		`{"dup":1,"dup":2}`,                                              // duplicate key -> last wins both
		`{"nested":{"input":[{"a":[1,[2,[3]]]}]},"input":[1,2,3]}`,       // decoy nested input
		`{"big":` + "`" + `STR` + "`" + `}`,                              // placeholder replaced below
		`  {  "x" : [ 1 , 2 ] , "y" : { "z" : 3 } }  `,                   // lots of whitespace
		`{"num":12345678901234567890,"flt":1.7976931348623157e308}`,     // big numbers
		`{"empty_arr":[],"empty_obj":{},"empty_str":""}`,
	}
	// build a big-string body
	big := `{"instructions":"` + string(bytes.Repeat([]byte("x"), 5000)) + `","input":[`
	for i := 0; i < 40; i++ {
		if i > 0 {
			big += ","
		}
		big += `{"type":"reasoning","encrypted_content":"` + string(bytes.Repeat([]byte("Z"), 2000)) + `"}`
	}
	big += `]}`
	curated[10] = big

	for _, s := range curated {
		b := []byte(s)
		if !json.Valid(b) {
			t.Fatalf("curated body not valid JSON: %q", s)
		}
		assertDecodersAgree(t, b)
	}

	// Randomized valid bodies.
	rng := rand.New(rand.NewSource(0xC0DEC0DE))
	keys := []string{"model", "stream", "store", "instructions", "tools", "tool_choice", "input", "reasoning", "service_tier", "metadata", "temperature", "max_output_tokens", "user", "weird.key", "ué", ""}
	strs := []string{"", "x", "gpt-5-codex", "日本語", "has \" quote", "a } b ] c , d", "line\nbreak", "high", "priority"}
	for i := 0; i < 6000; i++ {
		m := map[string]interface{}{}
		n := rng.Intn(8)
		for j := 0; j < n; j++ {
			k := keys[rng.Intn(len(keys))]
			switch rng.Intn(6) {
			case 0:
				m[k] = strs[rng.Intn(len(strs))]
			case 1:
				m[k] = rng.Intn(1000) - 500
			case 2:
				m[k] = rng.Float64()*1e6 - 5e5
			case 3:
				m[k] = rng.Intn(2) == 0
			case 4:
				m[k] = nil
			case 5:
				arr := make([]interface{}, rng.Intn(5))
				for x := range arr {
					arr[x] = map[string]interface{}{"type": "reasoning", "encrypted_content": strs[rng.Intn(len(strs))], "n": x}
				}
				m[k] = arr
			}
		}
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("marshal gen: %v", err)
		}
		assertDecodersAgree(t, b)
	}
}

// TestCodexDecoderGjsonAliasingInvariant asserts the source body is not mutated by
// the full downstream pipeline (rewrite fields + marshal) when using the zero-copy
// gjson decoder — guarding the aliasing invariant.
func TestCodexDecoderGjsonAliasingInvariant(t *testing.T) {
	body := []byte(`{"model":"gpt-5","stream":false,"instructions":"sys","tools":[{"type":"function","name":"f"}],"input":[{"type":"reasoning","encrypted_content":"abc"},{"type":"message","role":"system","content":"s"}],"reasoning":{"effort":"high"},"service_tier":"priority","temperature":0.5}`)
	orig := append([]byte(nil), body...)

	obj, ok := decodeTopLevelGjson(body)
	if !ok {
		t.Fatal("decodeTopLevelGjson returned !ok for valid body")
	}
	RewriteOpenAIResponsesRequestObjectFieldsForCodex(obj)
	obj["model"] = json.RawMessage(`"gpt-5-codex"`)
	obj["prompt_cache_key"] = json.RawMessage(`"k"`)
	out, err := MarshalCodexRequestObjectFast(obj)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !json.Valid(out) {
		t.Fatalf("marshal produced invalid JSON: %q", string(out))
	}
	if !bytes.Equal(body, orig) {
		t.Fatalf("source body was MUTATED by downstream pipeline:\n got=%q\nwant=%q", string(body), string(orig))
	}
}

func benchBody(items int) []byte {
	var b bytes.Buffer
	b.WriteString(`{"model":"gpt-5-codex","stream":true,"store":false,"parallel_tool_calls":true,"instructions":"`)
	b.Write(bytes.Repeat([]byte("x"), 2000))
	b.WriteString(`","tools":[{"type":"function","name":"shell"},{"type":"web_search"}],"reasoning":{"effort":"high"},"service_tier":"priority","input":[`)
	for i := 0; i < items; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		if i%2 == 0 {
			b.WriteString(`{"type":"reasoning","encrypted_content":"`)
			b.Write(bytes.Repeat([]byte("Z"), 10000))
			b.WriteString(`","summary":[]}`)
		} else {
			b.WriteString(`{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}`)
		}
	}
	b.WriteString(`]}`)
	return b.Bytes()
}

func BenchmarkCodexDecoder(b *testing.B) {
	for _, sz := range []struct {
		name  string
		items int
	}{{"large", 80}, {"small", 6}} {
		body := benchBody(sz.items)
		b.Run(sz.name+fmt.Sprintf("_%dB", len(body)), func(b *testing.B) {
			b.Run("A_stdlib", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					decodeTopLevelStdlib(body)
				}
			})
			b.Run("B_gojson", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					decodeTopLevelGoJSON(body)
				}
			})
			b.Run("C_gjson", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					decodeTopLevelGjson(body)
				}
			})
		})
	}
}
