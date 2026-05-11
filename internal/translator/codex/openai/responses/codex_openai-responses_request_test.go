package responses

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/tidwall/gjson"
)

// TestConvertSystemRoleToDeveloper_BasicConversion tests the basic system -> developer role conversion
func TestConvertSystemRoleToDeveloper_BasicConversion(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gpt-5.2",
		"input": [
			{
				"type": "message",
				"role": "system",
				"content": [{"type": "input_text", "text": "You are a pirate."}]
			},
			{
				"type": "message",
				"role": "user",
				"content": [{"type": "input_text", "text": "Say hello."}]
			}
		]
	}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5.2", inputJSON, false)
	outputStr := string(output)

	// Check that system role was converted to developer
	firstItemRole := gjson.Get(outputStr, "input.0.role")
	if firstItemRole.String() != "developer" {
		t.Errorf("Expected role 'developer', got '%s'", firstItemRole.String())
	}

	// Check that user role remains unchanged
	secondItemRole := gjson.Get(outputStr, "input.1.role")
	if secondItemRole.String() != "user" {
		t.Errorf("Expected role 'user', got '%s'", secondItemRole.String())
	}

	// Check content is preserved
	firstItemContent := gjson.Get(outputStr, "input.0.content.0.text")
	if firstItemContent.String() != "You are a pirate." {
		t.Errorf("Expected content 'You are a pirate.', got '%s'", firstItemContent.String())
	}
}

// TestConvertSystemRoleToDeveloper_MultipleSystemMessages tests conversion with multiple system messages
func TestConvertSystemRoleToDeveloper_MultipleSystemMessages(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gpt-5.2",
		"input": [
			{
				"type": "message",
				"role": "system",
				"content": [{"type": "input_text", "text": "You are helpful."}]
			},
			{
				"type": "message",
				"role": "system",
				"content": [{"type": "input_text", "text": "Be concise."}]
			},
			{
				"type": "message",
				"role": "user",
				"content": [{"type": "input_text", "text": "Hello"}]
			}
		]
	}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5.2", inputJSON, false)
	outputStr := string(output)

	// Check that both system roles were converted
	firstRole := gjson.Get(outputStr, "input.0.role")
	if firstRole.String() != "developer" {
		t.Errorf("Expected first role 'developer', got '%s'", firstRole.String())
	}

	secondRole := gjson.Get(outputStr, "input.1.role")
	if secondRole.String() != "developer" {
		t.Errorf("Expected second role 'developer', got '%s'", secondRole.String())
	}

	// Check that user role is unchanged
	thirdRole := gjson.Get(outputStr, "input.2.role")
	if thirdRole.String() != "user" {
		t.Errorf("Expected third role 'user', got '%s'", thirdRole.String())
	}
}

// TestConvertSystemRoleToDeveloper_NoSystemMessages tests that requests without system messages are unchanged
func TestConvertSystemRoleToDeveloper_NoSystemMessages(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gpt-5.2",
		"input": [
			{
				"type": "message",
				"role": "user",
				"content": [{"type": "input_text", "text": "Hello"}]
			},
			{
				"type": "message",
				"role": "assistant",
				"content": [{"type": "output_text", "text": "Hi there!"}]
			}
		]
	}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5.2", inputJSON, false)
	outputStr := string(output)

	// Check that user and assistant roles are unchanged
	firstRole := gjson.Get(outputStr, "input.0.role")
	if firstRole.String() != "user" {
		t.Errorf("Expected role 'user', got '%s'", firstRole.String())
	}

	secondRole := gjson.Get(outputStr, "input.1.role")
	if secondRole.String() != "assistant" {
		t.Errorf("Expected role 'assistant', got '%s'", secondRole.String())
	}
}

// TestConvertSystemRoleToDeveloper_EmptyInput tests that empty input arrays are handled correctly
func TestConvertSystemRoleToDeveloper_EmptyInput(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gpt-5.2",
		"input": []
	}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5.2", inputJSON, false)
	outputStr := string(output)

	// Check that input is still an empty array
	inputArray := gjson.Get(outputStr, "input")
	if !inputArray.IsArray() {
		t.Error("Input should still be an array")
	}
	if len(inputArray.Array()) != 0 {
		t.Errorf("Expected empty array, got %d items", len(inputArray.Array()))
	}
}

// TestConvertSystemRoleToDeveloper_NoInputField tests that requests without input field are unchanged
func TestConvertSystemRoleToDeveloper_NoInputField(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gpt-5.2",
		"stream": false
	}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5.2", inputJSON, false)
	outputStr := string(output)

	// Check that other fields are still set correctly
	stream := gjson.Get(outputStr, "stream")
	if !stream.Bool() {
		t.Error("Stream should be set to true by conversion")
	}

	store := gjson.Get(outputStr, "store")
	if store.Bool() {
		t.Error("Store should be set to false by conversion")
	}
}

// TestConvertOpenAIResponsesRequestToCodex_OriginalIssue tests the exact issue reported by the user
func TestConvertOpenAIResponsesRequestToCodex_OriginalIssue(t *testing.T) {
	// This is the exact input that was failing with "System messages are not allowed"
	inputJSON := []byte(`{
		"model": "gpt-5.2",
		"input": [
			{
				"type": "message",
				"role": "system",
				"content": "You are a pirate. Always respond in pirate speak."
			},
			{
				"type": "message",
				"role": "user",
				"content": "Say hello."
			}
		],
		"stream": false
	}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5.2", inputJSON, false)
	outputStr := string(output)

	// Verify system role was converted to developer
	firstRole := gjson.Get(outputStr, "input.0.role")
	if firstRole.String() != "developer" {
		t.Errorf("Expected role 'developer', got '%s'", firstRole.String())
	}

	// Verify stream was set to true (as required by Codex)
	stream := gjson.Get(outputStr, "stream")
	if !stream.Bool() {
		t.Error("Stream should be set to true")
	}

	// Verify other required fields for Codex
	store := gjson.Get(outputStr, "store")
	if store.Bool() {
		t.Error("Store should be false")
	}

	parallelCalls := gjson.Get(outputStr, "parallel_tool_calls")
	if !parallelCalls.Bool() {
		t.Error("parallel_tool_calls should be true")
	}

	include := gjson.Get(outputStr, "include")
	if !include.IsArray() || len(include.Array()) != 1 {
		t.Error("include should be an array with one element")
	} else if include.Array()[0].String() != "reasoning.encrypted_content" {
		t.Errorf("Expected include[0] to be 'reasoning.encrypted_content', got '%s'", include.Array()[0].String())
	}
}

// TestConvertSystemRoleToDeveloper_AssistantRole tests that assistant role is preserved
func TestConvertSystemRoleToDeveloper_AssistantRole(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gpt-5.2",
		"input": [
			{
				"type": "message",
				"role": "system",
				"content": [{"type": "input_text", "text": "You are helpful."}]
			},
			{
				"type": "message",
				"role": "user",
				"content": [{"type": "input_text", "text": "Hello"}]
			},
			{
				"type": "message",
				"role": "assistant",
				"content": [{"type": "output_text", "text": "Hi!"}]
			}
		]
	}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5.2", inputJSON, false)
	outputStr := string(output)

	// Check system -> developer
	firstRole := gjson.Get(outputStr, "input.0.role")
	if firstRole.String() != "developer" {
		t.Errorf("Expected first role 'developer', got '%s'", firstRole.String())
	}

	// Check user unchanged
	secondRole := gjson.Get(outputStr, "input.1.role")
	if secondRole.String() != "user" {
		t.Errorf("Expected second role 'user', got '%s'", secondRole.String())
	}

	// Check assistant unchanged
	thirdRole := gjson.Get(outputStr, "input.2.role")
	if thirdRole.String() != "assistant" {
		t.Errorf("Expected third role 'assistant', got '%s'", thirdRole.String())
	}
}

func TestConvertOpenAIResponsesRequestToCodex_NormalizesWebSearchPreview(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gpt-5.4-mini",
		"input": "find latest OpenAI model news",
		"tools": [
			{"type": "web_search_preview_2025_03_11"}
		],
		"tool_choice": {
			"type": "allowed_tools",
			"tools": [
				{"type": "web_search_preview"},
				{"type": "web_search_preview_2025_03_11"}
			]
		}
	}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5.4-mini", inputJSON, false)

	if got := gjson.GetBytes(output, "tools.0.type").String(); got != "web_search" {
		t.Fatalf("tools.0.type = %q, want %q: %s", got, "web_search", string(output))
	}
	if got := gjson.GetBytes(output, "tool_choice.type").String(); got != "allowed_tools" {
		t.Fatalf("tool_choice.type = %q, want %q: %s", got, "allowed_tools", string(output))
	}
	if got := gjson.GetBytes(output, "tool_choice.tools.0.type").String(); got != "web_search" {
		t.Fatalf("tool_choice.tools.0.type = %q, want %q: %s", got, "web_search", string(output))
	}
	if got := gjson.GetBytes(output, "tool_choice.tools.1.type").String(); got != "web_search" {
		t.Fatalf("tool_choice.tools.1.type = %q, want %q: %s", got, "web_search", string(output))
	}
}

func TestConvertOpenAIResponsesRequestToCodex_NormalizesTopLevelToolChoicePreviewAlias(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gpt-5.4-mini",
		"input": "find latest OpenAI model news",
		"tool_choice": {"type": "web_search_preview_2025_03_11"}
	}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5.4-mini", inputJSON, false)

	if got := gjson.GetBytes(output, "tool_choice.type").String(); got != "web_search" {
		t.Fatalf("tool_choice.type = %q, want %q: %s", got, "web_search", string(output))
	}
}

func TestUserFieldDeletion(t *testing.T) {
	inputJSON := []byte(`{  
		"model": "gpt-5.2",  
		"user": "test-user",  
		"input": [{"role": "user", "content": "Hello"}]  
	}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5.2", inputJSON, false)
	outputStr := string(output)

	// Verify user field is deleted
	userField := gjson.Get(outputStr, "user")
	if userField.Exists() {
		t.Errorf("user field should be deleted, but it was found with value: %s", userField.Raw)
	}
}

func TestContextManagementCompactionCompatibility(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gpt-5.2",
		"context_management": [
			{
				"type": "compaction",
				"compact_threshold": 12000
			}
		],
		"input": [{"role":"user","content":"hello"}]
	}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5.2", inputJSON, false)
	outputStr := string(output)

	if gjson.Get(outputStr, "context_management").Exists() {
		t.Fatalf("context_management should be removed for Codex compatibility")
	}
	if gjson.Get(outputStr, "truncation").Exists() {
		t.Fatalf("truncation should be removed for Codex compatibility")
	}
}

func TestTruncationRemovedForCodexCompatibility(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gpt-5.2",
		"truncation": "disabled",
		"input": [{"role":"user","content":"hello"}]
	}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5.2", inputJSON, false)
	outputStr := string(output)

	if gjson.Get(outputStr, "truncation").Exists() {
		t.Fatalf("truncation should be removed for Codex compatibility")
	}
}

func TestConvertOpenAIResponsesRequestToCodex_FastPathMatchesLegacySemantics(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "input string",
			body: `{
				"model":"gpt-5.2",
				"input":"hello <world>",
				"stream":false,
				"store":true
			}`,
		},
		{
			name: "system role and removed fields",
			body: `{
				"model":"gpt-5.2",
				"input":[
					{"type":"message","role":"system","content":[{"type":"input_text","text":"system prompt"}]},
					{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}
				],
				"max_output_tokens":1024,
				"max_completion_tokens":2048,
				"temperature":0.7,
				"top_p":0.9,
				"truncation":"disabled",
				"context_management":[{"type":"compaction"}],
				"user":"abc"
			}`,
		},
		{
			name: "priority service tier",
			body: `{
				"model":"gpt-5.2",
				"service_tier":"priority",
				"input":[{"role":"user","content":"hello"}]
			}`,
		},
		{
			name: "non priority service tier",
			body: `{
				"model":"gpt-5.2",
				"service_tier":"auto",
				"input":[{"role":"user","content":"hello"}]
			}`,
		},
		{
			name: "tool aliases",
			body: `{
				"model":"gpt-5.4-mini",
				"input":[{"role":"user","content":"search"}],
				"tools":[
					{"type":"web_search_preview"},
					{"type":"function","name":"noop","parameters":{"type":"object"}}
				],
				"tool_choice":{
					"type":"allowed_tools",
					"tools":[{"type":"web_search_preview_2025_03_11"}]
				}
			}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ConvertOpenAIResponsesRequestToCodex("gpt-5.2", []byte(tt.body), false)
			want := convertOpenAIResponsesRequestToCodexLegacy("gpt-5.2", []byte(tt.body))
			assertJSONSemanticallyEqual(t, want, got)
		})
	}
}

func TestRewriteOpenAIResponsesRequestObjectForCodexMatchesConverter(t *testing.T) {
	inputJSON := []byte(`{
		"model":"gpt-5.4-mini",
		"input":[
			{"type":"message","role":"system","content":[{"type":"input_text","text":"system"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hello <world>"}]}
		],
		"tools":[{"type":"web_search_preview"}],
		"tool_choice":{"type":"web_search_preview_2025_03_11"},
		"service_tier":"auto",
		"max_output_tokens":1024,
		"context_management":[{"type":"compaction"}],
		"user":"abc"
	}`)

	obj, ok := RewriteOpenAIResponsesRequestObjectForCodex(inputJSON)
	if !ok {
		t.Fatal("RewriteOpenAIResponsesRequestObjectForCodex returned false")
	}
	got, err := MarshalCodexRequestObject(obj)
	if err != nil {
		t.Fatalf("MarshalCodexRequestObject error: %v", err)
	}
	want := ConvertOpenAIResponsesRequestToCodex("gpt-5.4-mini", inputJSON, false)
	assertJSONSemanticallyEqual(t, want, got)
}

func TestConvertOpenAIResponsesRequestToCodex_NormalizesEscapedWebSearchAlias(t *testing.T) {
	inputJSON := []byte(`{
		"model":"gpt-5.4-mini",
		"input":[{"role":"user","content":"search"}],
		"tools":[{"type":"web_search\u005fpreview"}],
		"tool_choice":{"type":"web_search\u005fpreview_2025_03_11"}
	}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5.4-mini", inputJSON, false)
	if got := gjson.GetBytes(output, "tools.0.type").String(); got != "web_search" {
		t.Fatalf("tools.0.type = %q, want web_search: %s", got, string(output))
	}
	if got := gjson.GetBytes(output, "tool_choice.type").String(); got != "web_search" {
		t.Fatalf("tool_choice.type = %q, want web_search: %s", got, string(output))
	}
}

func TestRewriteResponsesInputDispatchesAfterWhitespace(t *testing.T) {
	t.Run("string input", func(t *testing.T) {
		output := rewriteResponsesInput(json.RawMessage(" \n\t\"hello <world>\""))
		if got := gjson.GetBytes(output, "0.role").String(); got != "user" {
			t.Fatalf("role = %q, want user: %s", got, string(output))
		}
		if got := gjson.GetBytes(output, "0.content.0.text").String(); got != "hello <world>" {
			t.Fatalf("text = %q, want hello <world>: %s", got, string(output))
		}
	})

	t.Run("array input", func(t *testing.T) {
		output := rewriteResponsesInput(json.RawMessage(` 
			[{"type":"message","role":"system","content":"hello"}]`))
		if got := gjson.GetBytes(output, "0.role").String(); got != "developer" {
			t.Fatalf("role = %q, want developer: %s", got, string(output))
		}
	})

	t.Run("other input", func(t *testing.T) {
		input := json.RawMessage(` 
			{"type":"message","role":"system","content":"hello"}`)
		output := rewriteResponsesInput(input)
		if !bytes.Equal(output, input) {
			t.Fatalf("object input should be unchanged\nwant: %s\n got: %s", string(input), string(output))
		}
	})
}

func assertJSONSemanticallyEqual(t *testing.T, want []byte, got []byte) {
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

var benchmarkConvertOutput []byte

func BenchmarkConvertOpenAIResponsesRequestToCodex_LargePayload(b *testing.B) {
	rawJSON := largeResponsesPayload(500, 500, true)
	validateLargeResponsesOutput(b, ConvertOpenAIResponsesRequestToCodex("gpt-5.4-mini", rawJSON, false))

	b.ReportAllocs()
	b.SetBytes(int64(len(rawJSON)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkConvertOutput = ConvertOpenAIResponsesRequestToCodex("gpt-5.4-mini", rawJSON, false)
	}
}

func BenchmarkConvertOpenAIResponsesRequestToCodexLegacy_LargePayload(b *testing.B) {
	rawJSON := largeResponsesPayload(500, 500, true)
	validateLargeResponsesOutput(b, convertOpenAIResponsesRequestToCodexLegacy("gpt-5.4-mini", rawJSON))

	b.ReportAllocs()
	b.SetBytes(int64(len(rawJSON)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkConvertOutput = convertOpenAIResponsesRequestToCodexLegacy("gpt-5.4-mini", rawJSON)
	}
}

func BenchmarkConvertSystemRoleToDeveloper_LargeInput(b *testing.B) {
	const inputItems = 500
	var payload bytes.Buffer
	payload.WriteString(`{"model":"gpt-5.2","input":[`)
	for i := 0; i < inputItems; i++ {
		if i > 0 {
			payload.WriteByte(',')
		}
		role := "user"
		if i == 0 {
			role = "system"
		}
		fmt.Fprintf(&payload, `{"type":"message","role":%q,"content":[{"type":"input_text","text":%q}]}`, role, "large payload text")
	}
	payload.WriteString(`]}`)
	rawJSON := payload.Bytes()

	b.ReportAllocs()
	b.SetBytes(int64(len(rawJSON)))
	for i := 0; i < b.N; i++ {
		out := convertSystemRoleToDeveloper(rawJSON)
		if got := gjson.GetBytes(out, "input.0.role").String(); got != "developer" {
			b.Fatalf("input.0.role = %q, want developer", got)
		}
	}
}

func largeResponsesPayload(inputItems int, toolItems int, includeAliases bool) []byte {
	var payload bytes.Buffer
	payload.WriteString(`{"model":"gpt-5.4-mini","input":[`)
	for i := 0; i < inputItems; i++ {
		if i > 0 {
			payload.WriteByte(',')
		}
		role := "user"
		if i == 0 {
			role = "system"
		}
		fmt.Fprintf(&payload, `{"type":"message","role":%q,"content":[{"type":"input_text","text":%q}]}`, role, "large input payload text")
	}
	payload.WriteString(`],"tools":[`)
	for i := 0; i < toolItems; i++ {
		if i > 0 {
			payload.WriteByte(',')
		}
		toolType := "function"
		if includeAliases && i == 0 {
			toolType = "web_search_preview"
		}
		fmt.Fprintf(&payload, `{"type":%q,"name":"tool_%d","description":%q,"parameters":{"type":"object","properties":{"query":{"type":"string","description":%q}}}}`, toolType, i, "large tool payload text", "large parameter payload text")
	}
	payload.WriteString(`],"tool_choice":{"type":"allowed_tools","tools":[`)
	if includeAliases {
		payload.WriteString(`{"type":"web_search_preview_2025_03_11"}`)
	}
	payload.WriteString(`]},"max_output_tokens":1024,"temperature":0.7,"top_p":0.9,"truncation":"disabled","user":"benchmark-user"}`)
	return payload.Bytes()
}

func validateLargeResponsesOutput(tb testing.TB, output []byte) {
	tb.Helper()
	if got := gjson.GetBytes(output, "input.0.role").String(); got != "developer" {
		tb.Fatalf("input.0.role = %q, want developer", got)
	}
	if got := gjson.GetBytes(output, "tools.0.type").String(); got != "web_search" {
		tb.Fatalf("tools.0.type = %q, want web_search", got)
	}
	if got := gjson.GetBytes(output, "tool_choice.tools.0.type").String(); got != "web_search" {
		tb.Fatalf("tool_choice.tools.0.type = %q, want web_search", got)
	}
	if gjson.GetBytes(output, "temperature").Exists() {
		tb.Fatalf("temperature should be removed")
	}
	if !gjson.GetBytes(output, "stream").Bool() {
		tb.Fatalf("stream should be true")
	}
}

func BenchmarkNormalizeCodexBuiltinTools_LargeInput(b *testing.B) {
	const toolItems = 500
	var payload bytes.Buffer
	payload.WriteString(`{"model":"gpt-5.4-mini","tools":[`)
	for i := 0; i < toolItems; i++ {
		if i > 0 {
			payload.WriteByte(',')
		}
		toolType := "function"
		if i == 0 {
			toolType = "web_search_preview"
		}
		fmt.Fprintf(&payload, `{"type":%q,"name":"tool_%d","description":%q,"parameters":{"type":"object","properties":{"query":{"type":"string","description":%q}}}}`, toolType, i, "large tool payload text", "large parameter payload text")
	}
	payload.WriteString(`],"tool_choice":{"type":"allowed_tools","tools":[{"type":"web_search_preview_2025_03_11"}]}}`)
	rawJSON := payload.Bytes()

	b.ReportAllocs()
	b.SetBytes(int64(len(rawJSON)))
	for i := 0; i < b.N; i++ {
		out := normalizeCodexBuiltinTools(rawJSON)
		if got := gjson.GetBytes(out, "tools.0.type").String(); got != "web_search" {
			b.Fatalf("tools.0.type = %q, want web_search", got)
		}
		if got := gjson.GetBytes(out, "tool_choice.tools.0.type").String(); got != "web_search" {
			b.Fatalf("tool_choice.tools.0.type = %q, want web_search", got)
		}
	}
}

func BenchmarkNormalizeCodexBuiltinTools_LargeInputNoAlias(b *testing.B) {
	const toolItems = 500
	var payload bytes.Buffer
	payload.WriteString(`{"model":"gpt-5.4-mini","tools":[`)
	for i := 0; i < toolItems; i++ {
		if i > 0 {
			payload.WriteByte(',')
		}
		fmt.Fprintf(&payload, `{"type":"function","name":"tool_%d","description":%q,"parameters":{"type":"object","properties":{"query":{"type":"string","description":%q}}}}`, i, "large tool payload text", "large parameter payload text")
	}
	payload.WriteString(`],"tool_choice":{"type":"allowed_tools"}}`)
	rawJSON := payload.Bytes()

	b.ReportAllocs()
	b.SetBytes(int64(len(rawJSON)))
	for i := 0; i < b.N; i++ {
		out := normalizeCodexBuiltinTools(rawJSON)
		if !bytes.Equal(out, rawJSON) {
			b.Fatalf("payload without aliases should not be modified")
		}
	}
}
