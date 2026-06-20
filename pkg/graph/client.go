package graph

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const graphBaseURL = "https://graph.microsoft.com/v1.0"

// Client is a Microsoft Graph API client scoped to a single mailbox.
type Client struct {
	tp      *TokenProvider
	userID  string
	httpc   *http.Client
	baseURL string // defaults to graphBaseURL; overridable in tests
}

// NewClient returns a Client that fetches tokens from tp and operates on the
// mailbox identified by userID (the UPN, e.g. "mail@example.com").
func NewClient(tp *TokenProvider, userID string) *Client {
	return &Client{
		tp:      tp,
		userID:  userID,
		httpc:   &http.Client{Timeout: 30 * time.Second},
		baseURL: graphBaseURL,
	}
}

// base returns the API base URL, falling back to the package default when the
// client was constructed without one (e.g. zero-value struct).
func (c *Client) base() string {
	if c.baseURL == "" {
		return graphBaseURL
	}
	return c.baseURL
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

	if resp.StatusCode == http.StatusNotFound {
		// 404 → the message no longer exists at this id (e.g. hard-deleted or
		// purged). Surface a sentinel so callers can treat it as a removal.
		return nil, ErrMessageNotFound
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

// MoveMessage moves a message to a folder identified by a well-known folder name
// (e.g. "archive", "deleteditems") via POST /users/{id}/messages/{id}/move.
// Follows the same token/header pattern as SetRead. Non-2xx → error with body.
func (c *Client) MoveMessage(ctx context.Context, msgID, destWellKnownFolder string) error {
	token, err := c.tp.Token(ctx)
	if err != nil {
		return fmt.Errorf("graph MoveMessage: acquire token: %w", err)
	}

	moveURL := fmt.Sprintf("%s/users/%s/messages/%s/move", c.base(), c.userID, url.PathEscape(msgID))
	payload := []byte(`{"destinationId":"` + destWellKnownFolder + `"}`)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, moveURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("graph MoveMessage: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Add("Prefer", `IdType="ImmutableId"`)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("graph MoveMessage: http: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("graph MoveMessage: read body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("graph MoveMessage: status %d: %s", resp.StatusCode, body)
	}
	return nil
}

// DeleteMessage soft-deletes a message via DELETE /users/{id}/messages/{id},
// which moves it to Deleted Items (recoverable). Non-2xx → error with body.
func (c *Client) DeleteMessage(ctx context.Context, msgID string) error {
	token, err := c.tp.Token(ctx)
	if err != nil {
		return fmt.Errorf("graph DeleteMessage: acquire token: %w", err)
	}

	delURL := fmt.Sprintf("%s/users/%s/messages/%s", c.base(), c.userID, url.PathEscape(msgID))
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, delURL, nil)
	if err != nil {
		return fmt.Errorf("graph DeleteMessage: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Add("Prefer", `IdType="ImmutableId"`)

	resp, err := c.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("graph DeleteMessage: http: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("graph DeleteMessage: read body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("graph DeleteMessage: status %d: %s", resp.StatusCode, body)
	}
	return nil
}

// InboxRef is a lightweight reference to an inbox message (immutable Graph id +
// RFC-822 internetMessageId), used by the self-heal audit.
type InboxRef struct {
	GraphID           string
	InternetMessageID string
}

// ListRecentInbox returns the most recent inbox messages (newest first) with only
// their immutable id and internetMessageId, for orphan auditing.
func (c *Client) ListRecentInbox(ctx context.Context, top int) ([]InboxRef, error) {
	token, err := c.tp.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("graph ListRecentInbox: acquire token: %w", err)
	}
	q := url.Values{}
	q.Set("$top", strconv.Itoa(top))
	q.Set("$orderby", "receivedDateTime desc")
	q.Set("$select", "id,internetMessageId")
	listURL := fmt.Sprintf("%s/users/%s/mailFolders/inbox/messages?%s", c.base(), c.userID, q.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
	if err != nil {
		return nil, fmt.Errorf("graph ListRecentInbox: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Add("Prefer", `IdType="ImmutableId"`)

	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("graph ListRecentInbox: http: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("graph ListRecentInbox: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("graph ListRecentInbox: status %d: %s", resp.StatusCode, body)
	}
	var result struct {
		Value []struct {
			ID                string `json:"id"`
			InternetMessageID string `json:"internetMessageId"`
		} `json:"value"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("graph ListRecentInbox: parse: %w", err)
	}
	refs := make([]InboxRef, 0, len(result.Value))
	for _, v := range result.Value {
		refs = append(refs, InboxRef{GraphID: v.ID, InternetMessageID: v.InternetMessageID})
	}
	return refs, nil
}

// ReplyAttachment is a file to attach to an outgoing reply (raw bytes + name).
type ReplyAttachment struct {
	Name  string
	Bytes []byte
}

// SendReply sends a reply to origMsgID using the createReply→(attachments)→send
// flow. createReply produces a draft with threading + recipients pre-filled; we
// pass bodyText as the createReply "comment", which Graph inserts ABOVE the
// quoted original message (so the reply keeps the email correspondence). Any
// attachments are POSTed to the draft before it is sent. Returns the draft
// (Graph) id for mapping. Same token/Prefer pattern as SetRead. Non-2xx → error
// with body.
//
// NB: per Graph docs, createReply accepts EITHER a "comment" OR a "body" — never
// both (sending both is a 400). We use "comment" so the quoted original survives;
// we deliberately do NOT PATCH the draft body afterwards (that overwrote the
// quoted original in the previous implementation).
func (c *Client) SendReply(ctx context.Context, origMsgID, bodyText string, attachments ...ReplyAttachment) (string, error) {
	token, err := c.tp.Token(ctx)
	if err != nil {
		return "", fmt.Errorf("graph SendReply: acquire token: %w", err)
	}
	hdr := func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Add("Prefer", `IdType="ImmutableId"`)
		req.Header.Set("Content-Type", "application/json")
	}
	do := func(method, u string, payload []byte) ([]byte, int, error) {
		req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(payload))
		if err != nil {
			return nil, 0, err
		}
		hdr(req)
		resp, err := c.httpc.Do(req)
		if err != nil {
			return nil, 0, err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		return body, resp.StatusCode, err
	}

	// 1. createReply (with comment) → draft id. The comment becomes the reply
	//    text, inserted above the quoted original by Graph.
	createPayload, _ := json.Marshal(map[string]string{"comment": bodyText})
	createURL := fmt.Sprintf("%s/users/%s/messages/%s/createReply", c.base(), c.userID, url.PathEscape(origMsgID))
	body, status, err := do(http.MethodPost, createURL, createPayload)
	if err != nil {
		return "", fmt.Errorf("graph SendReply: createReply: %w", err)
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("graph SendReply: createReply status %d: %s", status, body)
	}
	var draft struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &draft); err != nil || draft.ID == "" {
		return "", fmt.Errorf("graph SendReply: parse draft id (body: %s): %w", body, err)
	}

	// 2. attachments → POST each to the draft before sending.
	for i, att := range attachments {
		attPayload, _ := json.Marshal(map[string]string{
			"@odata.type":  "#microsoft.graph.fileAttachment",
			"name":         att.Name,
			"contentBytes": base64.StdEncoding.EncodeToString(att.Bytes),
		})
		attURL := fmt.Sprintf("%s/users/%s/messages/%s/attachments", c.base(), c.userID, url.PathEscape(draft.ID))
		body, status, err = do(http.MethodPost, attURL, attPayload)
		if err != nil {
			return "", fmt.Errorf("graph SendReply: attachment %d (%s): %w", i, att.Name, err)
		}
		if status < 200 || status >= 300 {
			return "", fmt.Errorf("graph SendReply: attachment %d (%s) status %d: %s", i, att.Name, status, body)
		}
	}

	// 3. send
	sendURL := fmt.Sprintf("%s/users/%s/messages/%s/send", c.base(), c.userID, url.PathEscape(draft.ID))
	body, status, err = do(http.MethodPost, sendURL, []byte("{}"))
	if err != nil {
		return "", fmt.Errorf("graph SendReply: send: %w", err)
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("graph SendReply: send status %d: %s", status, body)
	}
	return draft.ID, nil
}
