package auth

import (
	"testing"
	"time"
)

func TestFatalCredentialErrorReason(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		err        *Error
		wantReason string
	}{
		{
			name:       "nil error",
			err:        nil,
			wantReason: "",
		},
		{
			name:       "biscuit baker inactive owner status",
			err:        &Error{Message: `{ "error": { "message": "Personal access token owner is inactive.", "type": null, "code": "biscuit_baker_service_auth_credential_error_status", "param": null }, "status": 403 }`},
			wantReason: "personal access token owner is inactive",
		},
		{
			name:       "personal access token owner inactive message only",
			err:        &Error{Message: `Personal access token owner is inactive.`},
			wantReason: "personal access token owner is inactive",
		},
		{
			name:       "codex unauthorized normalized body",
			err:        &Error{Message: `{"error":{"message":"Unauthorized","type":"authentication_error","code":"auth_unavailable"}}`},
			wantReason: "unauthorized",
		},
		{
			name:       "refreshable expired token is not fatal",
			err:        &Error{Message: `{"error":{"message":"invalid or expired token","type":"authentication_error","code":"auth_unavailable"}}`},
			wantReason: "",
		},
		{
			name:       "generic quota error is not fatal",
			err:        &Error{Message: `{"error":{"message":"rate limit","type":"rate_limit_error","code":"rate_limited"}}`},
			wantReason: "",
		},
		{
			name:       "self-serve business usage-based limit is fatal",
			err:        &Error{Message: `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"self_serve_business_usage_based","resets_at":1784390329,"eligible_promo":null,"resets_in_seconds":2429922}}`},
			wantReason: "usage limit reached (self-serve business usage-based plan)",
		},
		{
			name:       "usage_limit_reached without business plan stays on cooldown",
			err:        &Error{Message: `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"claude_pro","resets_in_seconds":3600}}`},
			wantReason: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := fatalCredentialErrorReason(tc.err); got != tc.wantReason {
				t.Fatalf("fatalCredentialErrorReason = %q, want %q", got, tc.wantReason)
			}
		})
	}
}

func TestFatalCredentialErrorReason_RuleToggles(t *testing.T) {
	// Not parallel: mutates global rule toggles.
	inactiveOwnerErr := &Error{Message: `Personal access token owner is inactive.`}
	unauthorizedErr := &Error{Message: `{"error":{"message":"Unauthorized","type":"authentication_error","code":"auth_unavailable"}}`}
	usageLimitErr := &Error{Message: `{"error":{"type":"usage_limit_reached","plan_type":"self_serve_business_usage_based"}}`}

	// Restore defaults after the test.
	t.Cleanup(func() { SetFatalCredentialRulesEnabled(true, true, true) })

	// All disabled: every fatal signature falls through to the cooldown path.
	SetFatalCredentialRulesEnabled(false, false, false)
	for _, err := range []*Error{inactiveOwnerErr, unauthorizedErr, usageLimitErr} {
		if got := fatalCredentialErrorReason(err); got != "" {
			t.Fatalf("rules disabled: fatalCredentialErrorReason = %q, want empty", got)
		}
	}

	// Selectively enable only the unauthorized rule.
	SetFatalCredentialRulesEnabled(false, true, false)
	if got := fatalCredentialErrorReason(inactiveOwnerErr); got != "" {
		t.Fatalf("inactive-owner disabled: got %q, want empty", got)
	}
	if got := fatalCredentialErrorReason(unauthorizedErr); got != "unauthorized" {
		t.Fatalf("unauthorized enabled: got %q, want %q", got, "unauthorized")
	}
	if got := fatalCredentialErrorReason(usageLimitErr); got != "" {
		t.Fatalf("usage-limit disabled: got %q, want empty", got)
	}
}

func TestDisableAuthForFatalError(t *testing.T) {
	t.Parallel()

	now := time.Now()
	resultErr := &Error{Message: `{"error":{"message":"Unauthorized","type":"authentication_error","code":"auth_unavailable"}}`}
	auth := &Auth{
		ID:             "a",
		Provider:       "codex",
		Status:         StatusActive,
		NextRetryAfter: now.Add(30 * time.Minute),
	}

	disableAuthForFatalError(auth, resultErr, fatalCredentialErrorReason(resultErr), now)

	if !auth.Disabled {
		t.Fatalf("auth.Disabled = false, want true")
	}
	if auth.Status != StatusDisabled {
		t.Fatalf("auth.Status = %q, want %q", auth.Status, StatusDisabled)
	}
	if !auth.Unavailable {
		t.Fatalf("auth.Unavailable = false, want true")
	}
	if auth.StatusMessage != "unauthorized" {
		t.Fatalf("auth.StatusMessage = %q, want %q", auth.StatusMessage, "unauthorized")
	}
	if !auth.NextRetryAfter.IsZero() {
		t.Fatalf("auth.NextRetryAfter = %v, want zero (disable, not cooldown)", auth.NextRetryAfter)
	}
	if auth.LastError == nil {
		t.Fatalf("auth.LastError = nil, want cloned error")
	}
}
