package graph

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// attachmentsFixture mirrors the Graph API shape for
// GET /users/{id}/messages/{id}/attachments: a `value` array containing
// fileAttachments (with contentBytes), inline fileAttachments (isInline +
// contentId), and itemAttachments (no contentBytes — embedded messages).
func attachmentsFixture(t *testing.T) (body []byte, wantBytes []byte) {
	t.Helper()
	wantBytes = []byte("hello world")
	b64 := base64.StdEncoding.EncodeToString(wantBytes)
	inlineBytes := base64.StdEncoding.EncodeToString([]byte{0x89, 0x50, 0x4e, 0x47})
	json := `{"value":[` +
		`{"@odata.type":"#microsoft.graph.fileAttachment","name":"note.txt","contentType":"text/plain","size":11,"isInline":false,"contentBytes":"` + b64 + `"},` +
		`{"@odata.type":"#microsoft.graph.fileAttachment","name":"logo.png","contentType":"image/png","size":4,"isInline":true,"contentId":"logo123","contentBytes":"` + inlineBytes + `"},` +
		`{"@odata.type":"#microsoft.graph.itemAttachment","name":"Fwd: budget","contentType":null,"size":2048,"isInline":false}` +
		`]}`
	return []byte(json), wantBytes
}

func TestParseAttachmentsResponse(t *testing.T) {
	body, wantBytes := attachmentsFixture(t)

	atts, itemCount, err := parseAttachmentsResponse(body)
	if err != nil {
		t.Fatalf("parseAttachmentsResponse error: %v", err)
	}

	// itemAttachment is excluded from the slice but counted.
	if itemCount != 1 {
		t.Fatalf("itemCount = %d, want 1", itemCount)
	}
	if len(atts) != 2 {
		t.Fatalf("len(atts) = %d, want 2 (file + inline)", len(atts))
	}

	// First: regular file attachment with decoded bytes.
	file := atts[0]
	if file.Name != "note.txt" {
		t.Errorf("atts[0].Name = %q, want note.txt", file.Name)
	}
	if file.ContentType != "text/plain" {
		t.Errorf("atts[0].ContentType = %q, want text/plain", file.ContentType)
	}
	if string(file.Bytes) != string(wantBytes) {
		t.Errorf("atts[0].Bytes = %q, want %q", file.Bytes, wantBytes)
	}
	if file.Size != 11 {
		t.Errorf("atts[0].Size = %d, want 11", file.Size)
	}
	if file.IsInline {
		t.Error("atts[0].IsInline = true, want false")
	}

	// Second: inline attachment with contentId.
	inline := atts[1]
	if inline.Name != "logo.png" {
		t.Errorf("atts[1].Name = %q, want logo.png", inline.Name)
	}
	if inline.ContentType != "image/png" {
		t.Errorf("atts[1].ContentType = %q, want image/png", inline.ContentType)
	}
	if !inline.IsInline {
		t.Error("atts[1].IsInline = false, want true")
	}
	if inline.ContentID != "logo123" {
		t.Errorf("atts[1].ContentID = %q, want logo123", inline.ContentID)
	}
	if len(inline.Bytes) != 4 {
		t.Errorf("atts[1].Bytes len = %d, want 4", len(inline.Bytes))
	}
}

func TestFetchAttachments_RequestShape(t *testing.T) {
	body, wantBytes := attachmentsFixture(t)

	var gotMethod, gotPath, gotPrefer, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotPrefer = r.Header.Get("Prefer")
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	atts, itemCount, err := c.FetchAttachments(context.Background(), "mail@example.com", "MSG123")
	if err != nil {
		t.Fatalf("FetchAttachments error: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if !strings.HasSuffix(gotPath, "/users/mail@example.com/messages/MSG123/attachments") {
		t.Errorf("path = %q, want .../messages/MSG123/attachments", gotPath)
	}
	if !strings.Contains(gotPrefer, `IdType="ImmutableId"`) {
		t.Errorf("Prefer = %q, want IdType=ImmutableId", gotPrefer)
	}
	if gotAuth != "Bearer tok-X" {
		t.Errorf("Authorization = %q, want Bearer tok-X", gotAuth)
	}
	if itemCount != 1 {
		t.Errorf("itemCount = %d, want 1", itemCount)
	}
	if len(atts) != 2 {
		t.Fatalf("len(atts) = %d, want 2", len(atts))
	}
	if string(atts[0].Bytes) != string(wantBytes) {
		t.Errorf("atts[0].Bytes = %q, want %q", atts[0].Bytes, wantBytes)
	}
}

func TestFetchAttachments_Non200ReturnsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("ErrorAccessDenied"))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, _, err := c.FetchAttachments(context.Background(), "mail@example.com", "MSG123")
	if err == nil || !strings.Contains(err.Error(), "ErrorAccessDenied") {
		t.Fatalf("expected error with body, got %v", err)
	}
}
