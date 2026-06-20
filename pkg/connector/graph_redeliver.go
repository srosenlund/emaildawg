package connector

import (
	"context"
	"os"
	"strings"

	"maunium.net/go/mautrix/bridgev2/networkid"
)

// forceRedeliver re-delivers specific messages whose Matrix delivery is stuck:
// the bridgev2 DB has a message mapping (so the dedup in handleRemoteMessage
// ignores any re-delivery) but the message is not actually visible in the room
// — e.g. an encrypted send whose megolm session never reached the user's device
// ("no group session created"). For each internetMessageID listed in
// EMAILDAWG_FORCE_REDELIVER (comma-separated), it deletes the stale DB mapping,
// re-fetches the message from Graph, and re-delivers it (+ read receipt).
//
// One-shot recovery hook: set the env, redeploy, observe, then unset. Also a
// diagnostic — if the message appears after redelivery the original failure was
// transient; if it stays empty the encryption issue is persistent.
func (ec *EmailConnector) forceRedeliver(ctx context.Context) {
	raw := strings.TrimSpace(os.Getenv("EMAILDAWG_FORCE_REDELIVER"))
	if raw == "" || ec.graphClient == nil {
		return
	}
	log := ec.Bridge.Log.With().Str("component", "graph_redeliver").Logger()
	client, err := ec.lookupEmailClient()
	if err != nil {
		log.Warn().Err(err).Msg("force-redeliver: email client not ready; skipping")
		return
	}
	loginID := client.UserLogin.ID

	for _, imi := range strings.Split(raw, ",") {
		imi = strings.TrimSpace(imi)
		if imi == "" {
			continue
		}
		msgID := networkid.MessageID("email:" + imi)

		parts, err := ec.Bridge.DB.Message.GetAllPartsByID(ctx, loginID, msgID)
		if err != nil {
			log.Warn().Err(err).Str("imi", imi).Msg("force-redeliver: GetAllPartsByID failed")
			continue
		}
		if len(parts) > 0 {
			if err := ec.Bridge.DB.Message.DeleteAllParts(ctx, loginID, msgID); err != nil {
				log.Warn().Err(err).Str("imi", imi).Msg("force-redeliver: DeleteAllParts failed")
				continue
			}
			log.Info().Str("imi", imi).Int("deleted_parts", len(parts)).
				Msg("force-redeliver: cleared stale mapping")
		} else {
			log.Info().Str("imi", imi).Msg("force-redeliver: no existing mapping (delivering fresh)")
		}

		graphID, err := ec.graphClient.FindGraphIDByInternetID(ctx, imi)
		if err != nil || graphID == "" {
			log.Warn().Err(err).Str("imi", imi).Msg("force-redeliver: could not resolve Graph id; skipping")
			continue
		}
		g, err := ec.graphClient.GetMessage(ctx, graphID)
		if err != nil {
			log.Warn().Err(err).Str("imi", imi).Msg("force-redeliver: GetMessage failed; skipping")
			continue
		}
		client.deliverGraphMessage(ctx, g)
		if g.IsRead {
			client.deliverReadReceipt(ctx, g)
		}
		log.Info().Str("imi", imi).Str("subject", g.Subject).
			Msg("force-redeliver: re-delivered message")
	}
}
