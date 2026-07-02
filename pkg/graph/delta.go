package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// ErrDeltaResync is returned by DeltaPage when the saved delta token is no
// longer valid (HTTP 410 or error code "syncStateNotFound"). The caller must
// re-baseline by running a full backfill from an empty token.
var ErrDeltaResync = errors.New("graph delta: resync required (token expired)")

// deltaPageJSON is the JSON shape of a single Graph delta query response page.
type deltaPageJSON struct {
	Value     []json.RawMessage `json:"value"`
	NextLink  string            `json:"@odata.nextLink"`
	DeltaLink string            `json:"@odata.deltaLink"`
}

// deltaRemovedItem is used to detect @removed items in the delta value array.
type deltaRemovedItem struct {
	ID      string `json:"id"`
	Removed *struct {
		Reason string `json:"reason"`
	} `json:"@removed"`
}

// graphErrorResponse is used to extract the error code from Graph error JSON.
type graphErrorResponse struct {
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
}

// parseDeltaPage parses a raw Graph delta query response page and returns:
//   - msgs: full message objects (items without @removed)
//   - removed: IDs of items that were deleted/moved out of the folder
//   - nextLink: set when there are more pages in this round (follow it)
//   - deltaLink: set when this round is done (save it for next reconcile)
//
// A page never has both nextLink and deltaLink set simultaneously.
func parseDeltaPage(data []byte) (msgs []*GraphMessage, removed []string, nextLink, deltaLink string, err error) {
	var page deltaPageJSON
	if err = json.Unmarshal(data, &page); err != nil {
		return nil, nil, "", "", fmt.Errorf("parseDeltaPage: unmarshal page: %w", err)
	}

	for _, rawItem := range page.Value {
		// First check if this is a @removed item.
		var probe deltaRemovedItem
		if err = json.Unmarshal(rawItem, &probe); err != nil {
			// Skip malformed items rather than aborting.
			continue
		}
		if probe.Removed != nil {
			// This is a removal notification — collect the id.
			removed = append(removed, probe.ID)
			continue
		}

		// Normal message item — parse with the full message parser.
		msg, parseErr := parseGraphMessage(rawItem)
		if parseErr != nil {
			// Tolerate stray read-state events (e.g. only isRead changed, no
			// receivedDateTime) — log at caller level if needed; don't abort.
			continue
		}
		msgs = append(msgs, msg)
	}

	return msgs, removed, page.NextLink, page.DeltaLink, nil
}

// DeltaPage performs a single Graph delta query page GET and returns the parsed
// results. If url is empty, it uses the initial inbox delta endpoint (with
// Prefer headers and maxpagesize). Otherwise it GETs the provided url verbatim
// (nextLink or deltaLink from a previous call).
//
// On HTTP 410 or error code "syncStateNotFound", it returns ErrDeltaResync so
// the caller can re-baseline with a fresh backfill.
func (c *Client) DeltaPage(ctx context.Context, url string) (msgs []*GraphMessage, removed []string, nextLink, deltaLink string, err error) {
	token, err := c.tp.Token(ctx)
	if err != nil {
		return nil, nil, "", "", fmt.Errorf("graph DeltaPage: acquire token: %w", err)
	}

	isInitial := url == ""
	if isInitial {
		url = fmt.Sprintf("%s/users/%s/mailFolders/inbox/messages/delta?$select=%s", graphBaseURL, c.userID, messageSelectFields)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, "", "", fmt.Errorf("graph DeltaPage: build request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Add("Prefer", `IdType="ImmutableId"`)
	req.Header.Add("Prefer", `outlook.body-content-type="html"`)
	if isInitial {
		req.Header.Add("Prefer", "odata.maxpagesize=50")
	}

	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, nil, "", "", fmt.Errorf("graph DeltaPage: http: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, "", "", fmt.Errorf("graph DeltaPage: read body: %w", err)
	}

	// 410 Gone → the delta token is stale; caller must re-baseline.
	if resp.StatusCode == http.StatusGone {
		return nil, nil, "", "", ErrDeltaResync
	}

	// Check for syncStateNotFound inside a 4xx body.
	if resp.StatusCode >= 400 {
		var errResp graphErrorResponse
		if jsonErr := json.Unmarshal(body, &errResp); jsonErr == nil {
			if errResp.Error.Code == "syncStateNotFound" {
				return nil, nil, "", "", ErrDeltaResync
			}
		}
		return nil, nil, "", "", fmt.Errorf("graph DeltaPage: status %d: %s", resp.StatusCode, body)
	}

	msgs, removed, nextLink, deltaLink, err = parseDeltaPage(body)
	if err != nil {
		return nil, nil, "", "", fmt.Errorf("graph DeltaPage: parse: %w", err)
	}

	return msgs, removed, nextLink, deltaLink, nil
}
