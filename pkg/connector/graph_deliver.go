package connector

import (
	"context"

	"maunium.net/go/mautrix/bridgev2"
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
