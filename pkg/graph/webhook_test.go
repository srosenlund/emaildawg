package graph

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestValidationResponse_WithToken verifies that when a ?validationToken= query
// parameter is present, ValidationResponse writes the URL-decoded token as
// text/plain with HTTP 200 and returns true.
func TestValidationResponse_WithToken(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/_email/graph/webhook?validationToken=abc%20def", nil)
	w := httptest.NewRecorder()

	got := ValidationResponse(w, req)

	if !got {
		t.Fatal("ValidationResponse should return true when validationToken present")
	}
	res := w.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", res.StatusCode)
	}
	ct := res.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("expected Content-Type text/plain, got %q", ct)
	}
	body := w.Body.String()
	if body != "abc def" {
		t.Fatalf("expected body %q, got %q", "abc def", body)
	}
}

// TestValidationResponse_PercentLiteralToken verifies that a token containing a
// literal '%25' sequence (i.e. a URL-encoded percent sign) is not double-decoded.
// Microsoft Graph sends validation tokens that may include URL-encoded characters.
// r.URL.Query().Get() performs exactly one URL-decode; a second url.QueryUnescape
// call would corrupt the value by decoding '%25' → '%' a second time.
//
// URL encoding:  validationToken=100%252525off
// After 1 decode (net/http):    "100%2525off"
// After 2nd decode (old bug):   "100%25off"   ← wrong, corrupts token
// With fix (no 2nd decode):     "100%2525off" ← correct, preserves the value
func TestValidationResponse_PercentLiteralToken(t *testing.T) {
	// %2525 in the URL encodes a literal "%25" in the query value after one decode.
	req := httptest.NewRequest(http.MethodPost, "/_email/graph/webhook?validationToken=100%2525off", nil)
	w := httptest.NewRecorder()

	got := ValidationResponse(w, req)

	if !got {
		t.Fatal("ValidationResponse should return true when validationToken present")
	}
	body := w.Body.String()
	// net/http decodes %2525 → %25 (one pass). The body must preserve that exactly.
	// A buggy second url.QueryUnescape would further decode %25 → %, corrupting the token.
	if body != "100%25off" {
		t.Fatalf("expected body %q (single-decode, no corruption), got %q", "100%25off", body)
	}
}

// TestValidationResponse_NoToken verifies that ValidationResponse returns false
// and writes nothing when no validationToken parameter is present.
func TestValidationResponse_NoToken(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/_email/graph/webhook", nil)
	w := httptest.NewRecorder()

	got := ValidationResponse(w, req)

	if got {
		t.Fatal("ValidationResponse should return false when validationToken absent")
	}
	if w.Body.Len() != 0 {
		t.Fatalf("expected empty body, got %q", w.Body.String())
	}
}

// TestParseNotifications verifies that the standard Graph notification payload
// is correctly decoded into a Notification struct.
func TestParseNotifications(t *testing.T) {
	payload := []byte(`{
		"value": [{
			"subscriptionId": "sub-123",
			"clientState": "secret-abc",
			"changeType": "created",
			"resourceData": {"id": "msg-456"}
		}]
	}`)

	n, err := parseNotifications(payload)
	if err != nil {
		t.Fatalf("parseNotifications returned error: %v", err)
	}
	if len(n.Value) != 1 {
		t.Fatalf("expected 1 item, got %d", len(n.Value))
	}
	item := n.Value[0]
	if item.SubscriptionID != "sub-123" {
		t.Errorf("SubscriptionID: got %q, want %q", item.SubscriptionID, "sub-123")
	}
	if item.ClientState != "secret-abc" {
		t.Errorf("ClientState: got %q, want %q", item.ClientState, "secret-abc")
	}
	if item.ChangeType != "created" {
		t.Errorf("ChangeType: got %q, want %q", item.ChangeType, "created")
	}
	if item.ResourceData.ID != "msg-456" {
		t.Errorf("ResourceData.ID: got %q, want %q", item.ResourceData.ID, "msg-456")
	}
}

// TestParseNotifications_Invalid verifies that malformed JSON returns an error.
func TestParseNotifications_Invalid(t *testing.T) {
	_, err := parseNotifications([]byte(`{not valid json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}
