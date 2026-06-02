package executor

import (
	"context"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func sanitizeOpenAIResponsesReasoningEncryptedContent(ctx context.Context, provider string, body []byte) []byte {
	input := gjson.GetBytes(body, "input")
	if !input.Exists() || !input.IsArray() {
		return body
	}
	sanitized, changed := sanitizeReasoningEncryptedContentInput(ctx, provider, []byte(input.Raw))
	if !changed {
		return body
	}
	updated, err := sjson.SetRawBytes(body, "input", sanitized)
	if err != nil {
		return body
	}
	return updated
}

// sanitizeReasoningEncryptedContentInput sanitizes a Codex "input" array value (a
// json.RawMessage), dropping invalid reasoning encrypted_content fields. It returns
// the possibly-modified input array and whether anything was dropped.
//
// The Codex fast path calls this directly on the already-parsed obj["input"] BEFORE
// marshaling, so it never re-scans the whole (often multi-MB) request body to find
// "input". The whole-body wrapper above delegates here for the slow path. The result
// is JSON-equivalent to the previous whole-body sanitize; only invalid
// encrypted_content fields are removed.
func sanitizeReasoningEncryptedContentInput(ctx context.Context, provider string, inputRaw []byte) ([]byte, bool) {
	input := gjson.ParseBytes(inputRaw)
	if !input.IsArray() {
		return inputRaw, false
	}
	provider = strings.TrimSpace(provider)
	if provider == "" {
		provider = "openai responses upstream"
	}

	updated := inputRaw
	changed := false
	for index, item := range input.Array() {
		if strings.TrimSpace(item.Get("type").String()) != "reasoning" {
			continue
		}

		encryptedContent := item.Get("encrypted_content")
		if !encryptedContent.Exists() {
			continue
		}

		reason := ""
		switch encryptedContent.Type {
		case gjson.String:
			rawSignature := encryptedContent.String()
			if rawSignature != strings.TrimSpace(rawSignature) {
				reason = "encrypted_content has leading or trailing whitespace"
			} else if err := signature.InspectGPTReasoningSignatureCached(rawSignature); err != nil {
				reason = err.Error()
			}
		case gjson.Null:
			reason = "encrypted_content is null"
		default:
			reason = fmt.Sprintf("encrypted_content must be a string, got %s", encryptedContent.Type.String())
		}
		if reason == "" {
			continue
		}

		// Path is relative to the input array root (e.g. "3.encrypted_content").
		encryptedContentPath := fmt.Sprintf("%d.encrypted_content", index)
		next, err := sjson.DeleteBytes(updated, encryptedContentPath)
		if err != nil {
			helps.LogWithRequestID(ctx).Debugf("%s: failed to drop invalid reasoning encrypted_content at input[%d]: %v", provider, index, err)
			continue
		}
		updated = next
		changed = true

		itemID := strings.TrimSpace(item.Get("id").String())
		if itemID == "" {
			itemID = fmt.Sprintf("input[%d]", index)
		}
		helps.LogWithRequestID(ctx).Debugf("%s: dropped invalid reasoning encrypted_content at input[%d] item_id=%q reason=%s", provider, index, itemID, reason)
	}
	return updated, changed
}
