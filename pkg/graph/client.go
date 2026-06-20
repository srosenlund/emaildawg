package graph

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
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

	url := fmt.Sprintf("%s/users/%s/messages/%s", graphBaseURL, c.userID, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
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

	url := fmt.Sprintf("%s/users/%s/messages/%s", graphBaseURL, c.userID, msgID)

	isReadStr := "false"
	if isRead {
		isReadStr = "true"
	}
	payload := []byte(`{"isRead":` + isReadStr + `}`)

	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(payload))
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
