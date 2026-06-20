package connector

import (
	"context"
	"os"
	"strings"
	"time"
)

const (
	ensureJoinInterval   = 5 * time.Minute
	ensureJoinFirstDelay = 45 * time.Second
)

// ensureJoinedLoop periodically makes sure the user's double puppet is joined to
// every email portal room. On Beeper, the user's invite to a portal is normally
// auto-accepted (will_auto_accept) — but that fires reliably only for rooms
// created at startup/backfill, NOT for rooms created from live webhook deliveries.
// The result is that live new mail creates a room the user never joins, so it
// stays invisible in Beeper. This loop closes that gap non-destructively: it just
// joins the user to rooms they should already be in (idempotent, no room
// deletion/recreation). Disable with EMAILDAWG_JOINHEAL=off.
func (ec *EmailConnector) ensureJoinedLoop(ctx context.Context) {
	if strings.EqualFold(os.Getenv("EMAILDAWG_JOINHEAL"), "off") {
		return
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(ensureJoinFirstDelay):
	}
	ec.ensureAllPortalsJoined(ctx)
	ticker := time.NewTicker(ensureJoinInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ec.ensureAllPortalsJoined(ctx)
		}
	}
}

func (ec *EmailConnector) ensureAllPortalsJoined(ctx context.Context) {
	client, err := ec.lookupEmailClient()
	if err != nil {
		return // client not ready; next tick
	}
	dp := client.UserLogin.User.DoublePuppet(ctx)
	if dp == nil {
		return // no double puppet (IMAP mode); nothing to do
	}
	log := ec.Bridge.Log.With().Str("component", "graph_joinheal").Logger()
	userID := client.UserLogin.UserMXID
	portals, err := ec.Bridge.GetAllPortalsWithMXID(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("join-heal: failed to list portals")
		return
	}
	joined, failed := 0, 0
	for _, p := range portals {
		if p.MXID == "" {
			continue
		}
		// The double puppet joins via an m.room.member state PUT, which hungryserv
		// rejects with 403 unless the user is already invited. Live webhook rooms
		// are created without inviting the user, so the bot must invite first.
		if err := ec.Bridge.Bot.EnsureInvited(ctx, p.MXID, userID); err != nil {
			log.Debug().Err(err).Stringer("room", p.MXID).Msg("join-heal: EnsureInvited failed (continuing to join)")
		}
		if err := dp.EnsureJoined(ctx, p.MXID); err != nil {
			failed++
			continue
		}
		joined++
	}
	if failed > 0 || joined > 0 {
		log.Info().Int("portals", len(portals)).Int("ensured", joined).Int("failed", failed).
			Msg("join-heal: ensured user joined to email rooms")
	}
}
