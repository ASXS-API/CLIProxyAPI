package signature

import (
	"encoding/base64"
	"fmt"
	"math/rand"
	"testing"
)

// validReasoningSig builds a structurally-valid GPT reasoning signature whose
// decoded length is 57+16*k bytes (version 0x80), base64url-encoded.
func validReasoningSig(k int) string {
	decoded := make([]byte, 57+16*k)
	decoded[0] = 0x80
	return base64.RawURLEncoding.EncodeToString(decoded)
}

func TestInspectGPTReasoningSignatureCachedEquivalence(t *testing.T) {
	cases := []string{
		validReasoningSig(1),   // 73 bytes
		validReasoningSig(2),   // 89
		validReasoningSig(10),  // larger
		validReasoningSig(600), // ~10KB blob
		"",                     // empty -> invalid
		"notasig",              // no gAAAA prefix
		"gAAAA",                // prefix but decodes too short
		"gAAAA!!!",             // invalid base64url char
		"gAAAA====",            // bad
		"gBBBBshortbase64url",  // wrong version after decode / short
	}
	// a couple of random valid blobs
	rng := rand.New(rand.NewSource(0x515))
	for i := 0; i < 5; i++ {
		cases = append(cases, validReasoningSig(1+rng.Intn(50)))
	}

	for _, sig := range cases {
		// run each twice to exercise the cold (miss) and warm (hit) paths
		for pass := 0; pass < 3; pass++ {
			wantErr := !IsValidGPTReasoningSignature(sig)
			gotErr := InspectGPTReasoningSignatureCached(sig)
			if (gotErr != nil) != wantErr {
				t.Fatalf("pass %d sig=%.20q: cached err=%v, want invalid=%v", pass, sig, gotErr, wantErr)
			}
			if gotErr != nil {
				_, refErr := InspectGPTReasoningSignature(sig)
				if refErr == nil || gotErr.Error() != refErr.Error() {
					t.Fatalf("sig=%.20q: cached error %q != reference %v", sig, gotErr.Error(), refErr)
				}
			}
		}
	}
}

func TestInspectGPTReasoningSignatureCachedSizeBound(t *testing.T) {
	// Fill past the cap with distinct valid signatures; must stay correct and bounded.
	for i := 0; i < validReasoningSignatureCacheMax+2000; i++ {
		decoded := make([]byte, 73)
		decoded[0] = 0x80
		// vary TAIL bytes to make distinct valid signatures without disturbing the
		// leading 0x80/gAAAA prefix region.
		decoded[72] = byte(i)
		decoded[71] = byte(i >> 8)
		decoded[70] = byte(i >> 16)
		sig := base64.RawURLEncoding.EncodeToString(decoded)
		if err := InspectGPTReasoningSignatureCached(sig); err != nil {
			t.Fatalf("valid sig wrongly rejected at i=%d: %v", i, err)
		}
	}
	validReasoningSignatureMu.RLock()
	n := len(validReasoningSignatureCache)
	validReasoningSignatureMu.RUnlock()
	if n > validReasoningSignatureCacheMax {
		t.Fatalf("cache exceeded cap: %d > %d", n, validReasoningSignatureCacheMax)
	}
}

func BenchmarkGPTReasoningSignature(b *testing.B) {
	sig := validReasoningSig(600) // ~10KB realistic blob
	b.Run("uncached", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_, _ = InspectGPTReasoningSignature(sig)
		}
	})
	b.Run("cached_warm", func(b *testing.B) {
		_ = InspectGPTReasoningSignatureCached(sig) // warm
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = InspectGPTReasoningSignatureCached(sig)
		}
	})
	_ = fmt.Sprint
}
