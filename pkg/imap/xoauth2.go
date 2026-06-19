package imap

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

// XOAuth2Client implements the emersion/go-sasl Client interface for the
// XOAUTH2 mechanism used by Microsoft 365 and Gmail. go-sasl ships OAUTHBEARER
// (RFC 7628) but not the older non-standard XOAUTH2 that Office 365 IMAP
// requires, so we implement the tiny mechanism here.
type XOAuth2Client struct {
	Username string
	Token    string
}

// NewXOAuth2Client returns a SASL client that authenticates as username using
// the given OAuth2 bearer access token.
func NewXOAuth2Client(username, token string) *XOAuth2Client {
	return &XOAuth2Client{Username: username, Token: token}
}

// Start returns the XOAUTH2 mechanism name and the initial client response:
//   "user=<username>\x01auth=Bearer <token>\x01\x01"
func (c *XOAuth2Client) Start() (mech string, ir []byte, err error) {
	resp := fmt.Sprintf("user=%s\x01auth=Bearer %s\x01\x01", c.Username, c.Token)
	return "XOAUTH2", []byte(resp), nil
}

// Next handles the failure path: on a bad token the server sends a base64 JSON
// error challenge and expects an empty client response before returning the
// tagged NO. Returning an empty (non-nil) response lets the real error surface
// as the command result.
func (c *XOAuth2Client) Next(challenge []byte) ([]byte, error) {
	return []byte{}, nil
}

// XOAuth2TokenProvider acquires and caches an app-only (client-credentials)
// access token for the Office 365 IMAP resource. One provider is shared across
// all accounts because the app token grants access to every mailbox the Entra
// app is permitted to use; the per-mailbox identity is carried in the XOAUTH2
// auth string, not the token request.
type XOAuth2TokenProvider struct {
	tenantID     string
	clientID     string
	clientSecret string
	scope        string

	mu     sync.Mutex
	token  string
	expiry time.Time
	httpc  *http.Client
}

// NewXOAuth2TokenProvider builds a provider for the given Entra app credentials.
func NewXOAuth2TokenProvider(tenantID, clientID, clientSecret string) *XOAuth2TokenProvider {
	return &XOAuth2TokenProvider{
		tenantID:     tenantID,
		clientID:     clientID,
		clientSecret: clientSecret,
		scope:        "https://outlook.office365.com/.default",
		httpc:        &http.Client{Timeout: 30 * time.Second},
	}
}

// Token returns a valid access token, refreshing it when it is within 60s of
// expiry. Safe for concurrent use.
func (p *XOAuth2TokenProvider) Token(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.token != "" && time.Now().Before(p.expiry.Add(-60*time.Second)) {
		return p.token, nil
	}

	endpoint := fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", p.tenantID)
	form := url.Values{
		"client_id":     {p.clientID},
		"client_secret": {p.clientSecret},
		"scope":         {p.scope},
		"grant_type":    {"client_credentials"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := p.httpc.Do(req)
	if err != nil {
		return "", fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()

	var body struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK || body.AccessToken == "" {
		return "", fmt.Errorf("token endpoint returned %d: %s (%s)", resp.StatusCode, body.Error, body.ErrorDesc)
	}

	p.token = body.AccessToken
	p.expiry = time.Now().Add(time.Duration(body.ExpiresIn) * time.Second)
	return p.token, nil
}
