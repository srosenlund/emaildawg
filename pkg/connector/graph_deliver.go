package connector

import (
	"context"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"

	"github.com/iFixRobots/emaildawg/pkg/graph"
)

// deliverGraphMessage enqueues a parsed Microsoft Graph email message into the
// bridgev2 event pipeline.  The framework handles portal/room creation
// (CreatePortal:true), ghost creation, and message-id↔MXID persistence.
func (ec *EmailClient) deliverGraphMessage(ctx context.Context, g *graph.GraphMessage) {
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
			body := d.Subject
			if d.BodyText != "" {
				body = d.Subject + "\n\n" + d.BodyText
			}
			return &bridgev2.ConvertedMessage{
				Parts: []*bridgev2.ConvertedMessagePart{
					{
						Type: event.EventMessage,
						Content: &event.MessageEventContent{
							MsgType: event.MsgText,
							Body:    body,
						},
					},
				},
			}, nil
		},
	}

	if res := ec.UserLogin.QueueRemoteEvent(evt); !res.Success {
		ec.UserLogin.Log.Error().Msg("queue graph message failed")
	}
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
