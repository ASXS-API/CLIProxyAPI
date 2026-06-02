package executor

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	codexresponses "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/codex/openai/responses"
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

// codexReasoningEncryptedContentDropReason returns a non-empty reason when a Codex
// input item is a reasoning item whose encrypted_content is invalid (and should be
// dropped), or "" otherwise. Validity is checked via the memoized signature cache.
func codexReasoningEncryptedContentDropReason(item gjson.Result) string {
	if strings.TrimSpace(item.Get("type").String()) != "reasoning" {
		return ""
	}
	encryptedContent := item.Get("encrypted_content")
	if !encryptedContent.Exists() {
		return ""
	}
	switch encryptedContent.Type {
	case gjson.String:
		rawSignature := encryptedContent.String()
		if rawSignature != strings.TrimSpace(rawSignature) {
			return "encrypted_content has leading or trailing whitespace"
		}
		if err := signature.InspectGPTReasoningSignatureCached(rawSignature); err != nil {
			return err.Error()
		}
		return ""
	case gjson.Null:
		return "encrypted_content is null"
	default:
		return fmt.Sprintf("encrypted_content must be a string, got %s", encryptedContent.Type.String())
	}
}

// sanitizeAndRewriteCodexInput performs, over the Codex "input" array, BOTH the
// system->developer role rewrite AND the invalid reasoning encrypted_content drop.
// It returns the possibly-modified input array and whether anything changed,
// returning the original slice unchanged (zero-alloc) when neither transform fires.
// This folds the translator's role-rewrite walk and the executor's sanitize walk
// into one on the fast path (the slow/legacy path keeps them separate).
//
// It is JSON-equivalent to applying codexresponses.RewriteCodexInputItemSystemRole
// per item and then sanitizeReasoningEncryptedContentInput (the two transforms touch
// disjoint fields — role vs encrypted_content — so they commute).
func sanitizeAndRewriteCodexInput(ctx context.Context, provider string, inputRaw []byte) ([]byte, bool) {
	input := gjson.ParseBytes(inputRaw)
	if !input.IsArray() {
		return inputRaw, false
	}
	provider = strings.TrimSpace(provider)
	if provider == "" {
		provider = "openai responses upstream"
	}

	// Detection pass (streaming, no per-item allocation): does anything change? The
	// common case (no system role, all signatures valid) exits here with no rebuild.
	anyChange := false
	input.ForEach(func(_, item gjson.Result) bool {
		if item.Get("role").String() == "system" || codexReasoningEncryptedContentDropReason(item) != "" {
			anyChange = true
			return false
		}
		return true
	})
	if !anyChange {
		return inputRaw, false
	}

	// Rebuild pass: emit each item, applying both transforms. Re-derives the per-item
	// decisions (signature validity is now cache-warm from the detection pass).
	var out bytes.Buffer
	out.Grow(len(inputRaw))
	out.WriteByte('[')
	index := -1
	input.ForEach(func(_, item gjson.Result) bool {
		index++
		if index > 0 {
			out.WriteByte(',')
		}
		itemRaw := []byte(item.Raw)
		if rewritten, ok := codexresponses.RewriteCodexInputItemSystemRole(itemRaw); ok {
			itemRaw = rewritten
		}
		if reason := codexReasoningEncryptedContentDropReason(item); reason != "" {
			if next, err := sjson.DeleteBytes(itemRaw, "encrypted_content"); err == nil {
				itemRaw = next
				itemID := strings.TrimSpace(item.Get("id").String())
				if itemID == "" {
					itemID = fmt.Sprintf("input[%d]", index)
				}
				helps.LogWithRequestID(ctx).Debugf("%s: dropped invalid reasoning encrypted_content at input[%d] item_id=%q reason=%s", provider, index, itemID, reason)
			} else {
				helps.LogWithRequestID(ctx).Debugf("%s: failed to drop invalid reasoning encrypted_content at input[%d]: %v", provider, index, err)
			}
		}
		out.Write(itemRaw)
		return true
	})
	out.WriteByte(']')
	return out.Bytes(), true
}
