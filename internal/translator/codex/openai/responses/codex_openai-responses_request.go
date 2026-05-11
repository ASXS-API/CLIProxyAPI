package responses

import (
	"bytes"
	"encoding/json"
	"fmt"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

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
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(inputRawJSON, &obj); err != nil || obj == nil {
		return nil, false
	}

	if rawInput, exists := obj["input"]; exists {
		obj["input"] = rewriteResponsesInput(rawInput)
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

	rawJSON, err := marshalJSONNoEscape(obj)
	if err != nil {
		return nil, false
	}
	return rawJSON, true
}

func rewriteResponsesInput(rawInput json.RawMessage) json.RawMessage {
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
		if rewrittenInput, changed := rewriteSystemRolesInInputArray(rawInput); changed {
			return rewrittenInput
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
		if item.Get("role").String() == "system" {
			updated, err := sjson.SetBytes(itemRaw, "role", "developer")
			if err == nil {
				itemRaw = updated
			}
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
			updated, err := sjson.SetBytes(itemRaw, "type", normalizedType)
			if err == nil {
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

	result := rawToolChoice
	changed := false
	if normalizedType := normalizeCodexBuiltinToolType(toolChoiceResult.Get("type").String()); normalizedType != "" {
		updated, err := sjson.SetBytes(result, "type", normalizedType)
		if err == nil {
			result = updated
			changed = true
		}
	}

	toolsResult := gjson.GetBytes(result, "tools")
	if toolsResult.IsArray() {
		if rewrittenTools, toolsChanged := rewriteCodexBuiltinToolArray([]byte(toolsResult.Raw)); toolsChanged {
			updated, err := sjson.SetRawBytes(result, "tools", rewrittenTools)
			if err == nil {
				result = updated
				changed = true
			}
		}
	}

	return result, changed
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
