package graph

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const graphBaseURL = "https://graph.microsoft.com/v1.0"

// Client is a Microsoft Graph API client scoped to a single mailbox.
type Client struct {
	tp     *TokenProvider
	userID string
	httpc  *http.Client
}

// NewClient returns a Client that fetches tokens from tp and operates on the
// mailbox identified by userID (the UPN, e.g. "mail@example.com").
func NewClient(tp *TokenProvider, userID string) *Client {
	return &Client{
		tp:     tp,
		userID: userID,
		httpc:  &http.Client{Timeout: 30 * time.Second},
	}
}

// GetMessage fetches a single message by Graph message ID from the client's
// mailbox. It sends two Prefer headers:
//   - Prefer: IdType="ImmutableId"   (stable immutable message IDs)
//   - Prefer: outlook.body-content-type="text"  (plain-text body)
//
// Non-200 responses are returned as an error that includes the response body.
func (c *Client) GetMessage(ctx context.Context, id string) (*GraphMessage, error) {
	token, err := c.tp.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("graph GetMessage: acquire token: %w", err)
	}

	msgURL := fmt.Sprintf("%s/users/%s/messages/%s", graphBaseURL, c.userID, url.PathEscape(id))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, msgURL, nil)
	if err != nil {
		return nil, fmt.Errorf("graph GetMessage: build request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Add("Prefer", `IdType="ImmutableId"`)
	req.Header.Add("Prefer", `outlook.body-content-type="text"`)

	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("graph GetMessage: http: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("graph GetMessage: read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("graph GetMessage: status %d: %s", resp.StatusCode, body)
	}

	msg, err := parseGraphMessage(body)
	if err != nil {
		return nil, fmt.Errorf("graph GetMessage: parse: %w", err)
	}
	return msg, nil
}

// SetRead marks a message as read or unread via a PATCH request to the Graph
// API. It follows the same token-acquisition and header pattern as GetMessage.
// Non-200 responses are returned as an error that includes the response body.
func (c *Client) SetRead(ctx context.Context, msgID string, isRead bool) error {
	token, err := c.tp.Token(ctx)
	if err != nil {
		return fmt.Errorf("graph SetRead: acquire token: %w", err)
	}

	patchURL := fmt.Sprintf("%s/users/%s/messages/%s", graphBaseURL, c.userID, url.PathEscape(msgID))

	isReadStr := "false"
	if isRead {
		isReadStr = "true"
	}
	payload := []byte(`{"isRead":` + isReadStr + `}`)

	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, patchURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("graph SetRead: build request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Add("Prefer", `IdType="ImmutableId"`)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("graph SetRead: http: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("graph SetRead: read body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("graph SetRead: status %d: %s", resp.StatusCode, body)
	}

	return nil
}

// FindGraphIDByInternetID resolves an RFC-822 internetMessageId to the Graph
// internal (immutable) message id. It issues:
//
//	GET /users/{userID}/messages?$filter=internetMessageId eq '<internetID>'&$select=id&$top=1
//
// The whole OData query string is URL-encoded via url.Values so that special
// characters in the filter value are safely escaped. The Prefer: IdType="ImmutableId"
// header is sent so the returned id is the immutable form (survives folder moves).
//
// Returns ("", nil) when no match is found and (id, nil) on success.
// Returns ("", err) on HTTP or JSON parse failure.
func (c *Client) FindGraphIDByInternetID(ctx context.Context, internetID string) (string, error) {
	token, err := c.tp.Token(ctx)
	if err != nil {
		return "", fmt.Errorf("graph FindGraphIDByInternetID: acquire token: %w", err)
	}

	escaped := strings.ReplaceAll(internetID, "'", "''")
	q := url.Values{}
	q.Set("$filter", "internetMessageId eq '"+escaped+"'")
	q.Set("$select", "id")
	q.Set("$top", "1")
	filterURL := fmt.Sprintf("%s/users/%s/messages?%s", graphBaseURL, c.userID, q.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, filterURL, nil)
	if err != nil {
		return "", fmt.Errorf("graph FindGraphIDByInternetID: build request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Add("Prefer", `IdType="ImmutableId"`)

	resp, err := c.httpc.Do(req)
	if err != nil {
		return "", fmt.Errorf("graph FindGraphIDByInternetID: http: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("graph FindGraphIDByInternetID: read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("graph FindGraphIDByInternetID: status %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Value []struct {
			ID string `json:"id"`
		} `json:"value"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("graph FindGraphIDByInternetID: parse response: %w", err)
	}

	if len(result.Value) == 0 {
		return "", nil
	}
	return result.Value[0].ID, nil
}
