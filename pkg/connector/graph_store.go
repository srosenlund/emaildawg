package connector

import (
	"context"
	"fmt"
	"time"
)

// GraphState holds the Microsoft Graph subscription and delta-sync state for a
// single (user_mxid, email) pair. It is persisted in the graph_state table.
type GraphState struct {
	UserMXID           string
	Email              string
	SubscriptionID     string
	SubscriptionExpiry time.Time
	// ClientState is stored AES-GCM encrypted (same helper as email passwords).
	ClientState    string
	InboxDeltaLink string
}

// GetGraphState returns the stored GraphState for (userMXID, email), or
// (nil, nil) when no row exists.
func (eaq *EmailAccountQuery) GetGraphState(ctx context.Context, userMXID, email string) (*GraphState, error) {
	rows, err := eaq.DB.Query(ctx, `
		SELECT user_mxid, email, subscription_id, subscription_expiry, client_state, inbox_delta_link
		FROM graph_state
		WHERE user_mxid = ? AND email = ?
	`, userMXID, email)
	if err != nil {
		return nil, fmt.Errorf("graph_state query: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("graph_state rows: %w", err)
		}
		return nil, nil // no row found
	}

	gs := &GraphState{}
	var encClientState string
	var expiryStr string
	err = rows.Scan(
		&gs.UserMXID,
		&gs.Email,
		&gs.SubscriptionID,
		&expiryStr,
		&encClientState,
		&gs.InboxDeltaLink,
	)
	if err != nil {
		return nil, fmt.Errorf("graph_state scan: %w", err)
	}

	// Parse the expiry timestamp stored as RFC3339 text.
	gs.SubscriptionExpiry, err = time.Parse(time.RFC3339, expiryStr)
	if err != nil {
		return nil, fmt.Errorf("graph_state parse expiry %q: %w", expiryStr, err)
	}

	// Decrypt client_state.
	gs.ClientState, err = decryptString(encClientState)
	if err != nil {
		return nil, fmt.Errorf("graph_state decrypt client_state: %w", err)
	}

	return gs, nil
}

// UpsertGraphState inserts or replaces the graph_state row for (gs.UserMXID, gs.Email).
func (eaq *EmailAccountQuery) UpsertGraphState(ctx context.Context, gs *GraphState) error {
	encClientState, err := encryptString(gs.ClientState)
	if err != nil {
		return fmt.Errorf("graph_state encrypt client_state: %w", err)
	}

	expiryStr := gs.SubscriptionExpiry.UTC().Format(time.RFC3339)

	_, err = eaq.DB.Exec(ctx, `
		INSERT OR REPLACE INTO graph_state
		(user_mxid, email, subscription_id, subscription_expiry, client_state, inbox_delta_link)
		VALUES (?, ?, ?, ?, ?, ?)
	`, gs.UserMXID, gs.Email, gs.SubscriptionID, expiryStr, encClientState, gs.InboxDeltaLink)
	if err != nil {
		return fmt.Errorf("graph_state upsert: %w", err)
	}
	return nil
}
