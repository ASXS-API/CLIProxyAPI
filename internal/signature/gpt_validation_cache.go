package signature

import (
	"hash/maphash"
	"sync"
)

// The Codex / OpenAI-Responses protocol replays the full conversation history on
// every turn, so the SAME (large) reasoning encrypted_content blobs are
// shape-validated on every request — naively O(turns^2) over a conversation.
// InspectGPTReasoningSignature is a PURE, deterministic function of its input
// string (no time/config/key/IO), so its verdict is immutable and memoizable.
//
// We cache only KNOWN-VALID signatures, keyed by a fast 64-bit maphash of the
// signature string. Consequences:
//   - A hash collision can at worst make a structurally-INVALID blob be treated as
//     valid and forwarded upstream, which the upstream then rejects — a benign,
//     self-correcting outcome. It can NEVER cause a valid blob to be dropped
//     (cache hits only ever mean "valid").
//   - Validity never changes for a given string, so entries never need
//     invalidation; the size cap exists purely to bound memory.
//
// maphash uses the runtime's hardware-accelerated hash, which is far cheaper than
// the per-rune base64url scan + base64 decode that InspectGPTReasoningSignature
// performs over a multi-KB blob, so a cache hit is a large net win.

const validReasoningSignatureCacheMax = 65536

var (
	validReasoningSignatureSeed  = maphash.MakeSeed()
	validReasoningSignatureMu    sync.RWMutex
	validReasoningSignatureCache = make(map[uint64]struct{}, 1024)
)

// InspectGPTReasoningSignatureCached behaves exactly like
// InspectGPTReasoningSignature with respect to the returned error: it returns nil
// iff the signature is structurally valid, and the identical error otherwise. Valid
// signatures are memoized so repeated validation of resent conversation history is
// skipped. The *GPTReasoningSignatureInfo is intentionally not surfaced (callers in
// the sanitize hot path only need the validity verdict); use
// InspectGPTReasoningSignature directly if you need the info.
func InspectGPTReasoningSignatureCached(rawSignature string) error {
	h := maphash.String(validReasoningSignatureSeed, rawSignature)

	validReasoningSignatureMu.RLock()
	_, ok := validReasoningSignatureCache[h]
	validReasoningSignatureMu.RUnlock()
	if ok {
		return nil
	}

	if _, err := InspectGPTReasoningSignature(rawSignature); err != nil {
		return err
	}

	validReasoningSignatureMu.Lock()
	if len(validReasoningSignatureCache) >= validReasoningSignatureCacheMax {
		// Coarse bound: drop all. Verdicts are immutable, so re-filling just re-runs
		// a pure function; this is far simpler than LRU and correctness-neutral.
		validReasoningSignatureCache = make(map[uint64]struct{}, 1024)
	}
	validReasoningSignatureCache[h] = struct{}{}
	validReasoningSignatureMu.Unlock()
	return nil
}
