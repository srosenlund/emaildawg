package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// ErrMessageNotFound is returned by GetMessage when Graph answers 404 for a
// message id. It signals that the message no longer exists at that id (for
// example after a hard delete or after Deleted Items was emptied), which the
// removal classifier treats as a delete.
var ErrMessageNotFound = errors.New("graph: message not found (404)")

// RemovalKind classifies why a message left the inbox, as reported by the delta
// @removed signal cross-referenced with the message's current folder.
type RemovalKind int

const (
	// RemovalUnknown means we could not determine archive-vs-delete (e.g. the
	// message is in some other folder we don't map). Callers should take no
	// destructive action and log it.
	RemovalUnknown RemovalKind = iota
	// RemovalArchive means the message now lives in the Archive folder.
	RemovalArchive
	// RemovalDelete means the message is in Deleted Items or no longer exists.
	RemovalDelete
)

func (k RemovalKind) String() string {
	switch k {
	case RemovalArchive:
		return "archive"
	case RemovalDelete:
		return "delete"
	default:
		return "unknown"
	}
}

// ClassifyRemoval is a pure function that decides what a delta @removed item
// means, given the removed message's current parentFolderId, the resolved id of
// the Archive folder, and the resolved ids of the delete-destination folders.
//
//   - parentFolderID == archiveFolderID                 → RemovalArchive
//   - parentFolderID ∈ deleteFolderIDs                  → RemovalDelete
//   - notFound (message gone, 404)                       → RemovalDelete
//   - anything else (e.g. moved to a custom folder)      → RemovalUnknown
//
// deleteFolderIDs covers both "Deleted Items" (Outlook-client delete) and
// "Recoverable Items\Deletions" (Graph DELETE / retention) — a delete can land
// in either depending on how it was issued. Empty ids never match a real
// parentFolderID, so the function degrades to RemovalUnknown rather than guessing.
func ClassifyRemoval(parentFolderID, archiveFolderID string, deleteFolderIDs []string, notFound bool) RemovalKind {
	if notFound {
		return RemovalDelete
	}
	if archiveFolderID != "" && parentFolderID == archiveFolderID {
		return RemovalArchive
	}
	for _, d := range deleteFolderIDs {
		if d != "" && parentFolderID == d {
			return RemovalDelete
		}
	}
	return RemovalUnknown
}

// ResolveWellKnownFolderID resolves a Graph well-known folder name (e.g.
// "archive", "deleteditems") to its immutable folder id for this mailbox. The id
// is what message.parentFolderId is compared against. Same token/Prefer pattern
// as the rest of the client. Non-200 → error with body.
func (c *Client) ResolveWellKnownFolderID(ctx context.Context, wellKnownName string) (string, error) {
	token, err := c.tp.Token(ctx)
	if err != nil {
		return "", fmt.Errorf("graph ResolveWellKnownFolderID: acquire token: %w", err)
	}

	folderURL := fmt.Sprintf("%s/users/%s/mailFolders/%s?$select=id", c.base(), c.userID, wellKnownName)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, folderURL, nil)
	if err != nil {
		return "", fmt.Errorf("graph ResolveWellKnownFolderID: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Add("Prefer", `IdType="ImmutableId"`)

	resp, err := c.httpc.Do(req)
	if err != nil {
		return "", fmt.Errorf("graph ResolveWellKnownFolderID: http: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("graph ResolveWellKnownFolderID: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("graph ResolveWellKnownFolderID: status %d: %s", resp.StatusCode, body)
	}

	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("graph ResolveWellKnownFolderID: parse: %w", err)
	}
	return result.ID, nil
}
