package executor

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
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
	cacheID := "cache-1"

	originalPayload, fastBody, ok, err := exec.prepareCodexResponsesRequestBodyFast(nil, req, opts, from, to, baseModel, codexStreamKeep, true, true, cacheID)
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
	fallbackBody = finalizeCodexRequestBody(fallbackBody, baseModel, codexStreamKeep, true, true, nil, cacheID)

	assertJSONEqual(t, fallbackBody, fastBody)
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
			name: "thinking suffix",
			exec: NewCodexExecutor(&config.Config{}),
			req:  cliproxyexecutor.Request{Model: "gpt-5.4-mini(high)", Payload: rawJSON},
			opts: opts,
		},
		{
			name: "body reasoning",
			exec: NewCodexExecutor(&config.Config{}),
			req:  cliproxyexecutor.Request{Model: "gpt-5.4-mini", Payload: []byte(`{"model":"gpt-5.4-mini","input":"hello","reasoning":{"effort":"high"}}`)},
			opts: cliproxyexecutor.Options{SourceFormat: from, Stream: true},
		},
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
			_, _, ok, err := tt.exec.prepareCodexResponsesRequestBodyFast(nil, tt.req, tt.opts, from, to, baseModel, codexStreamKeep, true, true, "cache-1")
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
