// Package codex provides authentication and token management for OpenAI's Codex API.
// This file implements support for v2 personal access tokens (PATs).
//
// Unlike the OAuth ChatGPT login flow, a personal access token is an opaque bearer
// token (prefixed with "at-") that carries no embedded account information and is not
// refreshed. The ChatGPT account id required for upstream requests must therefore be
// resolved online via the whoami endpoint before any request can be made.
package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// Personal access token configuration constants for OpenAI Codex.
const (
	// PersonalAccessTokenPrefix identifies v2 Codex personal access tokens.
	PersonalAccessTokenPrefix = "at-"
	// authAPIBaseURL is the default account API base used to resolve PAT metadata.
	authAPIBaseURL = "https://auth.openai.com/api/accounts"
	// authAPIBaseURLEnvVar allows overriding the account API base URL.
	authAPIBaseURLEnvVar = "CODEX_AUTHAPI_BASE_URL"
	// whoamiPath is the endpoint that returns account metadata for a PAT.
	whoamiPath = "/v1/user-auth-credential/whoami"
)

// IsPersonalAccessToken reports whether token looks like a v2 Codex personal access token.
func IsPersonalAccessToken(token string) bool {
	return strings.HasPrefix(strings.TrimSpace(token), PersonalAccessTokenPrefix)
}

// PersonalAccessTokenMetadata holds the account information returned by the whoami
// endpoint for a Codex personal access token. The JSON tags mirror the upstream
// response exactly.
type PersonalAccessTokenMetadata struct {
	Email                   string `json:"email"`
	ChatgptUserID           string `json:"chatgpt_user_id"`
	ChatgptAccountID        string `json:"chatgpt_account_id"`
	ChatgptPlanType         string `json:"chatgpt_plan_type"`
	ChatgptAccountIsFedramp bool   `json:"chatgpt_account_is_fedramp"`
}

// WhoamiPersonalAccessToken resolves the account metadata for a personal access token.
// Personal access tokens are opaque and carry no embedded account information, so the
// account id must be fetched online before upstream requests can be authenticated.
func (o *CodexAuth) WhoamiPersonalAccessToken(ctx context.Context, accessToken string) (*PersonalAccessTokenMetadata, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return nil, fmt.Errorf("personal access token is required")
	}

	base := strings.TrimRight(strings.TrimSpace(os.Getenv(authAPIBaseURLEnvVar)), "/")
	if base == "" {
		base = authAPIBaseURL
	}
	endpoint := base + whoamiPath

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create whoami request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := o.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("whoami request failed: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read whoami response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("whoami request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var meta PersonalAccessTokenMetadata
	if err = json.Unmarshal(body, &meta); err != nil {
		return nil, fmt.Errorf("failed to parse whoami response: %w", err)
	}
	if strings.TrimSpace(meta.ChatgptAccountID) == "" {
		return nil, fmt.Errorf("whoami response missing chatgpt_account_id")
	}
	return &meta, nil
}
