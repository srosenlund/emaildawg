package connector

import (
	htmlpkg "html"
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"

	"github.com/iFixRobots/emaildawg/pkg/email"
	"github.com/iFixRobots/emaildawg/pkg/graph"
)

// maxAttachmentParts bounds how many media parts a single message may emit, so a
// mail with very many small attachments cannot blow up the Matrix event. Files
// beyond the cap are summarised in a single "+N flere bilag" notice.
const maxAttachmentParts = 20

// deliverGraphMessage enqueues a parsed Microsoft Graph email message into the
// bridgev2 event pipeline.  The framework handles portal/room creation
// (CreatePortal:true), ghost creation, and message-id↔MXID persistence.
func (ec *EmailClient) deliverGraphMessage(ctx context.Context, g *graph.GraphMessage) {
	// Lazily fetch attachments off the slim delta path: only when the message
	// actually has them. On fetch failure we log and continue — the text part
	// must still be delivered, never drop the whole message.
	itemCount := 0
	if g.HasAttachments {
		if ec.Main != nil && ec.Main.graphClient != nil {
			atts, n, err := ec.Main.graphClient.FetchAttachments(ctx, ec.Email, g.ID)
			if err != nil {
				ec.UserLogin.Log.Warn().Err(err).Str("message_id", g.ID).Msg("Graph FetchAttachments failed; delivering text only")
			} else {
				g.Attachments = atts
				itemCount = n
			}
		} else {
			ec.UserLogin.Log.Warn().Str("message_id", g.ID).Msg("Graph FetchAttachments skipped; no graphClient")
		}
	}

	maxUpload := int64(DefaultMaxUploadBytes)
	if ec.Main != nil && ec.Main.Config.Processing.MaxUploadBytes != 0 {
		maxUpload = int64(ec.Main.Config.Processing.MaxUploadBytes)
	}

	evt := &simplevent.Message[*graph.GraphMessage]{
		EventMeta: simplevent.EventMeta{
			Type: bridgev2.RemoteEventMessage,
			PortalKey: networkid.PortalKey{
				ID:       networkid.PortalID("thread:" + g.ConversationID),
				Receiver: ec.UserLogin.ID,
			},
			Sender: bridgev2.EventSender{
				Sender: networkid.UserID("email:" + g.FromAddress),
			},
			CreatePortal: true,
			Timestamp:    g.ReceivedDateTime,
		},
		ID:   networkid.MessageID("email:" + g.InternetMessageID),
		Data: g,
		ConvertMessageFunc: func(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, d *graph.GraphMessage) (*bridgev2.ConvertedMessage, error) {
			return convertGraphMessage(ctx, intent, d, itemCount, maxUpload, ec.Main.Processor)
		},
	}

	if res := ec.UserLogin.QueueRemoteEvent(evt); !res.Success {
		ec.UserLogin.Log.Error().Msg("queue graph message failed")
	}
}

// deliverAttachmentBackfill enqueues a bilag-only message for a historical mail
// that was bridged before attachments were supported. It targets the SAME thread
// portal as the original mail but uses a distinct, deterministic MessageID
// ("email-att:<imi>") so:
//   - it is NOT deduped against the original text message (different ID), and
//   - re-running the backfill is idempotent (bridgev2 dedups the SAME att-id).
//
// The original text event is left untouched, so no duplicate text appears. On
// fetch failure or when there are no deliverable attachments at all, it logs and
// skips rather than posting an empty/garbage message.
func (ec *EmailClient) deliverAttachmentBackfill(ctx context.Context, g *graph.GraphMessage) {
	if !g.HasAttachments {
		return
	}
	itemCount := 0
	if ec.Main != nil && ec.Main.graphClient != nil {
		atts, n, err := ec.Main.graphClient.FetchAttachments(ctx, ec.Email, g.ID)
		if err != nil {
			ec.UserLogin.Log.Warn().Err(err).Str("message_id", g.ID).Msg("attachment-backfill: FetchAttachments failed; skipping")
			return
		}
		g.Attachments = atts
		itemCount = n
	} else {
		ec.UserLogin.Log.Warn().Str("message_id", g.ID).Msg("attachment-backfill: no graphClient; skipping")
		return
	}
	if len(g.Attachments) == 0 && itemCount == 0 {
		// hasAttachments was true but nothing deliverable came back (e.g. only
		// inline images already rendered, or a transient empty result): skip.
		return
	}

	maxUpload := int64(DefaultMaxUploadBytes)
	if ec.Main != nil && ec.Main.Config.Processing.MaxUploadBytes != 0 {
		maxUpload = int64(ec.Main.Config.Processing.MaxUploadBytes)
	}

	evt := &simplevent.Message[*graph.GraphMessage]{
		EventMeta: simplevent.EventMeta{
			Type: bridgev2.RemoteEventMessage,
			PortalKey: networkid.PortalKey{
				ID:       networkid.PortalID("thread:" + g.ConversationID),
				Receiver: ec.UserLogin.ID,
			},
			Sender: bridgev2.EventSender{
				Sender: networkid.UserID("email:" + g.FromAddress),
			},
			CreatePortal: true,
			Timestamp:    g.ReceivedDateTime,
		},
		// Distinct, deterministic ID → not deduped vs the text msg; idempotent on re-run.
		ID:   networkid.MessageID("email-att:" + g.InternetMessageID),
		Data: g,
		ConvertMessageFunc: func(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, d *graph.GraphMessage) (*bridgev2.ConvertedMessage, error) {
			return convertAttachmentBackfillMessage(ctx, intent, d, itemCount, maxUpload)
		},
	}

	if res := ec.UserLogin.QueueRemoteEvent(evt); !res.Success {
		ec.UserLogin.Log.Error().Str("message_id", g.ID).Msg("attachment-backfill: queue failed")
	}
}

// convertGraphMessage builds the bridgev2 ConvertedMessage for a Graph email:
// part 1 is the subject+body text, followed by one media part per file
// attachment (capped at maxAttachmentParts), an over-cap notice per too-large
// file (handled inside email.ConvertAttachmentPart), an item-attachment notice
// when itemCount>0, and a single "+N flere bilag" overflow notice when the file
// count exceeds the cap. PartIDs are deterministic ("att-<i>-<sanitized-navn>")
// so re-delivery/backfill is idempotent. Kept standalone (no portal/queue deps)
// so it is unit-testable with a mock intent.
func convertGraphMessage(ctx context.Context, intent bridgev2.MatrixAPI, g *graph.GraphMessage, itemCount int, maxUploadBytes int64, proc *email.Processor) (*bridgev2.ConvertedMessage, error) {
	// HTML-mails rendes gennem reader-pipelinen (hygiejne + bulk-pruning +
	// polish) til en pæn FormattedBody; plaintext-fallback bruges som Body.
	var formatted, plainBody string
	if g.BodyHTML != "" && proc != nil && proc.ReaderMode {
		extract := g.IsBulk && proc.ReaderModeExtract
		formatted, plainBody = email.RenderHTMLForMatrix(g.BodyHTML, proc.ReaderModeMinImgPx, extract)
	}
	if plainBody == "" {
		plainBody = g.BodyText
	}
	body := g.Subject
	if plainBody != "" {
		body = g.Subject + "\n\n" + plainBody
	}

	content := &event.MessageEventContent{
		MsgType: event.MsgText,
		Body:    body,
	}
	if formatted != "" {
		content.Format = event.FormatHTML
		content.FormattedBody = "<p><strong>" + htmlpkg.EscapeString(g.Subject) + "</strong></p>" + formatted
	}

	parts := []*bridgev2.ConvertedMessagePart{
		{
			Type:    event.EventMessage,
			Content: content,
		},
	}

	parts = appendAttachmentParts(ctx, intent, parts, g, itemCount, maxUploadBytes)
	return &bridgev2.ConvertedMessage{Parts: parts}, nil
}

// appendAttachmentParts appends, in order: one media part per file attachment
// (capped at maxAttachmentParts, an over-cap file becomes a notice inside
// email.ConvertAttachmentPart), a single "+N flere bilag" overflow notice when the
// file count exceeds the cap, and an item-attachment notice when itemCount>0.
// PartIDs are deterministic ("att-<i>-<sanitized-navn>") so re-delivery/backfill
// is idempotent. Shared by the live delivery (convertGraphMessage) and the
// attachment-backfill message (convertAttachmentBackfillMessage).
func appendAttachmentParts(
	ctx context.Context,
	intent bridgev2.MatrixAPI,
	parts []*bridgev2.ConvertedMessagePart,
	g *graph.GraphMessage,
	itemCount int,
	maxUploadBytes int64,
) []*bridgev2.ConvertedMessagePart {
	for i, att := range g.Attachments {
		if i >= maxAttachmentParts {
			break
		}
		partID := fmt.Sprintf("att-%d-%s", i+1, email.SanitizeFilename(att.Name))
		part, err := email.ConvertAttachmentPart(ctx, att, intent, partID, maxUploadBytes)
		if err != nil {
			// Upload failed for this one file: emit a notice, keep going.
			parts = append(parts, &bridgev2.ConvertedMessagePart{
				ID:   networkid.PartID(partID),
				Type: event.EventMessage,
				Content: &event.MessageEventContent{
					MsgType: event.MsgNotice,
					Body:    fmt.Sprintf("📎 kunne ikke sendes: %s", att.Name),
				},
			})
			continue
		}
		if part != nil {
			parts = append(parts, part)
		}
	}

	if overflow := len(g.Attachments) - maxAttachmentParts; overflow > 0 {
		parts = append(parts, &bridgev2.ConvertedMessagePart{
			ID:   networkid.PartID("att-overflow"),
			Type: event.EventMessage,
			Content: &event.MessageEventContent{
				MsgType: event.MsgNotice,
				Body:    fmt.Sprintf("+%d flere bilag", overflow),
			},
		})
	}

	if itemCount > 0 {
		parts = append(parts, &bridgev2.ConvertedMessagePart{
			ID:   networkid.PartID("att-item-notice"),
			Type: event.EventMessage,
			Content: &event.MessageEventContent{
				MsgType: event.MsgNotice,
				Body:    "[vedhæftet besked — ikke understøttet]",
			},
		})
	}

	return parts
}

// convertAttachmentBackfillMessage builds the bilag-only message used by the
// attachment backfill. It is delivered as a SEPARATE Matrix message in the same
// thread (distinct MessageID "email-att:<imi>"), NOT a re-delivery of the original
// mail — re-delivering would duplicate the already-visible text event (bridgev2's
// DeleteAllParts only clears the DB mapping, it does not redact the old event).
// Part 1 is a "📎 Bilag til tidligere mail: <subject>" header notice; the file
// media parts + notices follow via the shared appendAttachmentParts. No m.text
// part is emitted, so the original mail's body is never duplicated.
func convertAttachmentBackfillMessage(ctx context.Context, intent bridgev2.MatrixAPI, g *graph.GraphMessage, itemCount int, maxUploadBytes int64) (*bridgev2.ConvertedMessage, error) {
	parts := []*bridgev2.ConvertedMessagePart{
		{
			ID:   networkid.PartID("att-backfill-header"),
			Type: event.EventMessage,
			Content: &event.MessageEventContent{
				MsgType: event.MsgNotice,
				Body:    fmt.Sprintf("📎 Bilag til tidligere mail: %s", g.Subject),
			},
		},
	}
	parts = appendAttachmentParts(ctx, intent, parts, g, itemCount, maxUploadBytes)
	return &bridgev2.ConvertedMessage{Parts: parts}, nil
}

// readReceiptWaitTimeout bounds how long deliverReadReceipt waits for the
// asynchronous message mapping to appear before giving up. During backfill we
// deliver a message and its read receipt back-to-back; room creation and
// message persistence happen on the bridgev2 event loop, so the mapping may not
// exist for a short while.
const (
	readReceiptWaitTimeout  = 15 * time.Second
	readReceiptWaitInterval = 300 * time.Millisecond
)

// deliverReadReceipt marks the message g.InternetMessageID as read in the Matrix
// room for g.ConversationID, as the user's own double puppet. It is called by
// processWebhookItem when a Graph "updated" notification arrives for an
// already-read message that is NOT suppressed (the read came from Outlook), and
// by the backfill/reconcile walk to sync historical read state.
//
// We do NOT use the framework receipt event (QueueRemoteEvent + simplevent.Receipt):
// bridgev2's handleRemoteReadReceipt calls intent.MarkRead WITHOUT EnsureJoined,
// and on Beeper the double puppet joins rooms out-of-band (will_auto_accept), so
// during backfill the receipt outruns the join and fails with 403 "not in that
// room". Instead we wait (bounded) for the message mapping, force a synchronous
// join, and MarkRead directly — making the operation deterministic regardless of
// join timing. Without a double puppet (e.g. IMAP mode) we fall back to the
// framework path, which is the best available.
func (ec *EmailClient) deliverReadReceipt(ctx context.Context, g *graph.GraphMessage) {
	portalKey := networkid.PortalKey{
		ID:       networkid.PortalID("thread:" + g.ConversationID),
		Receiver: ec.UserLogin.ID,
	}
	msgID := networkid.MessageID("email:" + g.InternetMessageID)

	dp := ec.UserLogin.User.DoublePuppet(ctx)
	if dp == nil {
		ec.UserLogin.QueueRemoteEvent(&simplevent.Receipt{
			EventMeta: simplevent.EventMeta{
				Type:      bridgev2.RemoteEventReadReceipt,
				PortalKey: portalKey,
				Sender:    bridgev2.EventSender{IsFromMe: true, SenderLogin: ec.UserLogin.ID},
			},
			LastTarget: msgID,
			ReadUpTo:   time.Now(),
		})
		return
	}

	log := zerolog.Ctx(ctx)

	// Wait for the message mapping to be persisted (implies the room exists).
	var msg *database.Message
	pollUntil(ctx, readReceiptWaitTimeout, readReceiptWaitInterval, func() bool {
		m, err := ec.Main.Bridge.DB.Message.GetLastPartByID(ctx, ec.UserLogin.ID, msgID)
		if err != nil {
			log.Warn().Err(err).Str("message_id", string(msgID)).Msg("Graph read receipt: error loading message mapping")
			return false
		}
		if m != nil && !m.HasFakeMXID() {
			msg = m
			return true
		}
		return false
	})
	if msg == nil {
		log.Warn().Str("message_id", string(msgID)).Msg("Graph read receipt: message mapping not found within timeout; skipping")
		return
	}

	portal, err := ec.Main.Bridge.GetExistingPortalByKey(ctx, portalKey)
	if err != nil || portal == nil || portal.MXID == "" {
		log.Warn().Err(err).Str("message_id", string(msgID)).Msg("Graph read receipt: portal/room not ready; skipping")
		return
	}

	if err := dp.EnsureJoined(ctx, portal.MXID); err != nil {
		log.Warn().Err(err).Stringer("room", portal.MXID).Msg("Graph read receipt: double puppet EnsureJoined failed; skipping")
		return
	}
	if err := dp.MarkRead(ctx, portal.MXID, msg.MXID, time.Now()); err != nil {
		log.Warn().Err(err).Stringer("room", portal.MXID).Msg("Graph read receipt: MarkRead failed")
	}
}
