package executor

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestPrepareCodexResponsesRequestBodyFastMatchesFallback(t *testing.T) {
	rawJSON := []byte(`{
		"model":"gpt-5.4-mini",
		"instructions":null,
		"input":[
			{"type":"message","role":"system","content":[{"type":"input_text","text":"system"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hello <world>"}]}
		],
		"tools":[{"type":"web_search_preview"}],
		"tool_choice":{"type":"web_search_preview_2025_03_11"},
		"service_tier":"priority",
		"max_output_tokens":1024,
		"context_management":[{"type":"compaction"}],
		"previous_response_id":"resp_old",
		"stream_options":{"include_usage":true},
		"user":"abc"
	}`)
	exec := NewCodexExecutor(&config.Config{})
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.4-mini",
		Payload: rawJSON,
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("openai-response"),
		OriginalRequest: rawJSON,
		Stream:          true,
	}
	from := opts.SourceFormat
	to := sdktranslator.FromString("codex")
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	originalPayload, fastBody, ok, err := exec.prepareCodexResponsesRequestBodyFast(context.Background(), nil,req, opts, from, to, baseModel, codexStreamKeep, true, true)
	if err != nil {
		t.Fatalf("prepareCodexResponsesRequestBodyFast error: %v", err)
	}
	if !ok {
		t.Fatal("prepareCodexResponsesRequestBodyFast did not use fast path")
	}
	if !sameBytesBacking(originalPayload, rawJSON) {
		t.Fatal("fast path did not preserve original payload backing")
	}

	originalTranslated, fallbackBody := translateCodexRequestPair(opts, from, to, baseModel, originalPayload, req.Payload, true)
	fallbackBody, err = thinking.ApplyThinking(fallbackBody, req.Model, from.String(), to.String(), exec.Identifier())
	if err != nil {
		t.Fatalf("ApplyThinking error: %v", err)
	}
	fallbackBody = helps.ApplyPayloadConfigWithRoot(exec.cfg, baseModel, to.String(), "", fallbackBody, originalTranslated, req.Model, "")
	fallbackBody = finalizeCodexRequestBody(fallbackBody, baseModel, codexStreamKeep, true, true, nil, "")

	assertJSONEqual(t, fallbackBody, fastBody)
}

func TestPrepareCodexResponsesRequestBodyFastReasoningMatchesFallback(t *testing.T) {
	// A request carrying a "reasoning" object must now take the single-parse fast path
	// (it previously fell back to the byte path). The fast-path output must stay
	// equivalent to the full translate + ApplyThinking + ApplyPayloadConfig + finalize
	// slow path.
	rawJSON := []byte(`{
		"model":"gpt-5.4-mini",
		"instructions":null,
		"input":[
			{"type":"message","role":"system","content":[{"type":"input_text","text":"system"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hello <world>"}]}
		],
		"reasoning":{"effort":"high"},
		"tools":[{"type":"web_search_preview"}],
		"tool_choice":{"type":"web_search_preview_2025_03_11"},
		"service_tier":"priority",
		"max_output_tokens":1024,
		"context_management":[{"type":"compaction"}],
		"previous_response_id":"resp_old",
		"stream_options":{"include_usage":true},
		"user":"abc"
	}`)
	exec := NewCodexExecutor(&config.Config{})
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.4-mini",
		Payload: rawJSON,
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("openai-response"),
		OriginalRequest: rawJSON,
		Stream:          true,
	}
	from := opts.SourceFormat
	to := sdktranslator.FromString("codex")
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	originalPayload, fastBody, ok, err := exec.prepareCodexResponsesRequestBodyFast(context.Background(), nil,req, opts, from, to, baseModel, codexStreamKeep, true, true)
	if err != nil {
		t.Fatalf("prepareCodexResponsesRequestBodyFast error: %v", err)
	}
	if !ok {
		t.Fatal("prepareCodexResponsesRequestBodyFast did not use fast path for reasoning request")
	}
	if !sameBytesBacking(originalPayload, rawJSON) {
		t.Fatal("fast path did not preserve original payload backing")
	}

	originalTranslated, fallbackBody := translateCodexRequestPair(opts, from, to, baseModel, originalPayload, req.Payload, true)
	fallbackBody, err = thinking.ApplyThinking(fallbackBody, req.Model, from.String(), to.String(), exec.Identifier())
	if err != nil {
		t.Fatalf("ApplyThinking error: %v", err)
	}
	fallbackBody = helps.ApplyPayloadConfigWithRoot(exec.cfg, baseModel, to.String(), "", fallbackBody, originalTranslated, req.Model, "")
	fallbackBody = finalizeCodexRequestBody(fallbackBody, baseModel, codexStreamKeep, true, true, nil, "")

	assertJSONEqual(t, fallbackBody, fastBody)
}

func TestPrepareCodexResponsesRequestBodyFastThinkingSuffixMatchesFallback(t *testing.T) {
	// A model thinking suffix (e.g. "gpt-5.4-mini(high)") must now take the single-parse
	// fast path (it previously fell back to the byte path). The suffix-derived reasoning
	// effort must be applied on the fast path identically to the full translate +
	// ApplyThinking + ApplyPayloadConfig + finalize slow path — including when the body
	// carries NO "reasoning" object (the case the old reasoning-only trigger missed).
	cases := []struct {
		name    string
		model   string
		rawJSON string
	}{
		{
			name:  "suffix without body reasoning",
			model: "gpt-5.4-mini(high)",
			rawJSON: `{
				"model":"gpt-5.4-mini",
				"instructions":null,
				"input":[
					{"type":"message","role":"system","content":[{"type":"input_text","text":"system"}]},
					{"type":"message","role":"user","content":[{"type":"input_text","text":"hello <world>"}]}
				],
				"tools":[{"type":"web_search_preview"}],
				"max_output_tokens":1024,
				"previous_response_id":"resp_old",
				"stream_options":{"include_usage":true},
				"user":"abc"
			}`,
		},
		{
			name:  "suffix overrides body reasoning",
			model: "gpt-5.4-mini(low)",
			rawJSON: `{
				"model":"gpt-5.4-mini",
				"instructions":null,
				"input":[
					{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}
				],
				"reasoning":{"effort":"high"},
				"tools":[{"type":"web_search_preview"}],
				"user":"abc"
			}`,
		},
		{
			// The suffix overrides reasoning.effort while non-effort reasoning fields
			// (e.g. summary) must be preserved through the object-level thinking apply.
			name:  "suffix overrides effort but preserves other reasoning fields",
			model: "gpt-5.4-mini(low)",
			rawJSON: `{
				"model":"gpt-5.4-mini",
				"instructions":null,
				"input":[
					{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}
				],
				"reasoning":{"effort":"high","summary":"auto"},
				"tools":[{"type":"web_search_preview"}],
				"user":"abc"
			}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rawJSON := []byte(tc.rawJSON)
			exec := NewCodexExecutor(&config.Config{})
			req := cliproxyexecutor.Request{Model: tc.model, Payload: rawJSON}
			opts := cliproxyexecutor.Options{
				SourceFormat:    sdktranslator.FromString("openai-response"),
				OriginalRequest: rawJSON,
				Stream:          true,
			}
			from := opts.SourceFormat
			to := sdktranslator.FromString("codex")
			baseModel := thinking.ParseSuffix(req.Model).ModelName

			originalPayload, fastBody, ok, err := exec.prepareCodexResponsesRequestBodyFast(context.Background(), nil, req, opts, from, to, baseModel, codexStreamKeep, true, true)
			if err != nil {
				t.Fatalf("prepareCodexResponsesRequestBodyFast error: %v", err)
			}
			if !ok {
				t.Fatal("prepareCodexResponsesRequestBodyFast did not use fast path for thinking suffix request")
			}
			if !sameBytesBacking(originalPayload, rawJSON) {
				t.Fatal("fast path did not preserve original payload backing")
			}

			originalTranslated, fallbackBody := translateCodexRequestPair(opts, from, to, baseModel, originalPayload, req.Payload, true)
			fallbackBody, err = thinking.ApplyThinking(fallbackBody, req.Model, from.String(), to.String(), exec.Identifier())
			if err != nil {
				t.Fatalf("ApplyThinking error: %v", err)
			}
			fallbackBody = helps.ApplyPayloadConfigWithRoot(exec.cfg, baseModel, to.String(), "", fallbackBody, originalTranslated, req.Model, "")
			fallbackBody = finalizeCodexRequestBody(fallbackBody, baseModel, codexStreamKeep, true, true, nil, "")

			assertJSONEqual(t, fallbackBody, fastBody)
		})
	}
}

func TestPrepareCodexResponsesRequestBodyFastFallsBackForByteMutators(t *testing.T) {
	rawJSON := []byte(`{"model":"gpt-5.4-mini","input":"hello"}`)
	req := cliproxyexecutor.Request{Model: "gpt-5.4-mini", Payload: rawJSON}
	opts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("openai-response"),
		OriginalRequest: rawJSON,
		Stream:          true,
	}
	from := opts.SourceFormat
	to := sdktranslator.FromString("codex")

	tests := []struct {
		name string
		exec *CodexExecutor
		req  cliproxyexecutor.Request
		opts cliproxyexecutor.Options
	}{
		{
			name: "payload rules",
			exec: NewCodexExecutor(&config.Config{Payload: config.PayloadConfig{
				Filter: []config.PayloadFilterRule{{
					Models: []config.PayloadModelRule{{Name: "*"}},
					Params: []string{"metadata.trace_id"},
				}},
			}}),
			req:  req,
			opts: opts,
		},
		{
			name: "disable image generation",
			exec: NewCodexExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}}),
			req:  req,
			opts: opts,
		},
		{
			name: "different original payload backing",
			exec: NewCodexExecutor(&config.Config{}),
			req:  req,
			opts: cliproxyexecutor.Options{
				SourceFormat:    from,
				OriginalRequest: append([]byte(nil), rawJSON...),
				Stream:          true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			baseModel := thinking.ParseSuffix(tt.req.Model).ModelName
			_, _, ok, err := tt.exec.prepareCodexResponsesRequestBodyFast(context.Background(), nil,tt.req, tt.opts, from, to, baseModel, codexStreamKeep, true, true)
			if err != nil {
				t.Fatalf("prepareCodexResponsesRequestBodyFast error: %v", err)
			}
			if ok {
				t.Fatal("prepareCodexResponsesRequestBodyFast used fast path, want fallback")
			}
		})
	}
}

func assertJSONEqual(t *testing.T, want, got []byte) {
	t.Helper()
	var wantValue any
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("failed to parse expected JSON: %v\n%s", err, string(want))
	}
	var gotValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("failed to parse actual JSON: %v\n%s", err, string(got))
	}
	if !reflect.DeepEqual(wantValue, gotValue) {
		t.Fatalf("JSON mismatch\nwant: %s\n got: %s", string(want), string(got))
	}
}

// G002 differential (ACCURATE slow-path model): mirrors codex_executor.go:1027-1045
// exactly — translate + ApplyThinking + ApplyPayloadConfig + finalize + the
// whole-body sanitizeOpenAIResponsesReasoningEncryptedContent at line 1045 that the
// real slow path applies (and that my earlier test wrongly omitted). Uses the real
// production shape: thinking-suffix model + reasoning items carrying encrypted_content
// + system role in the input array. If fast==slow here, the post-latest commits
// (05f3cb2c/429749e7/a80c5d04) emit byte-identical upstream requests vs cbc1c3de.
func TestG002PostLatestProducesSameUpstreamBytes(t *testing.T) {
	cases := []struct{ name, model string }{
		{"plain", "gpt-5-codex"},
		{"suffix_high", "gpt-5-codex(high)"},
		{"suffix_low", "gpt-5-codex(low)"},
	}
	raw := `{
		"model":"gpt-5-codex",
		"instructions":"You are Codex.",
		"input":[
			{"type":"message","role":"system","content":[{"type":"input_text","text":"sys"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"q1"}]},
			{"type":"reasoning","summary":[{"type":"summary_text","text":"t"}],"encrypted_content":"INVALID_SIG_AAAA=="},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"a1"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"q2"}]}
		],
		"reasoning":{"effort":"medium","summary":"detailed"},
		"tools":[{"type":"web_search_preview"}],
		"service_tier":"priority",
		"max_output_tokens":2048,
		"previous_response_id":"resp_prev",
		"stream":true
	}`
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rawJSON := []byte(raw)
			exec := NewCodexExecutor(&config.Config{})
			req := cliproxyexecutor.Request{Model: tc.model, Payload: rawJSON}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), OriginalRequest: rawJSON, Stream: true}
			from := opts.SourceFormat
			to := sdktranslator.FromString("codex")
			baseModel := thinking.ParseSuffix(req.Model).ModelName
			// FAST path (a80c5d04)
			originalPayload, fastBody, ok, err := exec.prepareCodexResponsesRequestBodyFast(context.Background(), nil, req, opts, from, to, baseModel, codexStreamTrue, true, true)
			if err != nil { t.Fatalf("fast err: %v", err) }
			if !ok { t.Fatal("did not take fast path") }
			// SLOW path (cbc1c3de) — exact mirror of codex_executor.go:1027-1045
			originalTranslated, slowBody := translateCodexRequestPair(opts, from, to, baseModel, originalPayload, req.Payload, false)
			slowBody, err = thinking.ApplyThinking(slowBody, req.Model, from.String(), to.String(), exec.Identifier())
			if err != nil { t.Fatalf("ApplyThinking err: %v", err) }
			slowBody = helps.ApplyPayloadConfigWithRequest(exec.cfg, baseModel, to.String(), from.String(), "", slowBody, originalTranslated, req.Model, "", opts.Headers)
			slowBody = finalizeCodexRequestBody(slowBody, baseModel, codexStreamTrue, true, true, nil, "")
			slowBody = sanitizeOpenAIResponsesReasoningEncryptedContent(context.Background(), "codex executor", slowBody)
			assertJSONEqual(t, slowBody, fastBody)
		})
	}
}
