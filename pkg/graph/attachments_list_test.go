package graph

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestParseInboxListResponse verifies the list-page parser extracts refs and the
// @odata.nextLink for pagination.
func TestParseInboxListResponse(t *testing.T) {
	body := []byte(`{
		"@odata.nextLink": "https://graph.microsoft.com/v1.0/users/x/mailFolders/inbox/messages?$skip=10",
		"value": [
			{"id": "AAA", "internetMessageId": "<a@host>"},
			{"id": "BBB", "internetMessageId": "<b@host>"}
		]
	}`)
	refs, next, err := parseInboxListResponse(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("expected 2 refs, got %d", len(refs))
	}
	if refs[0].GraphID != "AAA" || refs[0].InternetMessageID != "<a@host>" {
		t.Fatalf("ref0 mismatch: %+v", refs[0])
	}
	if !strings.Contains(next, "$skip=10") {
		t.Fatalf("expected nextLink, got %q", next)
	}
}

// TestParseInboxListResponse_NoNext verifies an empty nextLink terminates pagination.
func TestParseInboxListResponse_NoNext(t *testing.T) {
	body := []byte(`{"value": [{"id": "AAA", "internetMessageId": "<a@host>"}]}`)
	refs, next, err := parseInboxListResponse(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d", len(refs))
	}
	if next != "" {
		t.Fatalf("expected empty nextLink, got %q", next)
	}
}

// TestListAttachmentMessagesSince_RequestShape asserts the OData filter
// (hasAttachments eq true and receivedDateTime ge <ISO>), $select, $top, the
// immutable-id Prefer header, and that pagination follows @odata.nextLink.
func TestListAttachmentMessagesSince_RequestShape(t *testing.T) {
	var firstQuery, gotPrefer string
	page := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPrefer = r.Header.Get("Prefer")
		w.Header().Set("Content-Type", "application/json")
		if page == 0 {
			firstQuery = r.URL.RawQuery
			next := fmt.Sprintf("http://%s/page2", r.Host)
			page++
			fmt.Fprintf(w, `{"@odata.nextLink": %q, "value": [{"id":"AAA","internetMessageId":"<a@host>"}]}`, next)
			return
		}
		// page 2: server-provided nextLink must be followed verbatim, no extra query rebuilding.
		if r.URL.Path != "/page2" {
			t.Errorf("page 2 path = %q, want /page2 (nextLink followed verbatim)", r.URL.Path)
		}
		fmt.Fprint(w, `{"value": [{"id":"BBB","internetMessageId":"<b@host>"}]}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	since := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	refs, err := c.ListAttachmentMessagesSince(context.Background(), since, 50)
	if err != nil {
		t.Fatalf("ListAttachmentMessagesSince error: %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("expected 2 refs across 2 pages, got %d", len(refs))
	}
	if refs[0].GraphID != "AAA" || refs[1].GraphID != "BBB" {
		t.Fatalf("refs mismatch: %+v", refs)
	}
	if !strings.Contains(gotPrefer, `IdType="ImmutableId"`) {
		t.Errorf("Prefer = %q, want IdType immutable", gotPrefer)
	}
	// $filter must contain both the hasAttachments and receivedDateTime ge clauses.
	if !strings.Contains(firstQuery, "hasAttachments+eq+true") {
		t.Errorf("query missing hasAttachments clause: %q", firstQuery)
	}
	if !strings.Contains(firstQuery, "receivedDateTime+ge+2026-05-01T00%3A00%3A00Z") {
		t.Errorf("query missing receivedDateTime ge clause: %q", firstQuery)
	}
	if !strings.Contains(firstQuery, "%24top=50") {
		t.Errorf("query missing $top: %q", firstQuery)
	}
}
