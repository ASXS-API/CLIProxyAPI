package helps

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestEffectiveProxyURL_ExplicitAuthWins(t *testing.T) {
	cfg := &config.Config{}
	cfg.CredentialEgressProxy = "socks5://:secret@172.18.0.1:1080"
	a := &cliproxyauth.Auth{ID: "acc-1", ProxyURL: "socks5://user:pw@example.com:1080"}
	if got := EffectiveProxyURL(cfg, a); got != "socks5://user:pw@example.com:1080" {
		t.Fatalf("explicit auth proxy should win, got %q", got)
	}
}

func TestEffectiveProxyURL_CredentialEgressInjectsIndex(t *testing.T) {
	cfg := &config.Config{}
	cfg.CredentialEgressProxy = "socks5://:s3cr3t@172.18.0.1:1080"
	a := &cliproxyauth.Auth{ID: "acc-1"}
	idx := a.EnsureIndex()
	if idx == "" {
		t.Fatal("expected non-empty index")
	}
	got := EffectiveProxyURL(cfg, a)
	want := "socks5://" + idx + ":s3cr3t@172.18.0.1:1080"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestEffectiveProxyURL_StableAndDistinct(t *testing.T) {
	cfg := &config.Config{}
	cfg.CredentialEgressProxy = "socks5://:s@172.18.0.1:1080"
	a := &cliproxyauth.Auth{ID: "acc-42"}
	if EffectiveProxyURL(cfg, a) != EffectiveProxyURL(cfg, a) {
		t.Fatal("expected stable url across calls for same credential")
	}
	u1 := EffectiveProxyURL(cfg, &cliproxyauth.Auth{ID: "acc-1"})
	u2 := EffectiveProxyURL(cfg, &cliproxyauth.Auth{ID: "acc-2"})
	if u1 == u2 {
		t.Fatalf("expected distinct urls per credential, both %q", u1)
	}
}

func TestEffectiveProxyURL_FallbackGlobalWhenNoEgress(t *testing.T) {
	cfg := &config.Config{}
	cfg.ProxyURL = "socks5://global:pw@10.0.0.1:1080"
	a := &cliproxyauth.Auth{ID: "acc-1"}
	if got := EffectiveProxyURL(cfg, a); got != "socks5://global:pw@10.0.0.1:1080" {
		t.Fatalf("expected global fallback, got %q", got)
	}
}

func TestEffectiveProxyURL_NoConfigReturnsEmpty(t *testing.T) {
	a := &cliproxyauth.Auth{ID: "acc-1"}
	if got := EffectiveProxyURL(&config.Config{}, a); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}
