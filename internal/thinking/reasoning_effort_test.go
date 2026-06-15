package thinking

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/jsonx"
)

func TestExtractReasoningEffortUsesSuffixOverBody(t *testing.T) {
	got := ExtractReasoningEffort([]byte(`{"reasoning_effort":"low"}`), "openai", "gpt-5.4(high)")
	if got != "high" {
		t.Fatalf("ExtractReasoningEffort() = %q, want %q", got, "high")
	}
}

func TestExtractReasoningEffortConvertsBudgetToLevel(t *testing.T) {
	got := ExtractReasoningEffort([]byte(`{"thinking":{"type":"enabled","budget_tokens":8192}}`), "claude", "claude-sonnet-4-5")
	if got != "medium" {
		t.Fatalf("ExtractReasoningEffort() = %q, want %q", got, "medium")
	}
}

func TestExtractReasoningEffortSupportsOpenAIResponses(t *testing.T) {
	got := ExtractReasoningEffort([]byte(`{"reasoning":{"effort":"medium"}}`), "openai-response", "gpt-5.4")
	if got != "medium" {
		t.Fatalf("ExtractReasoningEffort() = %q, want %q", got, "medium")
	}
}

func TestExtractReasoningEffortMissingConfigIsEmpty(t *testing.T) {
	got := ExtractReasoningEffort([]byte(`{"messages":[{"role":"user","content":"hi"}]}`), "openai", "gpt-5.4")
	if got != "" {
		t.Fatalf("ExtractReasoningEffort() = %q, want empty", got)
	}
}

func TestExtractCodexConfigBothEngines(t *testing.T) {
	type wantConfig struct {
		mode  ThinkingMode
		level ThinkingLevel
		zero  bool // true when we expect ThinkingConfig{}
	}
	cases := []struct {
		name string
		body string
		want wantConfig
	}{
		{"effort none returns ModeNone", `{"reasoning":{"effort":"none"}}`, wantConfig{mode: ModeNone}},
		{"effort low returns ModeLevel low", `{"reasoning":{"effort":"low"}}`, wantConfig{mode: ModeLevel, level: "low"}},
		{"effort medium returns ModeLevel medium", `{"reasoning":{"effort":"medium"}}`, wantConfig{mode: ModeLevel, level: "medium"}},
		{"effort high returns ModeLevel high", `{"reasoning":{"effort":"high"}}`, wantConfig{mode: ModeLevel, level: "high"}},
		{"absent reasoning returns zero config", `{"model":"gpt-5.4"}`, wantConfig{zero: true}},
		{"empty effort returns zero config", `{"reasoning":{"effort":""}}`, wantConfig{zero: true}},
	}
	for _, engine := range []string{"", "all"} {
		label := "std"
		if engine == "all" {
			label = "sonic"
		}
		jsonx.Configure(engine)
		for _, tc := range cases {
			t.Run(label+"/"+tc.name, func(t *testing.T) {
				got := extractCodexConfig([]byte(tc.body))
				if tc.want.zero {
					if got != (ThinkingConfig{}) {
						t.Fatalf("[%s] extractCodexConfig(%s) = %+v, want zero ThinkingConfig", label, tc.body, got)
					}
					return
				}
				if got.Mode != tc.want.mode {
					t.Fatalf("[%s] extractCodexConfig(%s).Mode = %v, want %v", label, tc.body, got.Mode, tc.want.mode)
				}
				if tc.want.mode == ModeLevel && got.Level != tc.want.level {
					t.Fatalf("[%s] extractCodexConfig(%s).Level = %q, want %q", label, tc.body, got.Level, tc.want.level)
				}
			})
		}
	}
	jsonx.Configure("")
}
