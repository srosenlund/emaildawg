package graph

import (
	"encoding/json"
	"testing"
	"time"
)

// TestBuildSubscriptionBody_Resource verifies that buildSubscriptionBody produces
// JSON with the correct resource path for the inbox messages endpoint.
func TestBuildSubscriptionBody_Resource(t *testing.T) {
	exp := time.Now().Add(6 * 24 * time.Hour)
	body, err := buildSubscriptionBody("alice@example.com", "https://example.com/_email/graph/webhook", "test-state", exp)
	if err != nil {
		t.Fatalf("buildSubscriptionBody returned error: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}

	want := "users/alice@example.com/mailFolders('inbox')/messages"
	if got, _ := m["resource"].(string); got != want {
		t.Errorf("resource: got %q, want %q", got, want)
	}
}

// TestBuildSubscriptionBody_ChangeType verifies changeType is "created,updated".
func TestBuildSubscriptionBody_ChangeType(t *testing.T) {
	exp := time.Now().Add(6 * 24 * time.Hour)
	body, err := buildSubscriptionBody("alice@example.com", "https://example.com/_email/graph/webhook", "test-state", exp)
	if err != nil {
		t.Fatalf("buildSubscriptionBody returned error: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}

	want := "created,updated"
	if got, _ := m["changeType"].(string); got != want {
		t.Errorf("changeType: got %q, want %q", got, want)
	}
}

// TestBuildSubscriptionBody_LifecycleURL verifies lifecycleNotificationUrl == notificationUrl.
func TestBuildSubscriptionBody_LifecycleURL(t *testing.T) {
	notifyURL := "https://example.com/_email/graph/webhook"
	exp := time.Now().Add(6 * 24 * time.Hour)
	body, err := buildSubscriptionBody("alice@example.com", notifyURL, "test-state", exp)
	if err != nil {
		t.Fatalf("buildSubscriptionBody returned error: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}

	if got, _ := m["notificationUrl"].(string); got != notifyURL {
		t.Errorf("notificationUrl: got %q, want %q", got, notifyURL)
	}
	if got, _ := m["lifecycleNotificationUrl"].(string); got != notifyURL {
		t.Errorf("lifecycleNotificationUrl: got %q, want %q", got, notifyURL)
	}
}

// TestBuildSubscriptionBody_ExpiryWithinCap verifies expirationDateTime is within 7 days.
func TestBuildSubscriptionBody_ExpiryWithinCap(t *testing.T) {
	exp := time.Now().Add(6 * 24 * time.Hour)
	body, err := buildSubscriptionBody("alice@example.com", "https://example.com/_email/graph/webhook", "test-state", exp)
	if err != nil {
		t.Fatalf("buildSubscriptionBody returned error: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}

	expStr, _ := m["expirationDateTime"].(string)
	if expStr == "" {
		t.Fatal("expirationDateTime missing or empty")
	}
	parsed, err := time.Parse(time.RFC3339, expStr)
	if err != nil {
		t.Fatalf("expirationDateTime is not RFC3339: %v", err)
	}

	maxExp := time.Now().Add(7 * 24 * time.Hour)
	if parsed.After(maxExp) {
		t.Errorf("expirationDateTime %v exceeds 7-day cap %v", parsed, maxExp)
	}
	if parsed.Before(time.Now()) {
		t.Errorf("expirationDateTime %v is in the past", parsed)
	}
}

// TestBuildSubscriptionBody_ClientState verifies clientState is present in the body.
func TestBuildSubscriptionBody_ClientState(t *testing.T) {
	exp := time.Now().Add(6 * 24 * time.Hour)
	body, err := buildSubscriptionBody("alice@example.com", "https://example.com/_email/graph/webhook", "my-secret-state", exp)
	if err != nil {
		t.Fatalf("buildSubscriptionBody returned error: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}

	if got, _ := m["clientState"].(string); got != "my-secret-state" {
		t.Errorf("clientState: got %q, want %q", got, "my-secret-state")
	}
}

// TestBuildSubscriptionBody_TLSVersion verifies latestSupportedTlsVersion is "v1_2".
func TestBuildSubscriptionBody_TLSVersion(t *testing.T) {
	exp := time.Now().Add(6 * 24 * time.Hour)
	body, err := buildSubscriptionBody("alice@example.com", "https://example.com/_email/graph/webhook", "test-state", exp)
	if err != nil {
		t.Fatalf("buildSubscriptionBody returned error: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}

	want := "v1_2"
	if got, _ := m["latestSupportedTlsVersion"].(string); got != want {
		t.Errorf("latestSupportedTlsVersion: got %q, want %q", got, want)
	}
}
