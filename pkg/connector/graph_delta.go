package connector

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

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

// maxDeltaPages is the upper bound on pages followed in a single backfill or
// reconcile walk. Graph delta links are forward-only, so an infinite loop here
// would stall the goroutine permanently. 1000 pages × 50 msgs/page = 50 000
// messages — generous for any real inbox.
const maxDeltaPages = 1000

// runBackfill walks Graph delta pages from an empty token (initial backfill),
// delivers each message via deliverGraphMessage, and stores the final deltaLink
// in the DB so future reconcile calls can catch up incrementally.
//
// Bounded by Graph's own pagination — each page honours the maxpagesize=50
// Prefer header set in DeltaPage. A full inbox backfill may span many pages;
// messages are delivered as each page arrives so the caller stays responsive.
//
// Safety: if the email client is not ready (nil), the deltaLink is NOT
// persisted — the next reconcile will re-run a full backfill once the client
// is available, preventing silent mail loss.
func (ec *EmailConnector) runBackfill(ctx context.Context) {
	if ec.graphClient == nil {
		return
	}
	log := ec.Bridge.Log.With().Str("component", "graph_delta").Logger()
	log.Info().Msg("Graph delta: starting inbox backfill")

	client, err := ec.lookupEmailClient()
	if err != nil {
		// Client not ready — do NOT persist the deltaLink. Graph delta links are
		// forward-only: persisting while delivery is skipped causes permanent mail
		// loss on the next reconcile. Log a WARN and return; the next trigger will
		// re-run a full backfill once the client is available.
		log.Warn().Err(err).Msg("Graph backfill: email client not ready — not persisting deltaLink, will retry on next trigger")
		return
	}

	ownerMXID := ec.Config.OAuth2.OwnerMXID
	email := ec.Config.OAuth2.AutoLoginEmail

	var url string // empty = initial endpoint
	total := 0
	pages := 0

	for {
		// Fix 2: bounded page walk — guard against Graph returning the same
		// nextLink repeatedly (infinite loop / stuck goroutine).
		if pages >= maxDeltaPages {
			log.Error().Int("pages", pages).Msg("Graph delta: backfill exceeded max page limit — aborting to prevent stuck goroutine")
			return
		}

		msgs, removed, nextLink, deltaLink, err := ec.graphClient.DeltaPage(ctx, url)
		if err != nil {
			log.Error().Err(err).Msg("Graph delta: backfill DeltaPage failed")
			return
		}
		pages++

		log.Debug().
			Int("msgs", len(msgs)).
			Int("removed", len(removed)).
			Str("next_link_set", boolStr(nextLink != "")).
			Str("delta_link_set", boolStr(deltaLink != "")).
			Msg("Graph delta: backfill page received")

		for _, msg := range msgs {
			client.deliverGraphMessage(ctx, msg)
			if msg.IsRead {
				client.deliverReadReceipt(ctx, msg)
			}
		}
		// Reflect messages that left the inbox (archive → Flow 5, delete → Flow 7).
		ec.handleRemovals(ctx, client, removed)
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

		// Fix 2: detect same-URL repeat before following, which would loop forever.
		if nextLink == url {
			log.Error().Str("url", url).Msg("Graph delta: backfill nextLink equals current URL — aborting to prevent infinite loop")
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
		// Client not ready — do NOT persist a new deltaLink. Advancing the
		// delta cursor while skipping delivery causes permanent mail loss.
		// The next trigger will retry from the same stored link.
		log.Warn().Err(clientErr).Msg("Graph delta: email client not ready — not persisting deltaLink, will retry on next trigger")
		return
	}

	url := gs.InboxDeltaLink
	total := 0
	pages := 0

	for {
		// Fix 2: bounded page walk — guard against Graph returning the same
		// nextLink repeatedly (infinite loop / stuck goroutine).
		if pages >= maxDeltaPages {
			log.Error().Int("pages", pages).Msg("Graph delta: reconcile exceeded max page limit — aborting to prevent stuck goroutine")
			return
		}

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
		pages++

		log.Debug().
			Int("msgs", len(msgs)).
			Int("removed", len(removed)).
			Str("next_link_set", boolStr(nextLink != "")).
			Str("delta_link_set", boolStr(deltaLink != "")).
			Msg("Graph delta: reconcile page received")

		for _, msg := range msgs {
			client.deliverGraphMessage(ctx, msg)
			if msg.IsRead {
				client.deliverReadReceipt(ctx, msg)
			}
		}
		// Reflect messages that left the inbox (archive → Flow 5, delete → Flow 7).
		ec.handleRemovals(ctx, client, removed)
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

		// Fix 2: detect same-URL repeat before following, which would loop forever.
		if nextLink == url {
			log.Error().Str("url", url).Msg("Graph delta: reconcile nextLink equals current URL — aborting to prevent infinite loop")
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

const reconcileInterval = 5 * time.Minute

var reconcileRunning atomic.Bool

// reconcileLoop runs an incremental delta reconcile on a fixed cadence. The
// webhook path delivers NEW inbox mail, but messages that LEAVE the inbox
// (archived/deleted in Outlook → Flow 5/7) are only visible as @removed ids in a
// delta page — and reconcile() is otherwise called only on rare lifecycle events
// (subscriptionRemoved/missed). Without this loop, Outlook archive/delete would
// never reflect into Beeper. The delta is incremental (from the saved deltaLink),
// so each pass is cheap; deliverGraphMessage dedups so no double delivery.
func (ec *EmailConnector) reconcileLoop(ctx context.Context) {
	ticker := time.NewTicker(reconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !reconcileRunning.CompareAndSwap(false, true) {
				continue // previous reconcile still running; skip this tick
			}
			ec.reconcile(ctx)
			reconcileRunning.Store(false)
		}
	}
}
