package connector

import (
	"context"
	"errors"
	"fmt"

	"maunium.net/go/mautrix/bridgev2/networkid"

	"github.com/iFixRobots/emaildawg/pkg/graph"
)

// lookupEmailClient finds the EmailClient for the auto-login mailbox via the
// bridge's cached user login registry. Returns an error if the login is not
// found or is not an *EmailClient.
func (ec *EmailConnector) lookupEmailClient() (*EmailClient, error) {
	loginID := networkid.UserLoginID(fmt.Sprintf("email:%s", ec.Config.OAuth2.AutoLoginEmail))
	login := ec.Bridge.GetCachedUserLoginByID(loginID)
	if login == nil {
		return nil, fmt.Errorf("graph delta: auto-login UserLogin %q not found (bridge may still be starting)", loginID)
	}
	client, ok := login.Client.(*EmailClient)
	if !ok {
		return nil, fmt.Errorf("graph delta: UserLogin client is not an *EmailClient")
	}
	return client, nil
}

// runBackfill walks Graph delta pages from an empty token (initial backfill),
// delivers each message via deliverGraphMessage, and stores the final deltaLink
// in the DB so future reconcile calls can catch up incrementally.
//
// Bounded by Graph's own pagination — each page honours the maxpagesize=50
// Prefer header set in DeltaPage. A full inbox backfill may span many pages;
// messages are delivered as each page arrives so the caller stays responsive.
func (ec *EmailConnector) runBackfill(ctx context.Context) {
	if ec.graphClient == nil {
		return
	}
	log := ec.Bridge.Log.With().Str("component", "graph_delta").Logger()
	log.Info().Msg("Graph delta: starting inbox backfill")

	client, err := ec.lookupEmailClient()
	if err != nil {
		log.Warn().Err(err).Msg("Graph delta: cannot deliver during backfill (client not ready)")
		// Continue without delivery — we still want to record the deltaLink so
		// reconcile can catch up from where we left off.
		client = nil
	}

	ownerMXID := ec.Config.OAuth2.OwnerMXID
	email := ec.Config.OAuth2.AutoLoginEmail

	var url string // empty = initial endpoint
	total := 0

	for {
		msgs, removed, nextLink, deltaLink, err := ec.graphClient.DeltaPage(ctx, url)
		if err != nil {
			log.Error().Err(err).Msg("Graph delta: backfill DeltaPage failed")
			return
		}

		log.Debug().
			Int("msgs", len(msgs)).
			Int("removed", len(removed)).
			Str("next_link_set", boolStr(nextLink != "")).
			Str("delta_link_set", boolStr(deltaLink != "")).
			Msg("Graph delta: backfill page received")

		if client != nil {
			for _, msg := range msgs {
				client.deliverGraphMessage(ctx, msg)
			}
		}
		total += len(msgs)

		if deltaLink != "" {
			// Round done — persist the deltaLink for incremental reconcile.
			gs, dbErr := ec.DB.GetGraphState(ctx, ownerMXID, email)
			if dbErr != nil {
				log.Warn().Err(dbErr).Msg("Graph delta: backfill could not load GraphState to save deltaLink")
				return
			}
			if gs == nil {
				log.Warn().Msg("Graph delta: backfill no GraphState row to save deltaLink into — skipping persist")
				return
			}
			gs.InboxDeltaLink = deltaLink
			if upsertErr := ec.DB.UpsertGraphState(ctx, gs); upsertErr != nil {
				log.Warn().Err(upsertErr).Msg("Graph delta: backfill failed to persist deltaLink")
			} else {
				log.Info().Int("total_messages", total).Msg("Graph delta: backfill complete, deltaLink saved")
			}
			return
		}

		if nextLink == "" {
			// Neither nextLink nor deltaLink — Graph returned an incomplete response.
			log.Warn().Msg("Graph delta: backfill page had neither nextLink nor deltaLink — aborting")
			return
		}

		// Follow nextLink verbatim for the next page.
		url = nextLink
	}
}

// reconcile catches up on missed change notifications by fetching the saved
// deltaLink from the DB and processing any new/changed messages since the last
// round. On ErrDeltaResync (token expired / 410 Gone) it re-baselines via a
// full runBackfill.
func (ec *EmailConnector) reconcile(ctx context.Context) {
	if ec.graphClient == nil {
		return
	}
	log := ec.Bridge.Log.With().Str("component", "graph_delta").Logger()

	ownerMXID := ec.Config.OAuth2.OwnerMXID
	email := ec.Config.OAuth2.AutoLoginEmail

	gs, err := ec.DB.GetGraphState(ctx, ownerMXID, email)
	if err != nil {
		log.Error().Err(err).Msg("Graph delta: reconcile could not load GraphState")
		return
	}
	if gs == nil || gs.InboxDeltaLink == "" {
		log.Info().Msg("Graph delta: no deltaLink stored — running full backfill")
		ec.runBackfill(ctx)
		return
	}

	log.Info().Msg("Graph delta: reconciling from stored deltaLink")

	client, clientErr := ec.lookupEmailClient()
	if clientErr != nil {
		log.Warn().Err(clientErr).Msg("Graph delta: cannot deliver during reconcile (client not ready)")
		client = nil
	}

	url := gs.InboxDeltaLink
	total := 0

	for {
		msgs, removed, nextLink, deltaLink, err := ec.graphClient.DeltaPage(ctx, url)
		if err != nil {
			if errors.Is(err, graph.ErrDeltaResync) {
				log.Warn().Msg("Graph delta: token expired (410/syncStateNotFound) — re-baselining with full backfill")
				// Clear stale deltaLink before re-baselining so runBackfill
				// can store a fresh one in the same row.
				gs.InboxDeltaLink = ""
				if upsertErr := ec.DB.UpsertGraphState(ctx, gs); upsertErr != nil {
					log.Warn().Err(upsertErr).Msg("Graph delta: failed to clear stale deltaLink before re-baseline")
				}
				ec.runBackfill(ctx)
				return
			}
			log.Error().Err(err).Msg("Graph delta: reconcile DeltaPage failed")
			return
		}

		log.Debug().
			Int("msgs", len(msgs)).
			Int("removed", len(removed)).
			Str("next_link_set", boolStr(nextLink != "")).
			Str("delta_link_set", boolStr(deltaLink != "")).
			Msg("Graph delta: reconcile page received")

		if client != nil {
			for _, msg := range msgs {
				client.deliverGraphMessage(ctx, msg)
			}
		}
		total += len(msgs)

		if deltaLink != "" {
			// This reconcile round is done — persist the new deltaLink.
			gs.InboxDeltaLink = deltaLink
			if upsertErr := ec.DB.UpsertGraphState(ctx, gs); upsertErr != nil {
				log.Warn().Err(upsertErr).Msg("Graph delta: reconcile failed to persist new deltaLink")
			} else {
				log.Info().Int("total_messages", total).Msg("Graph delta: reconcile complete, new deltaLink saved")
			}
			return
		}

		if nextLink == "" {
			log.Warn().Msg("Graph delta: reconcile page had neither nextLink nor deltaLink — aborting")
			return
		}

		url = nextLink
	}
}

// boolStr is a small helper that converts a bool to "true"/"false" for structured log fields.
func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
