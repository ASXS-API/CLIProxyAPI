package config

import "time"

const (
	defaultUpstreamResponseHeaderTimeoutInitial = 10 * time.Second
	defaultUpstreamResponseHeaderTimeoutMax     = 80 * time.Second
)

// UpstreamResponseHeaderTimeoutConfig controls the post-upload wait for upstream
// response headers. The timer starts only after the request, including its body,
// has been written to the transport.
type UpstreamResponseHeaderTimeoutConfig struct {
	Enabled bool                 `yaml:"enabled" json:"enabled"`
	Initial RetryIntervalSeconds `yaml:"initial" json:"initial"`
	Max     RetryIntervalSeconds `yaml:"max" json:"max"`
}

// Effective returns the runtime settings with conservative defaults filled in.
func (c UpstreamResponseHeaderTimeoutConfig) Effective() (bool, time.Duration, time.Duration) {
	if !c.Enabled {
		return false, 0, 0
	}
	initial := c.Initial.Duration()
	if initial <= 0 {
		initial = defaultUpstreamResponseHeaderTimeoutInitial
	}
	maximum := c.Max.Duration()
	if maximum <= 0 {
		maximum = defaultUpstreamResponseHeaderTimeoutMax
	}
	if maximum < initial {
		maximum = initial
	}
	return true, initial, maximum
}

// SanitizeUpstreamResponseHeaderTimeout normalizes negative or inverted values.
func (cfg *Config) SanitizeUpstreamResponseHeaderTimeout() {
	if cfg == nil {
		return
	}
	if cfg.UpstreamResponseHeaderTimeout.Initial < 0 {
		cfg.UpstreamResponseHeaderTimeout.Initial = 0
	}
	if cfg.UpstreamResponseHeaderTimeout.Max < 0 {
		cfg.UpstreamResponseHeaderTimeout.Max = 0
	}
	if cfg.UpstreamResponseHeaderTimeout.Initial > 0 &&
		cfg.UpstreamResponseHeaderTimeout.Max > 0 &&
		cfg.UpstreamResponseHeaderTimeout.Max < cfg.UpstreamResponseHeaderTimeout.Initial {
		cfg.UpstreamResponseHeaderTimeout.Max = cfg.UpstreamResponseHeaderTimeout.Initial
	}
}
