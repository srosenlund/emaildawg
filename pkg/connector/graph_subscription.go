package connector

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/iFixRobots/emaildawg/pkg/graph"
)

// subscriptionRenewalInterval is how often we check whether the subscription
// needs renewal. Per spec: renew when within 24h of expiry.
const subscriptionRenewalInterval = 1 * time.Hour

// subscriptionRenewalThreshold is the remaining lifetime below which we renew.
const subscriptionRenewalThreshold = 24 * time.Hour

// subscriptionLifetime is the requested subscription duration (6 days).
// Microsoft's maximum for mail = 7 days; we stay 1 day under the cap so there
// is room to renew before Graph auto-removes the subscription.
const subscriptionLifetime = 6 * 24 * time.Hour

// generateClientState produces a cryptographically random 32-byte hex string
// to use as the subscription clientState. It is NOT derived from OAuth secrets —
// it is generated fresh each time a new subscription is created so that it is
// unpredictable and unique to this bridge instance.
func generateClientState() (string, error) {
	b := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// ensureGraphSubscription is called from Start() after the webhook route is
// registered. It loads or creates a Graph change-notification subscription for
// the auto-login mailbox, sets ec.graphClientState to the stored value, and
// launches the background renewal goroutine.
//
// It is a no-op when graphClient is nil (OAuth2 not configured) or when the
// public base URL cannot be determined.
func (ec *EmailConnector) ensureGraphSubscription(ctx context.Context) {
	if ec.graphClient == nil {
		return
	}

	// Resolve the public base URL via the MatrixConnectorWithServer interface.
	// Start() already confirmed this assertion succeeds before registering routes.
	matrixWithServer, ok := ec.Bridge.Matrix.(interface{ GetPublicAddress() string })
	if !ok {
		ec.Bridge.Log.Warn().Msg("Graph subscription: bridge Matrix does not expose GetPublicAddress; subscription skipped")
		return
	}
	base := matrixWithServer.GetPublicAddress()
	if base == "" {
		ec.Bridge.Log.Warn().Msg("Graph subscription: public_address is empty; subscription skipped")
		return
	}
	notifyURL := base + "/_email/graph/webhook"

	// Determine owner MXID and email for the persisted GraphState row.
	ownerMXID := ec.Config.OAuth2.OwnerMXID
	email := ec.Config.OAuth2.AutoLoginEmail
	if ownerMXID == "" || email == "" {
		ec.Bridge.Log.Warn().Msg("Graph subscription: owner_mxid or auto_login_email not set; subscription skipped")
		return
	}

	// Check for an existing, still-valid subscription in the store.
	gs, err := ec.DB.GetGraphState(ctx, ownerMXID, email)
	if err != nil {
		ec.Bridge.Log.Error().Err(err).Msg("Graph subscription: failed to load GraphState; creating new subscription")
	}

	if gs != nil && gs.SubscriptionID != "" && time.Until(gs.SubscriptionExpiry) > subscriptionRenewalThreshold {
		// Reuse the existing subscription — it is valid and has >24h left.
		ec.graphMu.Lock()
		ec.graphClientState = gs.ClientState
		ec.graphMu.Unlock()
		ec.Bridge.Log.Info().
			Str("subscription_id", gs.SubscriptionID).
			Time("expiry", gs.SubscriptionExpiry).
			Msg("Graph subscription: reusing existing valid subscription")
		go ec.runSubscriptionRenewal(ctx, ownerMXID, email, gs.SubscriptionID, gs.SubscriptionExpiry)
		return
	}

	// Generate a fresh random clientState — NOT derived from the OAuth2 secret.
	clientState, err := generateClientState()
	if err != nil {
		ec.Bridge.Log.Error().Err(err).Msg("Graph subscription: failed to generate clientState; subscription skipped")
		return
	}

	sub, err := ec.graphClient.CreateSubscription(ctx, notifyURL, clientState)
	if err != nil {
		ec.Bridge.Log.Error().Err(err).Msg("Graph subscription: CreateSubscription failed")
		return
	}

	ec.graphMu.Lock()
	ec.graphClientState = clientState
	ec.graphMu.Unlock()

	newGS := &GraphState{
		UserMXID:           ownerMXID,
		Email:              email,
		SubscriptionID:     sub.ID,
		SubscriptionExpiry: sub.ExpirationDateTime,
		ClientState:        clientState,
		InboxDeltaLink:     func() string {
			if gs != nil {
				return gs.InboxDeltaLink
			}
			return ""
		}(),
	}
	if err := ec.DB.UpsertGraphState(ctx, newGS); err != nil {
		ec.Bridge.Log.Warn().Err(err).
			Str("subscription_id", sub.ID).
			Msg("Graph subscription: subscription is LIVE but failed to persist GraphState — a restart may recreate a duplicate subscription")
	}

	ec.Bridge.Log.Info().
		Str("subscription_id", sub.ID).
		Time("expiry", sub.ExpirationDateTime).
		Msg("Graph subscription: created new subscription")

	// On first ever subscription (no saved deltaLink), run a backfill to
	// populate InboxDeltaLink and deliver historical messages. Subsequent
	// subscription recreations (subscriptionRemoved) fall through to the
	// lifecycle handler which calls reconcile instead.
	if newGS.InboxDeltaLink == "" {
		go ec.runBackfill(ctx)
	}

	go ec.runSubscriptionRenewal(ctx, ownerMXID, email, sub.ID, sub.ExpirationDateTime)
}

// persistRenewedExpiry loads the current GraphState from DB, updates
// SubscriptionExpiry to newExp, and upserts it. It reads ownerMXID and email
// from ec.Config so it can be called from any goroutine without extra args.
// Errors are logged at Warn level — they do not block the subscription being live.
func (ec *EmailConnector) persistRenewedExpiry(ctx context.Context, newExp time.Time) {
	ownerMXID := ec.Config.OAuth2.OwnerMXID
	email := ec.Config.OAuth2.AutoLoginEmail
	gs, dbErr := ec.DB.GetGraphState(ctx, ownerMXID, email)
	if dbErr != nil {
		ec.Bridge.Log.Warn().Err(dbErr).Msg("Graph subscription: failed to load GraphState for expiry persist")
		return
	}
	if gs == nil {
		ec.Bridge.Log.Warn().Msg("Graph subscription: no GraphState found to persist renewed expiry")
		return
	}
	gs.SubscriptionExpiry = newExp
	if upsertErr := ec.DB.UpsertGraphState(ctx, gs); upsertErr != nil {
		ec.Bridge.Log.Warn().Err(upsertErr).Msg("Graph subscription: failed to persist renewed expiry")
	}
}

// runSubscriptionRenewal ticks every hour and PATCHes the subscription expiry
// whenever it is within 24h of expiry. If the PATCH fails it logs the error
// and retries on the next tick.
func (ec *EmailConnector) runSubscriptionRenewal(ctx context.Context, ownerMXID, email, subID string, expiry time.Time) {
	ticker := time.NewTicker(subscriptionRenewalInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if time.Until(expiry) > subscriptionRenewalThreshold {
				continue
			}

			newExp := time.Now().Add(subscriptionLifetime)
			if err := ec.graphClient.RenewSubscription(ctx, subID, newExp); err != nil {
				ec.Bridge.Log.Error().Err(err).
					Str("subscription_id", subID).
					Msg("Graph subscription: renewal failed; will retry next tick")
				continue
			}
			expiry = newExp

			// Persist updated expiry via shared helper.
			ec.persistRenewedExpiry(ctx, newExp)

			ec.Bridge.Log.Info().
				Str("subscription_id", subID).
				Time("new_expiry", expiry).
				Msg("Graph subscription: renewed successfully")
		}
	}
}

// lifecycleNotification is the Graph lifecycle event payload item.
type lifecycleNotification struct {
	LifecycleEvent string `json:"lifecycleEvent"`
	SubscriptionID string `json:"subscriptionId"`
	ClientState    string `json:"clientState"`
}

type lifecyclePayload struct {
	Value []lifecycleNotification `json:"value"`
}

// handleLifecycleEvents inspects an already-read webhook body for lifecycle
// events. It is called from handleGraphWebhookFull when the body contains a
// "lifecycleEvent" field. Returns true if the body was a lifecycle payload
// (caller should not treat it as a change notification).
func (ec *EmailConnector) handleLifecycleEvents(ctx context.Context, body []byte) bool {
	// Quick check: lifecycle payloads have "lifecycleEvent" somewhere in the value array.
	// We parse into a generic map first to detect the presence of the field cheaply.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return false
	}

	valueRaw, ok := raw["value"]
	if !ok {
		return false
	}

	var items []lifecycleNotification
	if err := json.Unmarshal(valueRaw, &items); err != nil {
		return false
	}

	// Determine whether any item has a non-empty lifecycleEvent.
	isLifecycle := false
	for _, item := range items {
		if item.LifecycleEvent != "" {
			isLifecycle = true
			break
		}
	}
	if !isLifecycle {
		return false
	}

	for _, item := range items {
		if item.ClientState != ec.expectedClientState() {
			ec.Bridge.Log.Warn().
				Str("lifecycle_event", item.LifecycleEvent).
				Str("subscription_id", item.SubscriptionID).
				Msg("Graph lifecycle: clientState mismatch — ignoring item")
			continue
		}

		switch item.LifecycleEvent {
		case "reauthorizationRequired":
			// Per Microsoft spec: respond with a single PATCH renew within 10 min.
			// Do NOT also call a /reauthorize endpoint within that window — one call only.
			ec.Bridge.Log.Info().
				Str("subscription_id", item.SubscriptionID).
				Msg("Graph lifecycle: reauthorizationRequired — renewing subscription")
			go func(subID string) {
				newExp := time.Now().Add(subscriptionLifetime)
				if err := ec.graphClient.RenewSubscription(ctx, subID, newExp); err != nil {
					ec.Bridge.Log.Error().Err(err).
						Str("subscription_id", subID).
						Msg("Graph lifecycle: renewal after reauthorizationRequired failed")
					return
				}
				ec.persistRenewedExpiry(ctx, newExp)
				ec.Bridge.Log.Info().Str("subscription_id", subID).Msg("Graph lifecycle: reauthorization renewal succeeded")
			}(item.SubscriptionID)

		case "subscriptionRemoved":
			// Graph removed our subscription — recreate it and reconcile to
			// catch up on any notifications we may have missed while the
			// subscription was gone.
			ec.Bridge.Log.Warn().
				Str("subscription_id", item.SubscriptionID).
				Msg("Graph lifecycle: subscriptionRemoved — recreating subscription and reconciling delta")
			go func() {
				ec.ensureGraphSubscription(ctx)
				ec.reconcile(ctx)
			}()

		case "missed":
			// Graph tells us it could not deliver some notifications — run a
			// delta reconcile to catch up on what we missed.
			ec.Bridge.Log.Warn().
				Str("subscription_id", item.SubscriptionID).
				Msg("Graph lifecycle: missed notifications — running delta reconcile")
			go ec.reconcile(ctx)

		default:
			ec.Bridge.Log.Warn().
				Str("lifecycle_event", item.LifecycleEvent).
				Str("subscription_id", item.SubscriptionID).
				Msg("Graph lifecycle: unrecognised lifecycleEvent — ignoring")
		}
	}

	return true
}

// handleGraphWebhookFull is the unified webhook+lifecycle handler registered on
// POST /_email/graph/webhook. It handles both the subscription-validation
// handshake, lifecycle events (reauthorizationRequired, subscriptionRemoved,
// missed), and normal change notifications.
func (ec *EmailConnector) handleGraphWebhookFull(w http.ResponseWriter, r *http.Request) {
	// Validation handshake: Graph sends ?validationToken= when creating/renewing.
	if graph.ValidationResponse(w, r) {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		ec.Bridge.Log.Error().Err(err).Msg("Graph webhook: failed to read body")
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// Respond 202 immediately — Graph requires a response within 3s.
	// All processing is async (queue / goroutine).
	w.WriteHeader(http.StatusAccepted)

	// Dispatch lifecycle events (reauthorizationRequired, subscriptionRemoved, missed).
	if ec.handleLifecycleEvents(r.Context(), body) {
		return
	}

	// Normal change notification.
	n, err := graph.ParseNotifications(body)
	if err != nil {
		ec.Bridge.Log.Error().Err(err).Msg("Graph webhook: failed to parse notifications")
		return
	}

	cs := ec.expectedClientState()
	for _, item := range n.Value {
		if item.ClientState != cs {
			ec.Bridge.Log.Warn().
				Str("got", item.ClientState).
				Msg("Graph webhook: clientState mismatch — ignoring item")
			continue
		}
		if item.ChangeType != "created" {
			continue
		}
		msgID := item.ResourceData.ID
		if msgID == "" {
			continue
		}
		select {
		case ec.webhookQueue <- webhookItem{messageID: msgID, changeType: item.ChangeType}:
		default:
			ec.Bridge.Log.Warn().Str("message_id", msgID).Msg("Graph webhook: queue full, dropping message")
		}
	}
}
