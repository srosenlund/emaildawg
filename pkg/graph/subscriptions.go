package graph

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// maxSubscriptionLifetime is the Microsoft Graph maximum subscription duration
// for mail resources (~10080 min = 7 days). We use 6 days as the request value.
const maxSubscriptionLifetime = 7 * 24 * time.Hour

// subscriptionRequestBody mirrors the JSON shape for POST /subscriptions and
// PATCH /subscriptions/{id}.
type subscriptionRequestBody struct {
	ChangeType               string `json:"changeType"`
	NotificationURL          string `json:"notificationUrl"`
	LifecycleNotificationURL string `json:"lifecycleNotificationUrl"`
	Resource                 string `json:"resource"`
	ExpirationDateTime       string `json:"expirationDateTime"`
	ClientState              string `json:"clientState"`
	LatestSupportedTLSVersion string `json:"latestSupportedTlsVersion"`
}

// subscriptionRenewBody is the minimal PATCH payload for renewing expiry.
type subscriptionRenewBody struct {
	ExpirationDateTime string `json:"expirationDateTime"`
}

// subscriptionResponse is the subset of the Graph subscription resource we care about.
type subscriptionResponse struct {
	ID                 string `json:"id"`
	ExpirationDateTime string `json:"expirationDateTime"`
}

// Subscription is the result of a Graph subscription create or renew.
type Subscription struct {
	ID                 string
	ExpirationDateTime time.Time
}

// buildSubscriptionBody constructs the JSON POST body for creating a Graph
// change-notification subscription. It is a pure function with no I/O — suitable
// for unit testing.
//
// The expiry is capped at now+7 days (Graph's maximum for mail) even if the
// caller passes a larger value.
func buildSubscriptionBody(userID, notifyURL, clientState string, exp time.Time) ([]byte, error) {
	// Enforce the 7-day cap.
	cap := time.Now().Add(maxSubscriptionLifetime)
	if exp.After(cap) {
		exp = cap
	}

	payload := subscriptionRequestBody{
		ChangeType:               "created,updated",
		NotificationURL:          notifyURL,
		LifecycleNotificationURL: notifyURL,
		Resource:                 fmt.Sprintf("users/%s/mailFolders('inbox')/messages", userID),
		ExpirationDateTime:       exp.UTC().Format(time.RFC3339),
		ClientState:              clientState,
		LatestSupportedTLSVersion: "v1_2",
	}
	return json.Marshal(payload)
}

// CreateSubscription POSTs a new subscription to Graph and returns the created
// Subscription. The subscription watches the userID mailbox inbox for
// created+updated messages and delivers lifecycle events to the same notifyURL.
func (c *Client) CreateSubscription(ctx context.Context, notifyURL, clientState string) (*Subscription, error) {
	exp := time.Now().Add(6 * 24 * time.Hour)
	body, err := buildSubscriptionBody(c.userID, notifyURL, clientState, exp)
	if err != nil {
		return nil, fmt.Errorf("graph CreateSubscription: build body: %w", err)
	}

	token, err := c.tp.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("graph CreateSubscription: acquire token: %w", err)
	}

	url := fmt.Sprintf("%s/subscriptions", graphBaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("graph CreateSubscription: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("graph CreateSubscription: http: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("graph CreateSubscription: read response: %w", err)
	}

	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("graph CreateSubscription: status %d: %s", resp.StatusCode, respBody)
	}

	var sub subscriptionResponse
	if err := json.Unmarshal(respBody, &sub); err != nil {
		return nil, fmt.Errorf("graph CreateSubscription: parse response: %w", err)
	}

	expiry, err := time.Parse(time.RFC3339, sub.ExpirationDateTime)
	if err != nil {
		// Fallback: use what we sent
		expiry = exp
	}

	return &Subscription{
		ID:                 sub.ID,
		ExpirationDateTime: expiry,
	}, nil
}

// RenewSubscription PATCHes the subscription expiry to extend its lifetime.
// This should be called when within 24h of expiry (see renewal goroutine).
func (c *Client) RenewSubscription(ctx context.Context, id string, exp time.Time) error {
	// Enforce the 7-day cap.
	cap := time.Now().Add(maxSubscriptionLifetime)
	if exp.After(cap) {
		exp = cap
	}

	payload := subscriptionRenewBody{
		ExpirationDateTime: exp.UTC().Format(time.RFC3339),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("graph RenewSubscription: build body: %w", err)
	}

	token, err := c.tp.Token(ctx)
	if err != nil {
		return fmt.Errorf("graph RenewSubscription: acquire token: %w", err)
	}

	url := fmt.Sprintf("%s/subscriptions/%s", graphBaseURL, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("graph RenewSubscription: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("graph RenewSubscription: http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("graph RenewSubscription: status %d: %s", resp.StatusCode, respBody)
	}
	return nil
}

// DeleteSubscription sends DELETE /subscriptions/{id}. A 204 is success.
func (c *Client) DeleteSubscription(ctx context.Context, id string) error {
	token, err := c.tp.Token(ctx)
	if err != nil {
		return fmt.Errorf("graph DeleteSubscription: acquire token: %w", err)
	}

	url := fmt.Sprintf("%s/subscriptions/%s", graphBaseURL, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("graph DeleteSubscription: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("graph DeleteSubscription: http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("graph DeleteSubscription: status %d: %s", resp.StatusCode, respBody)
	}
	return nil
}
