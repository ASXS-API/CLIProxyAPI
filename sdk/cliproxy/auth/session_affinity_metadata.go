package auth

import cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"

const sessionAffinityTriedAuthIDsMetadataKey = "session_affinity_tried_auth_ids"
const sessionAffinityLocalAuthIDsMetadataKey = "session_affinity_local_auth_ids"

func withSessionAffinityTriedAuthIDs(opts cliproxyexecutor.Options, tried map[string]struct{}) cliproxyexecutor.Options {
	if len(tried) == 0 {
		return opts
	}
	meta := make(map[string]any, len(opts.Metadata)+1)
	for key, value := range opts.Metadata {
		meta[key] = value
	}
	triedCopy := make(map[string]struct{}, len(tried))
	for authID := range tried {
		triedCopy[authID] = struct{}{}
	}
	meta[sessionAffinityTriedAuthIDsMetadataKey] = triedCopy
	opts.Metadata = meta
	return opts
}

func sessionAffinityTriedAuthID(meta map[string]any, authID string) bool {
	if len(meta) == 0 || authID == "" {
		return false
	}
	raw := meta[sessionAffinityTriedAuthIDsMetadataKey]
	switch tried := raw.(type) {
	case map[string]struct{}:
		_, ok := tried[authID]
		return ok
	case map[string]bool:
		return tried[authID]
	default:
		return false
	}
}

func withSessionAffinityLocalAuthIDs(opts cliproxyexecutor.Options, local map[string]struct{}) cliproxyexecutor.Options {
	if len(local) == 0 {
		return opts
	}
	meta := make(map[string]any, len(opts.Metadata)+1)
	for key, value := range opts.Metadata {
		meta[key] = value
	}
	localCopy := make(map[string]struct{}, len(local))
	for authID := range local {
		localCopy[authID] = struct{}{}
	}
	meta[sessionAffinityLocalAuthIDsMetadataKey] = localCopy
	opts.Metadata = meta
	return opts
}

func sessionAffinityLocalAuthID(meta map[string]any, authID string) bool {
	if len(meta) == 0 || authID == "" {
		return false
	}
	raw := meta[sessionAffinityLocalAuthIDsMetadataKey]
	switch local := raw.(type) {
	case map[string]struct{}:
		_, ok := local[authID]
		return ok
	case map[string]bool:
		return local[authID]
	default:
		return false
	}
}
