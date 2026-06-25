package graph

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// GraphAttachment is a decoded file attachment for a single Graph message.
// It is produced by FetchAttachments / parseAttachmentsResponse. Only
// fileAttachments (with contentBytes) are represented; itemAttachments
// (embedded messages, no contentBytes) are excluded from the slice and counted
// separately so the deliver layer can emit a notice for them.
type GraphAttachment struct {
	Name        string
	ContentType string
	Bytes       []byte
	Size        int64
	IsInline    bool
	ContentID   string
}

// GraphAttachment satisfies the email.MatrixAttachment interface
// (GetName/GetContentType/GetSize/GetBytes) by structure, so the Graph delivery
// path can hand its attachments straight to email.ConvertAttachmentPart without
// an adapter or importing the email package here.
func (a GraphAttachment) GetName() string        { return a.Name }
func (a GraphAttachment) GetContentType() string { return a.ContentType }
func (a GraphAttachment) GetSize() int64         { return a.Size }
func (a GraphAttachment) GetBytes() []byte       { return a.Bytes }

// graphAttachmentJSON mirrors the Graph API JSON shape for an attachment in the
// GET /messages/{id}/attachments collection. ContentBytes is absent for
// itemAttachments (#microsoft.graph.itemAttachment).
type graphAttachmentJSON struct {
	ODataType    string `json:"@odata.type"`
	Name         string `json:"name"`
	ContentType  string `json:"contentType"`
	Size         int64  `json:"size"`
	IsInline     bool   `json:"isInline"`
	ContentID    string `json:"contentId"`
	ContentBytes string `json:"contentBytes"`
}

// parseAttachmentsResponse parses the body of a
// GET /users/{id}/messages/{id}/attachments response. For each
// #microsoft.graph.fileAttachment it base64-decodes contentBytes into Bytes and
// maps name/contentType/size/isInline/contentId. #microsoft.graph.itemAttachment
// entries (no contentBytes) are excluded from the returned slice but counted in
// the returned int. The function is HTTP-free so it is unit-testable against a
// JSON fixture.
func parseAttachmentsResponse(data []byte) ([]GraphAttachment, int, error) {
	var raw struct {
		Value []graphAttachmentJSON `json:"value"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, 0, fmt.Errorf("parse attachments response: %w", err)
	}

	atts := make([]GraphAttachment, 0, len(raw.Value))
	itemCount := 0
	for _, a := range raw.Value {
		if a.ODataType == "#microsoft.graph.itemAttachment" {
			itemCount++
			continue
		}
		// Treat anything with contentBytes as a fileAttachment. (Reference
		// attachments also carry no contentBytes; we skip them like items.)
		if a.ContentBytes == "" {
			itemCount++
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(a.ContentBytes)
		if err != nil {
			return nil, 0, fmt.Errorf("decode attachment %q contentBytes: %w", a.Name, err)
		}
		atts = append(atts, GraphAttachment{
			Name:        a.Name,
			ContentType: a.ContentType,
			Bytes:       decoded,
			Size:        a.Size,
			IsInline:    a.IsInline,
			ContentID:   a.ContentID,
		})
	}
	return atts, itemCount, nil
}

// FetchAttachments lazily fetches and decodes the attachments for a single
// message via GET /users/{userID}/messages/{msgID}/attachments. It returns the
// decoded fileAttachments plus the count of excluded itemAttachments (embedded
// messages). Same token/Prefer (IdType="ImmutableId") pattern as the other
// Client methods. Non-200 responses are returned as an error including the body.
func (c *Client) FetchAttachments(ctx context.Context, userID, msgID string) ([]GraphAttachment, int, error) {
	token, err := c.tp.Token(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("graph FetchAttachments: acquire token: %w", err)
	}

	u := fmt.Sprintf("%s/users/%s/messages/%s/attachments",
		c.base(), url.PathEscape(userID), url.PathEscape(msgID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("graph FetchAttachments: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Add("Prefer", `IdType="ImmutableId"`)

	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("graph FetchAttachments: http: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("graph FetchAttachments: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("graph FetchAttachments: status %d: %s", resp.StatusCode, body)
	}

	return parseAttachmentsResponse(body)
}

// parseInboxListResponse parses one page of a
// GET .../mailFolders/inbox/messages?$select=id,internetMessageId response into
// InboxRefs plus the @odata.nextLink (empty when there are no more pages). HTTP-free
// so it is unit-testable against a JSON fixture.
func parseInboxListResponse(data []byte) ([]InboxRef, string, error) {
	var raw struct {
		NextLink string `json:"@odata.nextLink"`
		Value    []struct {
			ID                string `json:"id"`
			InternetMessageID string `json:"internetMessageId"`
		} `json:"value"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, "", fmt.Errorf("parse inbox list response: %w", err)
	}
	refs := make([]InboxRef, 0, len(raw.Value))
	for _, v := range raw.Value {
		refs = append(refs, InboxRef{GraphID: v.ID, InternetMessageID: v.InternetMessageID})
	}
	return refs, raw.NextLink, nil
}

// backfillListMaxPages bounds pagination so a misbehaving/huge mailbox cannot
// loop unbounded during a backfill walk.
const backfillListMaxPages = 50

// ListAttachmentMessagesSince enumerates inbox messages that have attachments and
// were received on or after `since`, returning their immutable Graph id +
// internetMessageId. It issues:
//
//	GET /users/{userID}/mailFolders/inbox/messages
//	    ?$filter=hasAttachments eq true and receivedDateTime ge <ISO8601Z>
//	    &$select=id,internetMessageId&$top=<top>&$orderby=receivedDateTime desc
//
// and follows @odata.nextLink pages verbatim (Graph signs the continuation token
// into the link). Same token / Prefer (IdType="ImmutableId") pattern as the other
// Client methods. Used by the connector's attachment-backfill walk.
func (c *Client) ListAttachmentMessagesSince(ctx context.Context, since time.Time, top int) ([]InboxRef, error) {
	token, err := c.tp.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("graph ListAttachmentMessagesSince: acquire token: %w", err)
	}

	q := url.Values{}
	q.Set("$filter", fmt.Sprintf("hasAttachments eq true and receivedDateTime ge %s",
		since.UTC().Format("2006-01-02T15:04:05Z")))
	q.Set("$select", "id,internetMessageId")
	q.Set("$top", strconv.Itoa(top))
	q.Set("$orderby", "receivedDateTime desc")
	nextURL := fmt.Sprintf("%s/users/%s/mailFolders/inbox/messages?%s",
		c.base(), url.PathEscape(c.userID), q.Encode())

	var all []InboxRef
	for page := 0; nextURL != "" && page < backfillListMaxPages; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, nextURL, nil)
		if err != nil {
			return nil, fmt.Errorf("graph ListAttachmentMessagesSince: build request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Add("Prefer", `IdType="ImmutableId"`)

		resp, err := c.httpc.Do(req)
		if err != nil {
			return nil, fmt.Errorf("graph ListAttachmentMessagesSince: http: %w", err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("graph ListAttachmentMessagesSince: read body: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("graph ListAttachmentMessagesSince: status %d: %s", resp.StatusCode, body)
		}

		refs, next, perr := parseInboxListResponse(body)
		if perr != nil {
			return nil, perr
		}
		all = append(all, refs...)
		nextURL = next
	}
	return all, nil
}
