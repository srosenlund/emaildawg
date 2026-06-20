package graph

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestClient builds a Client pointed at a test server with a stubbed token.
func newTestClient(t *testing.T, base string) *Client {
	t.Helper()
	calls := 0
	tp := newTestProvider("tenant", "client", "secret", stubFetch("tok-X", 3600, &calls))
	return &Client{tp: tp, userID: "mail@example.com", httpc: http.DefaultClient, baseURL: base}
}

func TestMoveMessage_RequestShape(t *testing.T) {
	var gotMethod, gotPath, gotPrefer, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotPrefer = r.Header.Get("Prefer")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	if err := c.MoveMessage(context.Background(), "AAA", "archive"); err != nil {
		t.Fatalf("MoveMessage error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/users/mail@example.com/messages/AAA/move" {
		t.Errorf("path = %q", gotPath)
	}
	if !strings.Contains(gotPrefer, `IdType="ImmutableId"`) {
		t.Errorf("Prefer = %q, want IdType immutable", gotPrefer)
	}
	if gotBody != `{"destinationId":"archive"}` {
		t.Errorf("body = %q", gotBody)
	}
}

func TestMoveMessage_Non2xxReturnsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("ErrorAccessDenied"))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	err := c.MoveMessage(context.Background(), "AAA", "archive")
	if err == nil || !strings.Contains(err.Error(), "ErrorAccessDenied") {
		t.Fatalf("expected error containing body, got %v", err)
	}
}

func TestDeleteMessage_RequestShape(t *testing.T) {
	var gotMethod, gotPath, gotPrefer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotPrefer = r.Header.Get("Prefer")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	if err := c.DeleteMessage(context.Background(), "BBB"); err != nil {
		t.Fatalf("DeleteMessage error: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", gotMethod)
	}
	if gotPath != "/users/mail@example.com/messages/BBB" {
		t.Errorf("path = %q", gotPath)
	}
	if !strings.Contains(gotPrefer, `IdType="ImmutableId"`) {
		t.Errorf("Prefer = %q", gotPrefer)
	}
}
