package graph

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
