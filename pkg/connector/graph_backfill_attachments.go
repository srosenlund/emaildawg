package connector

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	// defaultBackfillAttachmentDays is the look-back window when
	// EMAILDAWG_BACKFILL_ATTACHMENT_DAYS is unset.
	defaultBackfillAttachmentDays = 30
	// backfillAttachmentListTop is the page size for the Graph inbox enumeration.
	backfillAttachmentListTop = 50
)

// backfillAttachments is a one-shot recovery hook that re-delivers attachments
// for mails that were bridged BEFORE attachment support existed (A0-A2). Those
// mails have a healthy, visible text event in the room but no media; a plain
// re-delivery would duplicate the text (bridgev2's DeleteAllParts only clears the
// DB mapping, it does NOT redact the old event). So instead, for each
// HasAttachments mail in the window we deliver a SEPARATE bilag-message
// ("📎 Bilag til tidligere mail: <subject>" + media parts) into the same thread,
// keyed on a distinct deterministic MessageID ("email-att:<imi>") so it is both
// non-colliding with the text and idempotent across re-runs.
//
// Gated by EMAILDAWG_BACKFILL_ATTACHMENTS=1 (mirrors the forceRebuildInbox
// one-shot pattern): set it, redeploy, observe, then UNSET it. Window is
// configurable via EMAILDAWG_BACKFILL_ATTACHMENT_DAYS (default 30).
func (ec *EmailConnector) backfillAttachments(ctx context.Context) {
	if os.Getenv("EMAILDAWG_BACKFILL_ATTACHMENTS") != "1" || ec.graphClient == nil {
		return
	}
	log := ec.Bridge.Log.With().Str("component", "graph_backfill_attachments").Logger()

	days := defaultBackfillAttachmentDays
	if raw := strings.TrimSpace(os.Getenv("EMAILDAWG_BACKFILL_ATTACHMENT_DAYS")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			days = n
		} else {
			log.Warn().Str("value", raw).Msg("attachment-backfill: invalid EMAILDAWG_BACKFILL_ATTACHMENT_DAYS; using default")
		}
	}
	since := time.Now().AddDate(0, 0, -days)

	client, err := ec.lookupEmailClient()
	if err != nil {
		log.Warn().Err(err).Msg("attachment-backfill: email client not ready; skipping")
		return
	}

	refs, err := ec.graphClient.ListAttachmentMessagesSince(ctx, since, backfillAttachmentListTop)
	if err != nil {
		log.Warn().Err(err).Msg("attachment-backfill: ListAttachmentMessagesSince failed; skipping")
		return
	}
	log.Info().Int("candidates", len(refs)).Int("window_days", days).
		Msg("attachment-backfill: starting bilag re-delivery for historical mails")

	delivered := 0
	for _, ref := range refs {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if ref.InternetMessageID == "" {
			continue
		}
		g, err := ec.graphClient.GetMessage(ctx, ref.GraphID)
		if err != nil {
			log.Warn().Err(err).Str("imi", ref.InternetMessageID).Msg("attachment-backfill: GetMessage failed; skipping")
			continue
		}
		if !g.HasAttachments {
			continue // filter drift; nothing to do
		}
		client.deliverAttachmentBackfill(ctx, g)
		delivered++
		log.Info().Str("imi", ref.InternetMessageID).Str("subject", g.Subject).
			Msg("attachment-backfill: queued bilag message")
	}
	log.Info().Int("queued", delivered).Int("scanned", len(refs)).
		Msg("attachment-backfill: pass complete (UNSET EMAILDAWG_BACKFILL_ATTACHMENTS to avoid re-running on next boot)")
}
