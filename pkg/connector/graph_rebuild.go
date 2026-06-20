package connector

import (
	"context"
	"os"
	"time"

	"maunium.net/go/mautrix/bridgev2/networkid"
)

// forceRebuildInbox performs a one-shot clean rebuild of the current Outlook
// inbox in Beeper. It deletes the Matrix rooms + bridge mappings for every
// message currently in the inbox, then re-backfills them from Graph. The result
// is fresh megolm sessions shared to ALL of the user's current devices, so
// messages that became undecryptable on a specific client (e.g. after a device
// list change redacted their session) render again — without duplicates, since
// the old rooms are deleted before re-creation. Non-inbox threads are untouched.
//
// One-shot recovery hook gated by EMAILDAWG_REBUILD_INBOX=1: set it, redeploy,
// verify, then UNSET it (otherwise it rebuilds on every boot).
func (ec *EmailConnector) forceRebuildInbox(ctx context.Context) {
	if os.Getenv("EMAILDAWG_REBUILD_INBOX") != "1" || ec.graphClient == nil {
		return
	}
	log := ec.Bridge.Log.With().Str("component", "graph_rebuild").Logger()
	client, err := ec.lookupEmailClient()
	if err != nil {
		log.Warn().Err(err).Msg("rebuild-inbox: email client not ready; skipping")
		return
	}
	loginID := client.UserLogin.ID

	refs, err := ec.graphClient.ListRecentInbox(ctx, 60)
	if err != nil {
		log.Warn().Err(err).Msg("rebuild-inbox: ListRecentInbox failed; skipping")
		return
	}

	// Collect the distinct portals to delete and the message mappings to clear.
	portals := map[networkid.PortalKey]bool{}
	msgIDs := []networkid.MessageID{}
	for _, ref := range refs {
		if ref.InternetMessageID == "" {
			continue
		}
		mid := networkid.MessageID("email:" + ref.InternetMessageID)
		part, perr := ec.Bridge.DB.Message.GetLastPartByID(ctx, loginID, mid)
		if perr != nil || part == nil {
			continue
		}
		portals[part.Room] = true
		msgIDs = append(msgIDs, mid)
	}
	log.Info().Int("inbox_refs", len(refs)).Int("portals", len(portals)).
		Int("mappings", len(msgIDs)).Msg("rebuild-inbox: starting clean rebuild")

	// Delete the Matrix rooms (removes them from Beeper → no duplicates) and the
	// bridge portal rows.
	deletedRooms := 0
	for pk := range portals {
		portal, gerr := ec.Bridge.GetExistingPortalByKey(ctx, pk)
		if gerr != nil || portal == nil {
			continue
		}
		if portal.MXID != "" {
			if derr := ec.Bridge.Bot.DeleteRoom(ctx, portal.MXID, false); derr != nil {
				log.Warn().Err(derr).Str("portal", string(pk.ID)).Msg("rebuild-inbox: DeleteRoom failed")
			} else {
				deletedRooms++
			}
		}
		if derr := portal.Delete(ctx); derr != nil {
			log.Warn().Err(derr).Str("portal", string(pk.ID)).Msg("rebuild-inbox: portal.Delete failed")
		}
	}

	// Clear any remaining message mappings so the re-backfill is not deduped.
	for _, mid := range msgIDs {
		if derr := ec.Bridge.DB.Message.DeleteAllParts(ctx, loginID, mid); derr != nil {
			log.Warn().Err(derr).Str("message_id", string(mid)).Msg("rebuild-inbox: DeleteAllParts failed")
		}
	}
	log.Info().Int("deleted_rooms", deletedRooms).Msg("rebuild-inbox: deletions done; settling before re-backfill")

	// Let Beeper process the room deletions before re-creating.
	select {
	case <-ctx.Done():
		return
	case <-time.After(8 * time.Second):
	}

	log.Info().Msg("rebuild-inbox: running fresh backfill")
	ec.runBackfill(ctx)
	log.Info().Msg("rebuild-inbox: complete (UNSET EMAILDAWG_REBUILD_INBOX to avoid rebuilding on next boot)")
}
