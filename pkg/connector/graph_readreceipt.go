package connector

import (
	"context"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
)

// HandleMatrixReadReceipt implements bridgev2.ReadReceiptHandlingNetworkAPI.
// When the Matrix user sends a read receipt for a bridged email, this method
// marks the corresponding message as read in Microsoft Graph (Outlook).
//
// Flow:
//  1. Resolve the event to a bridgev2 database message (via ExactMessage or DB lookup).
//  2. Strip the "email:" prefix to get the RFC-822 internetMessageId.
//  3. Suppress the internetMessageId so the resulting Graph webhook (Task 3)
//     does not echo the read back to Matrix.
//  4. Resolve the internetMessageId to a Graph internal (immutable) id.
//  5. PATCH the message as isRead=true.
//  6. On any failure after suppression, Forget the entry so the webhook can
//     still handle the read if it arrives.
func (ec *EmailClient) HandleMatrixReadReceipt(ctx context.Context, rcpt *bridgev2.MatrixReadReceipt) error {
	msg := rcpt.ExactMessage
	if msg == nil {
		var err error
		msg, err = ec.Main.Bridge.DB.Message.GetPartByMXID(ctx, rcpt.EventID)
		if err != nil || msg == nil {
			// Not a bridged message; nothing to do.
			return nil
		}
	}

	// Our dedup IDs are stored as "email:<internetMessageID>".
	internetID := strings.TrimPrefix(string(msg.ID), "email:")
	if internetID == string(msg.ID) {
		// Unexpected format — not one of ours; skip silently.
		return nil
	}

	// Record suppression before the PATCH so we don't race the webhook.
	ec.Main.suppress.Suppress(internetID)

	graphID, err := ec.Main.graphClient.FindGraphIDByInternetID(ctx, internetID)
	if err != nil || graphID == "" {
		ec.Main.suppress.Forget(internetID)
		return err
	}

	if err := ec.Main.graphClient.SetRead(ctx, graphID, true); err != nil {
		// PATCH failed — let the Graph webhook handle the read state instead.
		ec.Main.suppress.Forget(internetID)
		return err
	}

	return nil
}
