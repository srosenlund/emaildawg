package graph

import (
	"testing"
)

// TestParseDeltaPageWithDeltaLink tests a delta response page that contains:
// - one normal message item
// - one @removed item (deleted)
// - an @odata.deltaLink (round is done)
// Expected: 1 msg, removed==["X"], deltaLink set, nextLink empty.
func TestParseDeltaPageWithDeltaLink(t *testing.T) {
	page := []byte(`{
		"value": [
			{
				"id": "AAM=",
				"receivedDateTime": "2024-01-15T10:00:00Z",
				"internetMessageId": "<msg1@example.com>",
				"subject": "Hello World",
				"conversationId": "CONV1=",
				"isRead": false,
				"hasAttachments": false,
				"body": {"contentType": "text", "content": "body text"},
				"from": {"emailAddress": {"name": "Alice", "address": "alice@example.com"}},
				"toRecipients": [{"emailAddress": {"name": "Bob", "address": "bob@example.com"}}]
			},
			{
				"id": "X",
				"@removed": {"reason": "deleted"}
			}
		],
		"@odata.deltaLink": "https://graph.microsoft.com/v1.0/users/me/mailFolders/inbox/messages/delta?$deltatoken=abc123"
	}`)

	msgs, removed, nextLink, deltaLink, err := parseDeltaPage(page)
	if err != nil {
		t.Fatalf("parseDeltaPage returned error: %v", err)
	}

	// Should have exactly 1 normal message
	if len(msgs) != 1 {
		t.Fatalf("want 1 msg, got %d", len(msgs))
	}
	if msgs[0].ID != "AAM=" {
		t.Errorf("want msg.ID=AAM=, got %q", msgs[0].ID)
	}
	if msgs[0].Subject != "Hello World" {
		t.Errorf("want msg.Subject=\"Hello World\", got %q", msgs[0].Subject)
	}
	if msgs[0].FromAddress != "alice@example.com" {
		t.Errorf("want msg.FromAddress=alice@example.com, got %q", msgs[0].FromAddress)
	}

	// Should have exactly 1 removed id
	if len(removed) != 1 {
		t.Fatalf("want 1 removed id, got %d: %v", len(removed), removed)
	}
	if removed[0] != "X" {
		t.Errorf("want removed[0]=\"X\", got %q", removed[0])
	}

	// deltaLink should be set, nextLink should be empty
	if deltaLink == "" {
		t.Error("want deltaLink set, got empty")
	}
	if nextLink != "" {
		t.Errorf("want nextLink empty, got %q", nextLink)
	}
}

// TestParseDeltaPageWithNextLink tests a delta response page with a nextLink
// (more pages to follow — no deltaLink yet).
func TestParseDeltaPageWithNextLink(t *testing.T) {
	page := []byte(`{
		"value": [
			{
				"id": "BBN=",
				"receivedDateTime": "2024-01-16T09:00:00Z",
				"internetMessageId": "<msg2@example.com>",
				"subject": "Page 1 message",
				"conversationId": "CONV2=",
				"isRead": true,
				"hasAttachments": false,
				"body": {"contentType": "text", "content": "another body"},
				"from": {"emailAddress": {"name": "Carol", "address": "carol@example.com"}},
				"toRecipients": []
			}
		],
		"@odata.nextLink": "https://graph.microsoft.com/v1.0/users/me/mailFolders/inbox/messages/delta?$skiptoken=xyz"
	}`)

	msgs, removed, nextLink, deltaLink, err := parseDeltaPage(page)
	if err != nil {
		t.Fatalf("parseDeltaPage returned error: %v", err)
	}

	if len(msgs) != 1 {
		t.Fatalf("want 1 msg, got %d", len(msgs))
	}
	if len(removed) != 0 {
		t.Errorf("want 0 removed, got %d: %v", len(removed), removed)
	}

	// nextLink should be set, deltaLink should be empty
	if nextLink == "" {
		t.Error("want nextLink set, got empty")
	}
	if deltaLink != "" {
		t.Errorf("want deltaLink empty, got %q", deltaLink)
	}
}
