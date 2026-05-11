package config

import (
	"encoding/json"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestRetryIntervalSecondsYAML(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want time.Duration
	}{
		{name: "legacy seconds", raw: "max-retry-interval: 1\n", want: time.Second},
		{name: "fractional seconds", raw: "max-retry-interval: 0.3\n", want: 300 * time.Millisecond},
		{name: "duration string", raw: "max-retry-interval: 300ms\n", want: 300 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cfg Config
			if err := yaml.Unmarshal([]byte(tt.raw), &cfg); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got := cfg.MaxRetryInterval.Duration(); got != tt.want {
				t.Fatalf("duration = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRetryIntervalSecondsJSON(t *testing.T) {
	var v RetryIntervalSeconds
	if err := json.Unmarshal([]byte(`"300ms"`), &v); err != nil {
		t.Fatalf("unmarshal string: %v", err)
	}
	if got := v.Duration(); got != 300*time.Millisecond {
		t.Fatalf("duration = %v, want %v", got, 300*time.Millisecond)
	}

	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(data) != "0.3" {
		t.Fatalf("json = %s, want 0.3", data)
	}
}
