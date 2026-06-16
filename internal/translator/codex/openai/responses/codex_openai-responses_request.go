package responses

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"unsafe"

	"github.com/bytedance/sonic"
	gojson "github.com/goccy/go-json"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/jsonx"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// sonicNoEscape marshals WITHOUT HTML escaping, matching the verbatim fast path /
// marshalJSONNoEscape semantics. Used when the codex.req.marshal component is on sonic.
var sonicNoEscape = sonic.Config{EscapeHTML: false}.Froze()

// CodexRequestObject is the top-level Responses request object after Codex compatibility rewrites.
type CodexRequestObject = map[string]json.RawMessage

// codexTopLevelDecoder splits a Responses request body into its top-level
// key -> raw value map (values kept as opaque json.RawMessage). Implementations
// must be equivalent for valid JSON; they differ only in speed and in how strictly
// they reject malformed input.
type codexTopLevelDecoder func([]byte) (CodexRequestObject, bool)

// codexDecoder selects the inbound top-level decoder. Default is the gjson
// zero-copy split (fastest). Override at startup via env:
//
//	CODEX_DECODER=go-json  -> goccy/go-json, validating, ~2-3x (safe fallback)
//	CODEX_DECODER=stdlib   -> encoding/json, exact legacy behavior
var codexDecoder = decodeTopLevelGjson

func init() {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CODEX_DECODER"))) {
	case "go-json", "gojson":
		codexDecoder = decodeTopLevelGoJSON
	case "stdlib", "encoding/json", "json":
		codexDecoder = decodeTopLevelStdlib
	}
}

// decodeTopLevelStdlib uses encoding/json (full validation). Retained as the
// behavioral baseline / differential reference and as an opt-in fallback.
func decodeTopLevelStdlib(inputRawJSON []byte) (CodexRequestObject, bool) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(inputRawJSON, &obj); err != nil || obj == nil {
		return nil, false
	}
	return obj, true
}

// decodeTopLevelGoJSON uses goccy/go-json: identical full-validation +
// json.RawMessage semantics to encoding/json (so the malformed-input contract is
// preserved exactly), but ~2-3x faster. Pure Go, uniform across amd64/arm64.
func decodeTopLevelGoJSON(inputRawJSON []byte) (CodexRequestObject, bool) {
	var obj map[string]json.RawMessage
	if err := gojson.Unmarshal(inputRawJSON, &obj); err != nil || obj == nil {
		return nil, false
	}
	return obj, true
}

// decodeTopLevelGjson splits ONLY the top level with gjson, storing each value as
// a json.RawMessage that ALIASES gjson's parse buffer (no per-value copy). It does
// NOT deeply validate nested JSON, so a malformed body is accepted here and — as
// today — rejected by the Codex upstream (the legacy slow path is equally lenient
// and also forwards). For valid JSON the resulting map is equivalent to
// encoding/json, verified by differential tests.
//
// ALIASING INVARIANT: the returned RawMessage values must be treated as read-only.
// Downstream code (finalizeCodexRequestObject, MarshalCodexRequestObjectFast)
// REPLACES map entries rather than mutating value bytes in place, which keeps the
// aliasing safe; a regression test asserts the source body is not mutated.
func decodeTopLevelGjson(inputRawJSON []byte) (CodexRequestObject, bool) {
	root := gjson.ParseBytes(inputRawJSON)
	if !root.IsObject() {
		return nil, false
	}
	obj := make(CodexRequestObject, 16)
	root.ForEach(func(key, value gjson.Result) bool {
		obj[key.String()] = json.RawMessage(unsafeStringToBytes(value.Raw))
		return true
	})
	// An empty object yields an empty (non-nil) map, matching json.Unmarshal("{}").
	return obj, true
}

// unsafeStringToBytes returns a []byte aliasing s without copying. The result MUST
// NOT be mutated. Used only for read-only RawMessage values produced by gjson.
func unsafeStringToBytes(s string) []byte {
	if len(s) == 0 {
		return nil
	}
	return unsafe.Slice(unsafe.StringData(s), len(s))
}

func ConvertOpenAIResponsesRequestToCodex(modelName string, inputRawJSON []byte, _ bool) []byte {
	if rawJSON, ok := rewriteOpenAIResponsesRequestForCodex(inputRawJSON); ok {
		return rawJSON
	}

	return convertOpenAIResponsesRequestToCodexLegacy(modelName, inputRawJSON)
}

func convertOpenAIResponsesRequestToCodexLegacy(modelName string, inputRawJSON []byte) []byte {
	rawJSON := inputRawJSON

	inputResult := gjson.GetBytes(rawJSON, "input")
	if inputResult.Type == gjson.String {
		input, _ := sjson.SetBytes([]byte(`[{"type":"message","role":"user","content":[{"type":"input_text","text":""}]}]`), "0.content.0.text", inputResult.String())
		rawJSON, _ = sjson.SetRawBytes(rawJSON, "input", input)
	}

	rawJSON, _ = sjson.SetBytes(rawJSON, "stream", true)
	rawJSON, _ = sjson.SetBytes(rawJSON, "store", false)
	rawJSON, _ = sjson.SetBytes(rawJSON, "parallel_tool_calls", true)
	rawJSON, _ = sjson.SetBytes(rawJSON, "include", []string{"reasoning.encrypted_content"})
	// Codex Responses rejects token limit fields, so strip them out before forwarding.
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "max_output_tokens")
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "max_completion_tokens")
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "temperature")
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "top_p")
	if v := gjson.GetBytes(rawJSON, "service_tier"); v.Exists() {
		if v.String() != "priority" {
			rawJSON, _ = sjson.DeleteBytes(rawJSON, "service_tier")
		}
	}

	rawJSON, _ = sjson.DeleteBytes(rawJSON, "truncation")
	rawJSON = applyResponsesCompactionCompatibility(rawJSON)

	// Delete the user field as it is not supported by the Codex upstream.
	rawJSON, _ = sjson.DeleteBytes(rawJSON, "user")

	// Convert role "system" to "developer" in input array to comply with Codex API requirements.
	rawJSON = convertSystemRoleToDeveloper(rawJSON)
	rawJSON = normalizeCodexBuiltinTools(rawJSON)

	return rawJSON
}

func rewriteOpenAIResponsesRequestForCodex(inputRawJSON []byte) ([]byte, bool) {
	obj, ok := RewriteOpenAIResponsesRequestObjectForCodex(inputRawJSON)
	if !ok {
		return nil, false
	}

	rawJSON, err := MarshalCodexRequestObject(obj)
	if err != nil {
		return nil, false
	}
	return rawJSON, true
}

// RewriteOpenAIResponsesRequestObjectForCodex parses and rewrites an OpenAI Responses request for Codex without marshaling it.
func RewriteOpenAIResponsesRequestObjectForCodex(inputRawJSON []byte) (CodexRequestObject, bool) {
	obj, ok := codexDecoder(inputRawJSON)
	if !ok {
		return nil, false
	}
	RewriteOpenAIResponsesRequestObjectFieldsForCodex(obj)
	return obj, true
}

// RewriteOpenAIResponsesRequestObjectForCodexFast is like
// RewriteOpenAIResponsesRequestObjectForCodex but DEFERS the input-array
// system->developer rewrite to the caller. The Codex executor fast path folds that
// rewrite into its single combined sanitize+rewrite walk over obj["input"], so doing
// it here too would scan the input array twice. String input is still normalized.
func RewriteOpenAIResponsesRequestObjectForCodexFast(inputRawJSON []byte) (CodexRequestObject, bool) {
	obj, ok := codexDecoder(inputRawJSON)
	if !ok {
		return nil, false
	}
	rewriteOpenAIResponsesRequestObjectFieldsForCodex(obj, false)
	return obj, true
}

// RewriteCodexInputItemSystemRole converts a single Responses input item's role from
// "system" to "developer" (Codex compatibility). It returns the item unchanged with
// changed=false when the item's role is not "system". Exported so the executor can
// apply the same per-item rewrite inside its combined input walk.
func RewriteCodexInputItemSystemRole(itemRaw []byte) ([]byte, bool) {
	if gjson.GetBytes(itemRaw, "role").String() != "system" {
		return itemRaw, false
	}
	return rewriteJSONObjectStringField(itemRaw, "role", "developer")
}

// RewriteOpenAIResponsesRequestObjectFieldsForCodex applies Codex compatibility rewrites to a parsed request object.
func RewriteOpenAIResponsesRequestObjectFieldsForCodex(obj CodexRequestObject) {
	rewriteOpenAIResponsesRequestObjectFieldsForCodex(obj, true)
}

// rewriteOpenAIResponsesRequestObjectFieldsForCodex applies the Codex compatibility
// rewrites. When rewriteInputArraySystemRoles is false the input-array
// system->developer rewrite is skipped (string input is still normalized).
func rewriteOpenAIResponsesRequestObjectFieldsForCodex(obj CodexRequestObject, rewriteInputArraySystemRoles bool) {
	if obj == nil {
		return
	}

	if rawInput, exists := obj["input"]; exists {
		obj["input"] = rewriteResponsesInput(rawInput, rewriteInputArraySystemRoles)
	}

	obj["stream"] = json.RawMessage(`true`)
	obj["store"] = json.RawMessage(`false`)
	obj["parallel_tool_calls"] = json.RawMessage(`true`)
	obj["include"] = json.RawMessage(`["reasoning.encrypted_content"]`)

	delete(obj, "max_output_tokens")
	delete(obj, "max_completion_tokens")
	delete(obj, "temperature")
	delete(obj, "top_p")
	delete(obj, "truncation")
	delete(obj, "context_management")
	delete(obj, "user")

	if rawServiceTier, exists := obj["service_tier"]; exists && !rawJSONEqualsString(rawServiceTier, "priority") {
		delete(obj, "service_tier")
	}

	if rawTools, exists := obj["tools"]; exists {
		if rewrittenTools, changed := rewriteCodexBuiltinToolArray(rawTools); changed {
			obj["tools"] = rewrittenTools
		}
	}

	if rawToolChoice, exists := obj["tool_choice"]; exists {
		if rewrittenToolChoice, changed := rewriteCodexToolChoice(rawToolChoice); changed {
			obj["tool_choice"] = rewrittenToolChoice
		}
	}
}

// MarshalCodexRequestObject marshals a rewritten Codex request object without HTML escaping.
func MarshalCodexRequestObject(obj CodexRequestObject) ([]byte, error) {
	return marshalJSONNoEscape(obj)
}

// MarshalCodexRequestObjectFast serializes a Codex request object WITHOUT
// re-compacting each value. Every value in obj is already a valid JSON
// RawMessage — either copied verbatim from the source payload (which was parsed
// with json.Unmarshal, so it is valid) or freshly produced by the rewrite /
// finalize step — so values are emitted as-is. This avoids the O(n) re-compaction
// of the large unchanged "input" array that MarshalCodexRequestObject performs via
// json.Encoder on every request.
//
// Keys are emitted in sorted order (matching encoding/json's map key ordering) and
// encoded without HTML escaping, so the result is JSON-equivalent to
// MarshalCodexRequestObject. It is not necessarily BYTE-identical: untouched values
// keep their original insignificant whitespace instead of being minified. Callers
// MUST only use this when obj's values are known-valid JSON (the Codex fast path
// guarantees this).
func MarshalCodexRequestObjectFast(obj CodexRequestObject) ([]byte, error) {
	if jsonx.UseSonic("codex.req.marshal") {
		if len(obj) == 0 {
			return []byte(`{}`), nil
		}
		// sonic re-serializes (incl. the input array), benchmark-neutral vs the
		// verbatim path; JSON-equivalent (key order / value whitespace may differ).
		return sonicNoEscape.Marshal(obj)
	}
	return marshalCodexRequestObjectFastVerbatim(obj)
}

func marshalCodexRequestObjectFastVerbatim(obj CodexRequestObject) ([]byte, error) {
	if len(obj) == 0 {
		return []byte(`{}`), nil
	}

	keys := make([]string, 0, len(obj))
	estimate := 2 // surrounding braces
	for k, v := range obj {
		keys = append(keys, k)
		estimate += len(k) + len(v) + 4 // quotes, colon, comma
	}
	sort.Strings(keys)

	var buf bytes.Buffer
	buf.Grow(estimate)
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		keyJSON, err := marshalJSONNoEscape(k)
		if err != nil {
			return nil, err
		}
		buf.Write(keyJSON)
		buf.WriteByte(':')
		// Values are already valid JSON; copy them verbatim (no re-compaction).
		// TrimSpace keeps the output tidy and matches RawMessage value semantics;
		// an empty/nil value marshals to null, as encoding/json would.
		v := bytes.TrimSpace(obj[k])
		if len(v) == 0 {
			v = []byte("null")
		}
		buf.Write(v)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func rewriteResponsesInput(rawInput json.RawMessage, rewriteArraySystemRoles bool) json.RawMessage {
	switch firstNonSpaceJSONByte(rawInput) {
	case '"':
		inputText, ok := decodeJSONString(rawInput)
		if !ok {
			return rawInput
		}
		rawJSON, err := marshalJSONNoEscape([]struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}{
			{
				Type: "message",
				Role: "user",
				Content: []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}{
					{Type: "input_text", Text: inputText},
				},
			},
		})
		if err == nil {
			return rawJSON
		}
		return rawInput
	case '[':
		if rewriteArraySystemRoles {
			if rewrittenInput, changed := rewriteSystemRolesInInputArray(rawInput); changed {
				return rewrittenInput
			}
		}
	}
	return rawInput
}

func firstNonSpaceJSONByte(rawJSON []byte) byte {
	for _, b := range rawJSON {
		switch b {
		case ' ', '\n', '\r', '\t':
			continue
		default:
			return b
		}
	}
	return 0
}

func rewriteSystemRolesInInputArray(rawInput []byte) ([]byte, bool) {
	inputResult := gjson.ParseBytes(rawInput)
	if !inputResult.IsArray() {
		return rawInput, false
	}

	hasSystemRole := false
	inputResult.ForEach(func(_, item gjson.Result) bool {
		if item.Get("role").String() == "system" {
			hasSystemRole = true
			return false
		}
		return true
	})
	if !hasSystemRole {
		return rawInput, false
	}

	var out bytes.Buffer
	out.Grow(len(rawInput))
	out.WriteByte('[')
	first := true
	inputResult.ForEach(func(_, item gjson.Result) bool {
		if !first {
			out.WriteByte(',')
		}
		first = false

		itemRaw := []byte(item.Raw)
		if rewritten, ok := RewriteCodexInputItemSystemRole(itemRaw); ok {
			itemRaw = rewritten
		}
		out.Write(itemRaw)
		return true
	})
	out.WriteByte(']')

	return out.Bytes(), true
}

func rewriteCodexBuiltinToolArray(rawTools []byte) ([]byte, bool) {
	toolsResult := gjson.ParseBytes(rawTools)
	if !toolsResult.IsArray() {
		return rawTools, false
	}

	hasAlias := false
	toolsResult.ForEach(func(_, item gjson.Result) bool {
		if normalizeCodexBuiltinToolType(item.Get("type").String()) != "" {
			hasAlias = true
			return false
		}
		return true
	})
	if !hasAlias {
		return rawTools, false
	}

	var out bytes.Buffer
	out.Grow(len(rawTools))
	out.WriteByte('[')
	first := true
	toolsResult.ForEach(func(_, item gjson.Result) bool {
		if !first {
			out.WriteByte(',')
		}
		first = false

		itemRaw := []byte(item.Raw)
		if normalizedType := normalizeCodexBuiltinToolType(item.Get("type").String()); normalizedType != "" {
			if updated, ok := rewriteJSONObjectStringField(itemRaw, "type", normalizedType); ok {
				itemRaw = updated
			}
		}
		out.Write(itemRaw)
		return true
	})
	out.WriteByte(']')

	return out.Bytes(), true
}

func rewriteCodexToolChoice(rawToolChoice []byte) ([]byte, bool) {
	toolChoiceResult := gjson.ParseBytes(rawToolChoice)
	if !toolChoiceResult.IsObject() {
		return rawToolChoice, false
	}

	changed := false
	var obj map[string]json.RawMessage
	if normalizedType := normalizeCodexBuiltinToolType(toolChoiceResult.Get("type").String()); normalizedType != "" {
		if obj == nil {
			if !decodeJSONObject(rawToolChoice, &obj) {
				return rawToolChoice, false
			}
		}
		if rawString, err := marshalJSONNoEscape(normalizedType); err == nil {
			obj["type"] = rawString
			changed = true
		}
	}

	toolsResult := toolChoiceResult.Get("tools")
	if toolsResult.IsArray() {
		if rewrittenTools, toolsChanged := rewriteCodexBuiltinToolArray([]byte(toolsResult.Raw)); toolsChanged {
			if obj == nil {
				if !decodeJSONObject(rawToolChoice, &obj) {
					return rawToolChoice, false
				}
			}
			obj["tools"] = rewrittenTools
			changed = true
		}
	}

	if !changed {
		return rawToolChoice, false
	}
	result, err := marshalJSONNoEscape(obj)
	if err != nil {
		return rawToolChoice, false
	}
	return result, true
}

func rewriteJSONObjectStringField(rawJSON []byte, field string, value string) ([]byte, bool) {
	var obj map[string]json.RawMessage
	if !decodeJSONObject(rawJSON, &obj) {
		return rawJSON, false
	}
	rawValue, err := marshalJSONNoEscape(value)
	if err != nil {
		return rawJSON, false
	}
	obj[field] = rawValue
	result, err := marshalJSONNoEscape(obj)
	if err != nil {
		return rawJSON, false
	}
	return result, true
}

func decodeJSONObject(rawJSON []byte, obj *map[string]json.RawMessage) bool {
	if err := json.Unmarshal(rawJSON, obj); err != nil || obj == nil || *obj == nil {
		return false
	}
	return true
}

func rawJSONEqualsString(rawJSON []byte, expected string) bool {
	value, ok := decodeJSONString(rawJSON)
	return ok && value == expected
}

func decodeJSONString(rawJSON []byte) (string, bool) {
	var value string
	if err := json.Unmarshal(rawJSON, &value); err != nil {
		return "", false
	}
	return value, true
}

func marshalJSONNoEscape(value any) ([]byte, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}

// applyResponsesCompactionCompatibility handles OpenAI Responses context_management.compaction
// for Codex upstream compatibility.
//
// Codex /responses currently rejects context_management with:
// {"detail":"Unsupported parameter: context_management"}.
//
// Compatibility strategy:
// 1) Remove context_management before forwarding to Codex upstream.
func applyResponsesCompactionCompatibility(rawJSON []byte) []byte {
	if !gjson.GetBytes(rawJSON, "context_management").Exists() {
		return rawJSON
	}

	rawJSON, _ = sjson.DeleteBytes(rawJSON, "context_management")
	return rawJSON
}

// convertSystemRoleToDeveloper traverses the input array and converts any message items
// with role "system" to role "developer". This is necessary because Codex API does not
// accept "system" role in the input array.
func convertSystemRoleToDeveloper(rawJSON []byte) []byte {
	inputResult := gjson.GetBytes(rawJSON, "input")
	if !inputResult.IsArray() {
		return rawJSON
	}

	inputArray := inputResult.Array()
	result := rawJSON

	// Use the already parsed input item instead of re-querying the full payload
	// for every index. Large Responses payloads make full-document path lookups
	// very expensive under concurrency.
	for i := 0; i < len(inputArray); i++ {
		if inputArray[i].Get("role").String() == "system" {
			rolePath := fmt.Sprintf("input.%d.role", i)
			result, _ = sjson.SetBytes(result, rolePath, "developer")
		}
	}

	return result
}

// normalizeCodexBuiltinTools rewrites legacy/preview built-in tool variants to the
// stable names expected by the current Codex upstream.
func normalizeCodexBuiltinTools(rawJSON []byte) []byte {
	if !bytes.Contains(rawJSON, []byte("web_search_preview")) {
		return rawJSON
	}

	result := rawJSON

	tools := gjson.GetBytes(result, "tools")
	if tools.IsArray() {
		toolArray := tools.Array()
		for i := 0; i < len(toolArray); i++ {
			typePath := fmt.Sprintf("tools.%d.type", i)
			result = normalizeCodexBuiltinToolAtPath(result, typePath, toolArray[i].Get("type").String())
		}
	}

	toolChoice := gjson.GetBytes(result, "tool_choice")
	result = normalizeCodexBuiltinToolAtPath(result, "tool_choice.type", toolChoice.Get("type").String())

	toolChoiceTools := toolChoice.Get("tools")
	if toolChoiceTools.IsArray() {
		toolArray := toolChoiceTools.Array()
		for i := 0; i < len(toolArray); i++ {
			typePath := fmt.Sprintf("tool_choice.tools.%d.type", i)
			result = normalizeCodexBuiltinToolAtPath(result, typePath, toolArray[i].Get("type").String())
		}
	}

	return result
}

func normalizeCodexBuiltinToolAtPath(rawJSON []byte, path string, currentType string) []byte {
	normalizedType := normalizeCodexBuiltinToolType(currentType)
	if normalizedType == "" {
		return rawJSON
	}

	updated, err := sjson.SetBytes(rawJSON, path, normalizedType)
	if err != nil {
		return rawJSON
	}

	log.Debugf("codex responses: normalized builtin tool type at %s from %q to %q", path, currentType, normalizedType)
	return updated
}

// normalizeCodexBuiltinToolType centralizes the current known Codex Responses
// built-in tool alias compatibility. If Codex introduces more legacy aliases,
// extend this helper instead of adding path-specific rewrite logic elsewhere.
func normalizeCodexBuiltinToolType(toolType string) string {
	switch toolType {
	case "web_search_preview", "web_search_preview_2025_03_11":
		return "web_search"
	default:
		return ""
	}
}
