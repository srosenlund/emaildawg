package graph

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// NotificationItem represents a single change notification from Microsoft Graph.
type NotificationItem struct {
	SubscriptionID string `json:"subscriptionId"`
	ClientState    string `json:"clientState"`
	ChangeType     string `json:"changeType"`
	ResourceData   struct {
		ID string `json:"id"`
	} `json:"resourceData"`
}

// Notification is the top-level payload sent by Graph to the notification URL.
type Notification struct {
	Value []NotificationItem `json:"value"`
}

// ValidationResponse handles the Microsoft Graph subscription validation
// handshake. If the request contains a ?validationToken= query parameter, it
// writes the URL-decoded token as text/plain with HTTP 200 and returns true.
// The caller must return immediately after receiving true.
// If no validationToken is present, it returns false and writes nothing.
func ValidationResponse(w http.ResponseWriter, r *http.Request) bool {
	token := r.URL.Query().Get("validationToken")
	if token == "" {
		return false
	}
	// The token arrives URL-encoded in the query string; net/http already
	// decodes query params, so token is already decoded here.
	decoded, err := url.QueryUnescape(token)
	if err != nil {
		// Fallback: use the raw (already partially decoded) value.
		decoded = token
	}
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, decoded)
	return true
}

// parseNotifications decodes a Graph change-notification JSON payload.
// It is unexported; use ParseNotifications for cross-package access.
func parseNotifications(data []byte) (*Notification, error) {
	return ParseNotifications(data)
}

// ParseNotifications decodes a Graph change-notification JSON payload.
func ParseNotifications(data []byte) (*Notification, error) {
	var n Notification
	if err := json.Unmarshal(data, &n); err != nil {
		return nil, fmt.Errorf("parse graph notifications: %w", err)
	}
	return &n, nil
}
