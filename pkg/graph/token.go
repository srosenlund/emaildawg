// Package graph provides a Microsoft Graph API client-credentials token
// provider for the emaildawg bridge. It acquires app-only access tokens
// using the OAuth 2.0 client-credentials flow and caches them with a 60s
// early-refresh window.
package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// fetchFunc is the type of the injectable HTTP-fetch function used by TokenProvider.
// In production the real httpFetch is wired in; in tests a stub is injected.
type fetchFunc func(ctx context.Context, tenantID, clientID, clientSecret, scope string) (accessToken string, expiresIn int, err error)

// TokenProvider acquires and caches an app-only (client-credentials) access
// token for Microsoft Graph. One provider can be shared across goroutines;
// all state is protected by a mutex.
type TokenProvider struct {
	tenantID     string
	clientID     string
	clientSecret string
	scope        string

	mu      sync.Mutex
	token   string
	expiry  time.Time
	httpc   *http.Client
	fetchFn fetchFunc // injectable for testing; nil means use httpFetch
}

// NewTokenProvider returns a TokenProvider for the given Entra application
// credentials. The token scope is fixed to "https://graph.microsoft.com/.default".
func NewTokenProvider(tenantID, clientID, clientSecret string) *TokenProvider {
	return &TokenProvider{
		tenantID:     tenantID,
		clientID:     clientID,
		clientSecret: clientSecret,
		scope:        "https://graph.microsoft.com/.default",
		httpc:        &http.Client{Timeout: 30 * time.Second},
	}
}

// Token returns a valid access token, refreshing it when it is within 60 seconds
// of expiry. Safe for concurrent use.
func (p *TokenProvider) Token(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.token != "" && time.Now().Before(p.expiry.Add(-60*time.Second)) {
		return p.token, nil
	}

	fetch := p.fetchFn
	if fetch == nil {
		fetch = p.httpFetch
	}

	accessToken, expiresIn, err := fetch(ctx, p.tenantID, p.clientID, p.clientSecret, p.scope)
	if err != nil {
		return "", err
	}

	p.token = accessToken
	p.expiry = time.Now().Add(time.Duration(expiresIn) * time.Second)
	return p.token, nil
}

// httpFetch performs the real client-credentials POST to the Microsoft identity
// platform token endpoint and returns the access token and its lifetime in seconds.
func (p *TokenProvider) httpFetch(ctx context.Context, tenantID, clientID, clientSecret, scope string) (string, int, error) {
	endpoint := fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", tenantID)
	form := url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"scope":         {scope},
		"grant_type":    {"client_credentials"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := p.httpc.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()

	var body struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", 0, fmt.Errorf("decode token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK || body.AccessToken == "" {
		return "", 0, fmt.Errorf("token endpoint returned %d: %s (%s)", resp.StatusCode, body.Error, body.ErrorDesc)
	}

	return body.AccessToken, body.ExpiresIn, nil
}
