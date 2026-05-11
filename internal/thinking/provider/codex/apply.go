// Package codex implements thinking configuration for Codex (OpenAI Responses API) models.
//
// Codex models use the reasoning.effort format with discrete levels
// (low/medium/high). This is similar to OpenAI but uses nested field
// "reasoning.effort" instead of "reasoning_effort".
// See: _bmad-output/planning-artifacts/architecture.md#Epic-8
package codex

import (
	"bytes"
	"encoding/json"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/tidwall/gjson"
)

// Applier implements thinking.ProviderApplier for Codex models.
//
// Codex-specific behavior:
//   - Output format: reasoning.effort (string: low/medium/high/xhigh)
//   - Level-only mode: no numeric budget support
//   - Some models support ZeroAllowed (gpt-5.1, gpt-5.2)
type Applier struct{}

var _ thinking.ProviderApplier = (*Applier)(nil)

// NewApplier creates a new Codex thinking applier.
func NewApplier() *Applier {
	return &Applier{}
}

func init() {
	thinking.RegisterProvider("codex", NewApplier())
}

// Apply applies thinking configuration to Codex request body.
//
// Expected output format:
//
//	{
//	  "reasoning": {
//	    "effort": "high"
//	  }
//	}
func (a *Applier) Apply(body []byte, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) ([]byte, error) {
	if thinking.IsUserDefinedModel(modelInfo) {
		return applyCompatibleCodex(body, config)
	}
	if modelInfo.Thinking == nil {
		return body, nil
	}

	// Only handle ModeLevel and ModeNone; other modes pass through unchanged.
	if config.Mode != thinking.ModeLevel && config.Mode != thinking.ModeNone {
		return body, nil
	}

	if len(body) == 0 || !gjson.ValidBytes(body) {
		body = []byte(`{}`)
	}

	if config.Mode == thinking.ModeLevel {
		return setCodexReasoningEffort(body, string(config.Level)), nil
	}

	effort := ""
	support := modelInfo.Thinking
	if config.Budget == 0 {
		if support.ZeroAllowed || thinking.HasLevel(support.Levels, string(thinking.LevelNone)) {
			effort = string(thinking.LevelNone)
		}
	}
	if effort == "" && config.Level != "" {
		effort = string(config.Level)
	}
	if effort == "" && len(support.Levels) > 0 {
		effort = support.Levels[0]
	}
	if effort == "" {
		return body, nil
	}

	return setCodexReasoningEffort(body, effort), nil
}

func applyCompatibleCodex(body []byte, config thinking.ThinkingConfig) ([]byte, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		body = []byte(`{}`)
	}

	var effort string
	switch config.Mode {
	case thinking.ModeLevel:
		if config.Level == "" {
			return body, nil
		}
		effort = string(config.Level)
	case thinking.ModeNone:
		effort = string(thinking.LevelNone)
		if config.Level != "" {
			effort = string(config.Level)
		}
	case thinking.ModeAuto:
		// Auto mode for user-defined models: pass through as "auto"
		effort = string(thinking.LevelAuto)
	case thinking.ModeBudget:
		// Budget mode: convert budget to level using threshold mapping
		level, ok := thinking.ConvertBudgetToLevel(config.Budget)
		if !ok {
			return body, nil
		}
		effort = level
	default:
		return body, nil
	}

	return setCodexReasoningEffort(body, effort), nil
}

func setCodexReasoningEffort(body []byte, effort string) []byte {
	if existing := gjson.GetBytes(body, "reasoning.effort"); existing.Exists() && existing.Type == gjson.String && existing.String() == effort {
		return body
	}

	var obj map[string]json.RawMessage
	if len(bytes.TrimSpace(body)) == 0 {
		obj = make(map[string]json.RawMessage)
	} else if err := json.Unmarshal(body, &obj); err != nil || obj == nil {
		return body
	}

	reasoning := make(map[string]json.RawMessage)
	if rawReasoning, ok := obj["reasoning"]; ok && len(bytes.TrimSpace(rawReasoning)) > 0 && !bytes.Equal(bytes.TrimSpace(rawReasoning), []byte("null")) {
		_ = json.Unmarshal(rawReasoning, &reasoning)
		if reasoning == nil {
			reasoning = make(map[string]json.RawMessage)
		}
	}
	rawEffort, err := marshalJSONNoEscape(effort)
	if err != nil {
		return body
	}
	reasoning["effort"] = rawEffort
	rawReasoning, err := marshalJSONNoEscape(reasoning)
	if err != nil {
		return body
	}
	obj["reasoning"] = rawReasoning
	out, err := marshalJSONNoEscape(obj)
	if err != nil {
		return body
	}
	return out
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
