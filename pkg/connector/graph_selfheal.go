package connector

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2/matrix"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/id"
)

const (
	selfHealInterval   = 30 * time.Minute
	selfHealFirstDelay = 3 * time.Minute
	selfHealScanTop    = 40
)

// decideHeal decides whether a recent inbox message must be re-delivered. It is
// intentionally conservative — it heals ONLY when a bridge DB part exists AND
// the message is either marked never-sent (fake MXID) or its event is CONFIRMED
// missing from the room (Matrix M_NOT_FOUND). Any uncertainty (no part yet, a
// transient lookup error) returns false, so a healthy message is never
// re-delivered (which would duplicate it in Beeper).
func decideHeal(partExists, isFake, confirmedMissing bool) bool {
	if !partExists {
		return false
	}
	return isFake || confirmedMissing
}

// selfHealLoop periodically audits recent inbox messages and re-delivers any
// whose Matrix event is missing — the orphan class where bridgev2 has a DB
// mapping (so dedup blocks re-delivery) but the event never landed in the room.
func (ec *EmailConnector) selfHealLoop(ctx context.Context) {
	if strings.EqualFold(os.Getenv("EMAILDAWG_SELFHEAL"), "off") {
		return
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(selfHealFirstDelay):
	}
	ec.runSelfHeal(ctx)
	ticker := time.NewTicker(selfHealInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ec.runSelfHeal(ctx)
		}
	}
}

// eventConfirmedMissing reports true only when Matrix definitively says the
// event does not exist (M_NOT_FOUND). On any other outcome (event present, no
// way to check, transient error) it returns false so we do not re-deliver.
func (ec *EmailConnector) eventConfirmedMissing(ctx context.Context, roomID id.RoomID, eventID id.EventID) bool {
	asIntent, ok := ec.Bridge.Bot.(*matrix.ASIntent)
	if !ok || asIntent.Matrix == nil {
		return false
	}
	_, err := asIntent.Matrix.GetEvent(ctx, roomID, eventID)
	return errors.Is(err, mautrix.MNotFound)
}

func (ec *EmailConnector) runSelfHeal(ctx context.Context) {
	if ec.graphClient == nil {
		return
	}
	log := ec.Bridge.Log.With().Str("component", "graph_selfheal").Logger()
	client, err := ec.lookupEmailClient()
	if err != nil {
		return // client not ready; try next tick
	}
	loginID := client.UserLogin.ID

	refs, err := ec.graphClient.ListRecentInbox(ctx, selfHealScanTop)
	if err != nil {
		log.Warn().Err(err).Msg("self-heal: ListRecentInbox failed")
		return
	}

	healed := 0
	for _, ref := range refs {
		if ref.InternetMessageID == "" {
			continue
		}
		msgID := networkid.MessageID("email:" + ref.InternetMessageID)
		part, err := ec.Bridge.DB.Message.GetLastPartByID(ctx, loginID, msgID)
		if err != nil {
			log.Warn().Err(err).Str("imi", ref.InternetMessageID).Msg("self-heal: GetLastPartByID failed")
			continue
		}
		if part == nil {
			continue // not delivered yet — normal delivery/backfill owns this
		}

		isFake := part.HasFakeMXID()
		confirmedMissing := false
		if !isFake {
			portal, perr := ec.Bridge.GetExistingPortalByKey(ctx, part.Room)
			if perr != nil || portal == nil || portal.MXID == "" {
				continue // can't verify room → assume healthy
			}
			confirmedMissing = ec.eventConfirmedMissing(ctx, portal.MXID, part.MXID)
		}
		if !decideHeal(true, isFake, confirmedMissing) {
			continue
		}

		// Orphan: clear the stale mapping and re-deliver.
		if err := ec.Bridge.DB.Message.DeleteAllParts(ctx, loginID, msgID); err != nil {
			log.Warn().Err(err).Str("imi", ref.InternetMessageID).Msg("self-heal: DeleteAllParts failed")
			continue
		}
		g, err := ec.graphClient.GetMessage(ctx, ref.GraphID)
		if err != nil {
			log.Warn().Err(err).Str("imi", ref.InternetMessageID).Msg("self-heal: GetMessage failed after clearing mapping")
			continue
		}
		client.deliverGraphMessage(ctx, g)
		if g.IsRead {
			client.deliverReadReceipt(ctx, g)
		}
		healed++
		log.Info().Str("imi", ref.InternetMessageID).Bool("fake_mxid", isFake).
			Str("subject", g.Subject).Msg("self-heal: re-delivered orphaned message")
	}
	log.Info().Int("healed", healed).Int("scanned", len(refs)).Msg("self-heal: pass complete")
}
