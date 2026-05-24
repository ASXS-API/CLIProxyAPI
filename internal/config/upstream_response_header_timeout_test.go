package config

import (
	"testing"
	"time"
)

func TestUpstreamResponseHeaderTimeoutEffectiveDefaults(t *testing.T) {
	cfg := UpstreamResponseHeaderTimeoutConfig{Enabled: true}

	enabled, initial, maximum := cfg.Effective()
	if !enabled {
		t.Fatal("enabled = false, want true")
	}
	if initial != 10*time.Second {
		t.Fatalf("initial = %s, want 10s", initial)
	}
	if maximum != 80*time.Second {
		t.Fatalf("max = %s, want 80s", maximum)
	}
}

func TestUpstreamResponseHeaderTimeoutEffectiveDisabled(t *testing.T) {
	cfg := UpstreamResponseHeaderTimeoutConfig{
		Enabled: false,
		Initial: RetryIntervalSeconds(10),
		Max:     RetryIntervalSeconds(80),
	}

	enabled, initial, maximum := cfg.Effective()
	if enabled || initial != 0 || maximum != 0 {
		t.Fatalf("Effective() = (%t, %s, %s), want disabled zeros", enabled, initial, maximum)
	}
}

func TestSanitizeUpstreamResponseHeaderTimeoutRaisesMax(t *testing.T) {
	cfg := &Config{
		UpstreamResponseHeaderTimeout: UpstreamResponseHeaderTimeoutConfig{
			Enabled: true,
			Initial: RetryIntervalSeconds(40),
			Max:     RetryIntervalSeconds(10),
		},
	}

	cfg.SanitizeUpstreamResponseHeaderTimeout()
	if cfg.UpstreamResponseHeaderTimeout.Max != cfg.UpstreamResponseHeaderTimeout.Initial {
		t.Fatalf("max = %v, want %v", cfg.UpstreamResponseHeaderTimeout.Max, cfg.UpstreamResponseHeaderTimeout.Initial)
	}
}
