// Package jsonx is a runtime-switchable JSON façade. Each operation runs on
// either the legacy std stack (gjson / sjson / encoding-json) or bytedance/sonic,
// selected PER COMPONENT via the JSONX_SONIC toggle.
//
// This lets the sonic migration ship behind a flag and be A/B-tested in
// production: the test container (cli-proxy-api-8319_fb) sets JSONX_SONIC=all
// (full sonic) while the prod container (8319) leaves it unset (std). The end
// state (after the migration is validated) removes the std branch entirely.
package jsonx

import (
	"os"
	"strings"
	"sync/atomic"
)

var (
	// sonicAll is set when JSONX_SONIC=all — every component uses sonic.
	sonicAll atomic.Bool
	// sonicComps holds the explicit per-component allowlist (when not "all").
	sonicComps atomic.Pointer[map[string]struct{}]
)

func init() { Configure(os.Getenv("JSONX_SONIC")) }

// Configure selects which components use the sonic engine. spec is:
//   - ""      → all components use the std stack (default; prod-safe)
//   - "all"   → every component uses sonic
//   - "a,b,c" → only the listed component keys use sonic
//
// Safe to call once at startup (e.g. from config). Reads are lock-free.
func Configure(spec string) {
	spec = strings.TrimSpace(spec)
	sonicAll.Store(false)
	sonicComps.Store(nil)
	if spec == "" {
		return
	}
	if strings.EqualFold(spec, "all") {
		sonicAll.Store(true)
		return
	}
	m := make(map[string]struct{})
	for _, c := range strings.Split(spec, ",") {
		if c = strings.TrimSpace(c); c != "" {
			m[c] = struct{}{}
		}
	}
	sonicComps.Store(&m)
}

// UseSonic reports whether the given component should use the sonic engine.
// component is a stable dotted key, e.g. "codex.req.build", "auth.read".
func UseSonic(component string) bool {
	if sonicAll.Load() {
		return true
	}
	if m := sonicComps.Load(); m != nil {
		_, ok := (*m)[component]
		return ok
	}
	return false
}
