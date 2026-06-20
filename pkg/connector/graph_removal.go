package connector

import (
	"context"
	"errors"
	"sync"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"

	"github.com/iFixRobots/emaildawg/pkg/graph"
)

// folderIDCache memoises the resolved immutable ids of the Archive and Deleted
// Items well-known folders for the mailbox. They are stable for the lifetime of
// the mailbox, so resolving them once per process is enough. Resolution is
// best-effort: if it fails, the ids stay empty and classifyRemoval degrades to
// RemovalUnknown (no destructive action) rather than guessing.
type folderIDCache struct {
	mu        sync.Mutex
	resolved  bool
	archiveID string
	deletedID string
}

var removalFolders folderIDCache

// resolveRemovalFolders resolves (once) the archive + deleteditems folder ids.
// Returns the cached values; either may be empty if resolution failed.
func (ec *EmailConnector) resolveRemovalFolders(ctx context.Context) (archiveID, deletedID string) {
	removalFolders.mu.Lock()
	defer removalFolders.mu.Unlock()
	if removalFolders.resolved {
		return removalFolders.archiveID, removalFolders.deletedID
	}
	log := ec.Bridge.Log.With().Str("component", "graph_removal").Logger()
	if id, err := ec.graphClient.ResolveWellKnownFolderID(ctx, "archive"); err != nil {
		log.Warn().Err(err).Msg("Graph removal: could not resolve Archive folder id (archive detection degraded)")
	} else {
		removalFolders.archiveID = id
	}
	if id, err := ec.graphClient.ResolveWellKnownFolderID(ctx, "deleteditems"); err != nil {
		log.Warn().Err(err).Msg("Graph removal: could not resolve Deleted Items folder id (delete detection degraded)")
	} else {
		removalFolders.deletedID = id
	}
	removalFolders.resolved = true
	return removalFolders.archiveID, removalFolders.deletedID
}

// handleRemovals processes the @removed graph-ids reported by a delta page.
// For each id it fetches the message to learn its current folder, classifies the
// removal as archive vs delete, and reflects it in Beeper (Flow 5 / Flow 7).
//
// Hard-deletes (GetMessage 404) cannot be mapped to a Beeper room because the
// conversationId is only obtainable from the message itself; those are logged
// and skipped as a known limitation (see report).
func (ec *EmailConnector) handleRemovals(ctx context.Context, client *EmailClient, removed []string) {
	if len(removed) == 0 {
		return
	}
	log := ec.Bridge.Log.With().Str("component", "graph_removal").Logger()
	archiveID, deletedID := ec.resolveRemovalFolders(ctx)

	for _, graphID := range removed {
		msg, err := ec.graphClient.GetMessage(ctx, graphID)
		notFound := errors.Is(err, graph.ErrMessageNotFound)
		if err != nil && !notFound {
			log.Warn().Err(err).Str("graph_id", graphID).
				Msg("Graph removal: GetMessage failed; skipping this removed item")
			continue
		}

		var parentFolderID, conversationID, internetID string
		if msg != nil {
			parentFolderID = msg.ParentFolderID
			conversationID = msg.ConversationID
			internetID = msg.InternetMessageID
		}

		kind := graph.ClassifyRemoval(parentFolderID, archiveID, deletedID, notFound)

		// Suppression: if we initiated this archive/delete from Beeper→Outlook
		// (Flow 4/6, not built yet) we'd have suppressed internetID. Drop the
		// echo so we don't re-apply it back onto Beeper.
		if internetID != "" && ec.suppress.IsSuppressed(internetID) {
			log.Debug().Str("graph_id", graphID).Str("internet_id", internetID).
				Msg("Graph removal: suppressed (echo of our own action)")
			continue
		}

		switch kind {
		case graph.RemovalArchive:
			if conversationID == "" {
				log.Warn().Str("graph_id", graphID).Msg("Graph removal: archive but no conversationId; skipping")
				continue
			}
			client.deliverArchive(ctx, conversationID, internetID)
		case graph.RemovalDelete:
			if conversationID == "" {
				// Hard delete (404) — no conversationId available, cannot map to a room.
				log.Warn().Str("graph_id", graphID).Bool("not_found", notFound).
					Msg("Graph removal: delete but no conversationId (hard delete?) — cannot map to room, skipping (known limitation)")
				continue
			}
			client.deliverDelete(ctx, conversationID, internetID)
		default:
			log.Info().Str("graph_id", graphID).Str("parent_folder", parentFolderID).
				Msg("Graph removal: unclassified (moved to a non-archive/non-deleted folder?) — no action")
		}
	}
}

// deliverArchive reflects an Outlook archive into Beeper (Flow 5) by tagging the
// thread's room as low-priority. We call dp.TagRoom directly rather than going
// through simplevent.ChatInfoChange because the framework gates tag application
// on Config.OnlyBridgeTags (must be non-empty + contain m.lowpriority) AND on
// Config.TagOnlyOnCreate (defaults true → tags only at room creation, never on
// an existing room). A direct TagRoom is deterministic regardless of config,
// mirroring deliverReadReceipt's direct MarkRead.
func (ec *EmailClient) deliverArchive(ctx context.Context, conversationID, internetID string) {
	log := zerolog.Ctx(ctx)
	portalKey := networkid.PortalKey{
		ID:       networkid.PortalID("thread:" + conversationID),
		Receiver: ec.UserLogin.ID,
	}

	dp := ec.UserLogin.User.DoublePuppet(ctx)
	if dp == nil {
		// No double puppet: fall back to the framework event. It may be filtered
		// by OnlyBridgeTags/TagOnlyOnCreate, but it is the best available path.
		lowPri := event.RoomTagLowPriority
		ec.UserLogin.QueueRemoteEvent(&simplevent.ChatInfoChange{
			EventMeta: simplevent.EventMeta{
				Type:      bridgev2.RemoteEventChatInfoChange,
				PortalKey: portalKey,
				Sender:    bridgev2.EventSender{IsFromMe: true, SenderLogin: ec.UserLogin.ID},
			},
			ChatInfoChange: &bridgev2.ChatInfoChange{
				ChatInfo: &bridgev2.ChatInfo{
					UserLocal: &bridgev2.UserLocalPortalInfo{Tag: &lowPri},
				},
			},
		})
		return
	}

	portal, err := ec.Main.Bridge.GetExistingPortalByKey(ctx, portalKey)
	if err != nil || portal == nil || portal.MXID == "" {
		log.Warn().Err(err).Str("conversation_id", conversationID).
			Msg("Graph archive: portal/room not ready; skipping")
		return
	}
	if err := dp.EnsureJoined(ctx, portal.MXID); err != nil {
		log.Warn().Err(err).Stringer("room", portal.MXID).
			Msg("Graph archive: double puppet EnsureJoined failed; skipping")
		return
	}
	if err := dp.TagRoom(ctx, portal.MXID, event.RoomTagLowPriority, true); err != nil {
		log.Warn().Err(err).Stringer("room", portal.MXID).Msg("Graph archive: TagRoom failed")
		return
	}
	log.Info().Stringer("room", portal.MXID).Str("internet_id", internetID).
		Msg("Graph archive: tagged room low-priority (Flow 5)")
}

// deliverDelete reflects an Outlook delete into Beeper (Flow 7) by deleting the
// thread's room. simplevent.ChatDelete{OnlyForMe:false} drives the framework's
// handleRemoteChatDelete → portal.Delete + Bot.DeleteRoom. Our portals have a
// non-empty Receiver, so the OnlyForMe branch is bypassed and the room is fully
// deleted.
func (ec *EmailClient) deliverDelete(ctx context.Context, conversationID, internetID string) {
	log := zerolog.Ctx(ctx)
	portalKey := networkid.PortalKey{
		ID:       networkid.PortalID("thread:" + conversationID),
		Receiver: ec.UserLogin.ID,
	}
	res := ec.UserLogin.QueueRemoteEvent(&simplevent.ChatDelete{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventChatDelete,
			PortalKey: portalKey,
			Sender:    bridgev2.EventSender{IsFromMe: true, SenderLogin: ec.UserLogin.ID},
		},
		OnlyForMe: false,
	})
	if !res.Success {
		log.Error().Str("conversation_id", conversationID).Msg("Graph delete: queue ChatDelete failed")
		return
	}
	log.Info().Str("conversation_id", conversationID).Str("internet_id", internetID).
		Msg("Graph delete: queued room delete (Flow 7)")
}
