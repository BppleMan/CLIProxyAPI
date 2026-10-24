package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// RefreshRPQuota makes one quota-only request through the credential's existing
// transport. Failure bodies are deliberately not logged or returned to callers.
func (e *AntigravityExecutor) RefreshRPQuota(ctx context.Context, auth *coreauth.Auth) (coreauth.RPQuotaSnapshot, error) {
	if auth == nil {
		return coreauth.RPQuotaSnapshot{}, errors.New("missing quota credential")
	}
	// Quota observation must not rotate credentials on an unpersisted clone.
	// The manager's existing OAuth refresh lifecycle owns token acquisition.
	expiry, _ := auth.ExpirationTime()
	if metaStringValue(auth.Metadata, "access_token") == "" || !expiry.After(time.Now().Add(antigravityRequestTokenSafetyWindow)) {
		return coreauth.RPQuotaSnapshot{}, errors.New("quota observation requires a current access token")
	}
	body, err := json.Marshal(map[string]string{"project": antigravityProjectIDFromAuth(auth)})
	if err != nil {
		return coreauth.RPQuotaSnapshot{}, err
	}
	endpoint := strings.TrimRight(resolveAntigravityRequestBaseURL(auth), "/") + "/v1internal:retrieveUserQuotaSummary"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return coreauth.RPQuotaSnapshot{}, errors.New("invalid quota request")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.HttpRequest(ctx, auth, req)
	if err != nil {
		return coreauth.RPQuotaSnapshot{}, errors.New("quota request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return coreauth.RPQuotaSnapshot{}, errors.New("quota request rejected")
	}
	const maxBytes = 1 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil || len(data) > maxBytes {
		return coreauth.RPQuotaSnapshot{}, errors.New("invalid quota response")
	}
	return helps.ParseAntigravityQuotaSummary(data, time.Now())
}
