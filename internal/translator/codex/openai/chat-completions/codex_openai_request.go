// Package openai provides utilities to translate OpenAI Chat Completions
// request JSON into OpenAI Responses API request JSON.
// It supports tools, multimodal text/image inputs, and Structured Outputs.
// The package handles the conversion of OpenAI API requests into the format
// expected by the OpenAI Responses API, including proper mapping of messages,
// tools, and generation parameters.
package chat_completions

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// ConvertOpenAIRequestToCodex converts an OpenAI Chat Completions request JSON
// into an OpenAI Responses API request JSON. The transformation follows the
// examples defined in docs/2.md exactly, including tools, multi-turn dialog,
// multimodal text/image handling, and Structured Outputs mapping.
//
// Parameters:
//   - modelName: The name of the model to use for the request
//   - rawJSON: The raw JSON request data from the OpenAI Chat Completions API
//   - stream: A boolean indicating if the request is for a streaming response
//
// Returns:
//   - []byte: The transformed request data in OpenAI Responses API format
func ConvertOpenAIRequestToCodex(modelName string, inputRawJSON []byte, stream bool) []byte {
	root := gjson.ParseBytes(inputRawJSON)
	out := map[string]any{
		"instructions":        "",
		"stream":              stream,
		"parallel_tool_calls": true,
		"reasoning": map[string]any{
			"effort":  "medium",
			"summary": "auto",
		},
		"include": []string{"reasoning.encrypted_content"},
		"model":   modelName,
		"input":   []any{},
		"store":   false,
	}

	if v := root.Get("reasoning_effort"); v.Exists() {
		out["reasoning"].(map[string]any)["effort"] = v.Value()
	}

	// Build tool name shortening map from original tools (if any)
	originalToolNameMap := map[string]string{}
	if tools := root.Get("tools"); tools.IsArray() {
		var names []string
		tools.ForEach(func(_, t gjson.Result) bool {
			if t.Get("type").String() == "function" {
				fn := t.Get("function")
				if fn.Exists() {
					if v := fn.Get("name"); v.Exists() {
						names = append(names, v.String())
					}
				}
			}
			return true
		})
		if len(names) > 0 {
			originalToolNameMap = buildShortNameMap(names)
		}
	}

	// Extract system instructions from first system message (string or text object)
	messages := root.Get("messages")
	// if messages.IsArray() {
	// 	arr := messages.Array()
	// 	for i := 0; i < len(arr); i++ {
	// 		m := arr[i]
	// 		if m.Get("role").String() == "system" {
	// 			c := m.Get("content")
	// 			if c.Type == gjson.String {
	// 				out, _ = sjson.SetBytes(out, "instructions", c.String())
	// 			} else if c.IsObject() && c.Get("type").String() == "text" {
	// 				out, _ = sjson.SetBytes(out, "instructions", c.Get("text").String())
	// 			}
	// 			break
	// 		}
	// 	}
	// }

	// Build input from messages, handling all message types including tool calls
	inputItems := []any{}
	if messages.IsArray() {
		arr := messages.Array()
		for i := 0; i < len(arr); i++ {
			m := arr[i]
			role := m.Get("role").String()

			switch role {
			case "tool":
				// Handle tool response messages as top-level function_call_output objects
				toolCallID := m.Get("tool_call_id").String()
				content := m.Get("content")

				funcOutput := map[string]any{
					"type":    "function_call_output",
					"call_id": toolCallID,
				}
				setToolCallOutputContent(funcOutput, content)
				inputItems = append(inputItems, funcOutput)

			default:
				// Handle regular messages
				msg := map[string]any{"type": "message"}
				if role == "system" {
					msg["role"] = "developer"
				} else {
					msg["role"] = role
				}

				contentParts := []any{}

				// Handle regular content
				c := m.Get("content")
				if c.Exists() && c.Type == gjson.String && c.String() != "" {
					// Single string content
					partType := "input_text"
					if role == "assistant" {
						partType = "output_text"
					}
					contentParts = append(contentParts, map[string]any{
						"type": partType,
						"text": c.String(),
					})
				} else if c.Exists() && c.IsArray() {
					items := c.Array()
					for j := 0; j < len(items); j++ {
						it := items[j]
						t := it.Get("type").String()
						switch t {
						case "text":
							partType := "input_text"
							if role == "assistant" {
								partType = "output_text"
							}
							contentParts = append(contentParts, map[string]any{
								"type": partType,
								"text": it.Get("text").String(),
							})
						case "image_url":
							// Map image inputs to input_image for Responses API
							if role == "user" {
								part := map[string]any{"type": "input_image"}
								if u := it.Get("image_url.url"); u.Exists() {
									part["image_url"] = u.String()
								}
								contentParts = append(contentParts, part)
							}
						case "file":
							if role == "user" {
								fileData := it.Get("file.file_data").String()
								filename := it.Get("file.filename").String()
								if fileData != "" {
									part := map[string]any{
										"type":      "input_file",
										"file_data": fileData,
									}
									if filename != "" {
										part["filename"] = filename
									}
									contentParts = append(contentParts, part)
								}
							}
						}
					}
				}

				// Don't emit empty assistant messages when only tool_calls
				// are present — Responses API needs function_call items
				// directly, otherwise call_id matching fails (#2132).
				msg["content"] = contentParts
				if role != "assistant" || len(contentParts) > 0 {
					inputItems = append(inputItems, msg)
				}

				// Handle tool calls for assistant messages as separate top-level objects
				if role == "assistant" {
					toolCalls := m.Get("tool_calls")
					if toolCalls.Exists() && toolCalls.IsArray() {
						toolCallsArr := toolCalls.Array()
						for j := 0; j < len(toolCallsArr); j++ {
							tc := toolCallsArr[j]
							if tc.Get("type").String() == "function" {
								name := tc.Get("function.name").String()
								if short, ok := originalToolNameMap[name]; ok {
									name = short
								} else {
									name = shortenNameIfNeeded(name)
								}
								inputItems = append(inputItems, map[string]any{
									"type":      "function_call",
									"call_id":   tc.Get("id").String(),
									"name":      name,
									"arguments": tc.Get("function.arguments").String(),
								})
							}
						}
					}
				}
			}
		}
	}
	out["input"] = inputItems

	// Map response_format and text settings to Responses API text.format
	rf := root.Get("response_format")
	text := root.Get("text")
	if rf.Exists() {
		// Always create text object when response_format provided
		textOut := map[string]any{}

		rft := rf.Get("type").String()
		switch rft {
		case "text":
			textOut["format"] = map[string]any{"type": "text"}
		case "json_schema":
			js := rf.Get("json_schema")
			if js.Exists() {
				format := map[string]any{"type": "json_schema"}
				if v := js.Get("name"); v.Exists() {
					format["name"] = v.Value()
				}
				if v := js.Get("strict"); v.Exists() {
					format["strict"] = v.Value()
				}
				if v := js.Get("schema"); v.Exists() {
					format["schema"] = json.RawMessage(v.Raw)
				}
				textOut["format"] = format
			}
		}

		// Map verbosity if provided
		if text.Exists() {
			if v := text.Get("verbosity"); v.Exists() {
				textOut["verbosity"] = v.Value()
			}
		}
		out["text"] = textOut
	} else if text.Exists() {
		// If only text.verbosity present (no response_format), map verbosity
		if v := text.Get("verbosity"); v.Exists() {
			out["text"] = map[string]any{"verbosity": v.Value()}
		}
	}

	// Map tools (flatten function fields)
	tools := root.Get("tools")
	if tools.IsArray() && len(tools.Array()) > 0 {
		outTools := []any{}
		arr := tools.Array()
		for i := 0; i < len(arr); i++ {
			t := arr[i]
			toolType := t.Get("type").String()
			// Pass through built-in tools (e.g. {"type":"web_search"}) directly for the Responses API.
			// Only "function" needs structural conversion because Chat Completions nests details under "function".
			if toolType != "" && toolType != "function" && t.IsObject() {
				outTools = append(outTools, json.RawMessage(t.Raw))
				continue
			}

			if toolType == "function" {
				item := map[string]any{"type": "function"}
				fn := t.Get("function")
				if fn.Exists() {
					if v := fn.Get("name"); v.Exists() {
						name := v.String()
						if short, ok := originalToolNameMap[name]; ok {
							name = short
						} else {
							name = shortenNameIfNeeded(name)
						}
						item["name"] = name
					}
					if v := fn.Get("description"); v.Exists() {
						item["description"] = v.Value()
					}
					if v := fn.Get("parameters"); v.Exists() {
						item["parameters"] = json.RawMessage(v.Raw)
					}
					if v := fn.Get("strict"); v.Exists() {
						item["strict"] = v.Value()
					}
				}
				outTools = append(outTools, item)
			}
		}
		out["tools"] = outTools
	}

	// Map tool_choice when present.
	// Chat Completions: "tool_choice" can be a string ("auto"/"none") or an object (e.g. {"type":"function","function":{"name":"..."}}).
	// Responses API: keep built-in tool choices as-is; flatten function choice to {"type":"function","name":"..."}.
	if tc := root.Get("tool_choice"); tc.Exists() {
		switch {
		case tc.Type == gjson.String:
			out["tool_choice"] = tc.String()
		case tc.IsObject():
			tcType := tc.Get("type").String()
			if tcType == "function" {
				name := tc.Get("function.name").String()
				if name != "" {
					if short, ok := originalToolNameMap[name]; ok {
						name = short
					} else {
						name = shortenNameIfNeeded(name)
					}
				}
				choice := map[string]any{"type": "function"}
				if name != "" {
					choice["name"] = name
				}
				out["tool_choice"] = choice
			} else if tcType != "" {
				// Built-in tool choices (e.g. {"type":"web_search"}) are already Responses-compatible.
				out["tool_choice"] = json.RawMessage(tc.Raw)
			}
		}
	}

	rawOut, err := marshalJSONNoEscape(out)
	if err != nil {
		if stream {
			return []byte(`{"instructions":"","input":[],"stream":true,"store":false}`)
		}
		return []byte(`{"instructions":"","input":[],"stream":false,"store":false}`)
	}
	return rawOut
}

func setToolCallOutputContent(funcOutput map[string]any, content gjson.Result) {
	switch {
	case content.Type == gjson.String:
		funcOutput["output"] = content.String()
	case content.IsArray():
		output := []any{}
		for _, item := range content.Array() {
			output = appendToolOutputContentPart(output, item)
		}
		funcOutput["output"] = output
	default:
		fallbackOutput := content.Raw
		if fallbackOutput == "" {
			fallbackOutput = content.String()
		}
		funcOutput["output"] = fallbackOutput
	}
}

func appendToolOutputContentPart(output []any, item gjson.Result) []any {
	switch item.Get("type").String() {
	case "text":
		output = append(output, map[string]any{
			"type": "input_text",
			"text": item.Get("text").String(),
		})
	case "image_url":
		imageURL := item.Get("image_url.url").String()
		fileID := item.Get("image_url.file_id").String()
		if imageURL == "" && fileID == "" {
			return appendToolOutputFallbackPart(output, item)
		}
		part := map[string]any{"type": "input_image"}
		if imageURL != "" {
			part["image_url"] = imageURL
		}
		if fileID != "" {
			part["file_id"] = fileID
		}
		if detail := item.Get("image_url.detail").String(); detail != "" {
			part["detail"] = detail
		}
		output = append(output, part)
	case "file":
		fileID := item.Get("file.file_id").String()
		fileData := item.Get("file.file_data").String()
		fileURL := item.Get("file.file_url").String()
		if fileID == "" && fileData == "" && fileURL == "" {
			return appendToolOutputFallbackPart(output, item)
		}
		part := map[string]any{"type": "input_file"}
		if fileID != "" {
			part["file_id"] = fileID
		}
		if fileData != "" {
			part["file_data"] = fileData
		}
		if fileURL != "" {
			part["file_url"] = fileURL
		}
		if filename := item.Get("file.filename").String(); filename != "" {
			part["filename"] = filename
		}
		output = append(output, part)
	default:
		output = appendToolOutputFallbackPart(output, item)
	}
	return output
}

func appendToolOutputFallbackPart(output []any, item gjson.Result) []any {
	text := item.Raw
	if text == "" {
		text = item.String()
	}
	return append(output, map[string]any{
		"type": "input_text",
		"text": text,
	})
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

// shortenNameIfNeeded applies the simple shortening rule for a single name.
// If the name length exceeds 64, it will try to preserve the "mcp__" prefix and last segment.
// Otherwise it truncates to 64 characters.
func shortenNameIfNeeded(name string) string {
	const limit = 64
	if len(name) <= limit {
		return name
	}
	if strings.HasPrefix(name, "mcp__") {
		// Keep prefix and last segment after '__'
		idx := strings.LastIndex(name, "__")
		if idx > 0 {
			candidate := "mcp__" + name[idx+2:]
			if len(candidate) > limit {
				return candidate[:limit]
			}
			return candidate
		}
	}
	return name[:limit]
}

// buildShortNameMap generates unique short names (<=64) for the given list of names.
// It preserves the "mcp__" prefix with the last segment when possible and ensures uniqueness
// by appending suffixes like "~1", "~2" if needed.
func buildShortNameMap(names []string) map[string]string {
	const limit = 64
	used := map[string]struct{}{}
	m := map[string]string{}

	baseCandidate := func(n string) string {
		if len(n) <= limit {
			return n
		}
		if strings.HasPrefix(n, "mcp__") {
			idx := strings.LastIndex(n, "__")
			if idx > 0 {
				cand := "mcp__" + n[idx+2:]
				if len(cand) > limit {
					cand = cand[:limit]
				}
				return cand
			}
		}
		return n[:limit]
	}

	makeUnique := func(cand string) string {
		if _, ok := used[cand]; !ok {
			return cand
		}
		base := cand
		for i := 1; ; i++ {
			suffix := "_" + strconv.Itoa(i)
			allowed := limit - len(suffix)
			if allowed < 0 {
				allowed = 0
			}
			tmp := base
			if len(tmp) > allowed {
				tmp = tmp[:allowed]
			}
			tmp = tmp + suffix
			if _, ok := used[tmp]; !ok {
				return tmp
			}
		}
	}

	for _, n := range names {
		cand := baseCandidate(n)
		uniq := makeUnique(cand)
		used[uniq] = struct{}{}
		m[n] = uniq
	}
	return m
}
