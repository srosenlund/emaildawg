package connector

import (
	"context"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
)

// PHASE 3 — PROBE STAGE (2026-06-20).
//
// These two handlers register EmailClient as a TagHandlingNetworkAPI and a
// RedactionHandlingNetworkAPI, but they currently only LOG what Beeper sends.
// The Beeper→Outlook triggers cannot be determined from framework code alone:
// it is unknown whether "archive chat" in Beeper emits an m.tag event (here) or
// a room capability with no tag event, and whether "delete chat" emits a
// per-message redaction (here) or nothing. Once Stefan archives and deletes one
// test chat, these logs reveal the real event shape and the handlers get their
// actual Graph actions (Move/Delete) in Tasks 3–4.

// HandleRoomTag is called when a portal room's m.tag account data changes
// (e.g. the user archives/low-priorities the chat in Beeper).
func (ec *EmailClient) HandleRoomTag(ctx context.Context, msg *bridgev2.MatrixRoomTag) error {
	log := zerolog.Ctx(ctx)
	ev := log.Info().Str("probe", "HandleRoomTag")
	if msg.Portal != nil {
		ev = ev.Str("portal_id", string(msg.Portal.ID)).Stringer("room", msg.Portal.MXID)
	}
	newTags := []string{}
	if msg.Content != nil {
		for tag := range msg.Content.Tags {
			newTags = append(newTags, string(tag))
		}
	}
	prevTags := []string{}
	if msg.PrevContent != nil {
		for tag := range msg.PrevContent.Tags {
			prevTags = append(prevTags, string(tag))
		}
	}
	ev.Strs("new_tags", newTags).Strs("prev_tags", prevTags).
		Msg("PHASE3 PROBE: room tag changed (no action yet)")
	return nil
}

// HandleMatrixMessageRemove is called when a previously bridged message is
// deleted/redacted in a portal room (e.g. the user deletes the chat in Beeper).
func (ec *EmailClient) HandleMatrixMessageRemove(ctx context.Context, msg *bridgev2.MatrixMessageRemove) error {
	log := zerolog.Ctx(ctx)
	ev := log.Info().Str("probe", "HandleMatrixMessageRemove")
	if msg.Portal != nil {
		ev = ev.Str("portal_id", string(msg.Portal.ID)).Stringer("room", msg.Portal.MXID)
	}
	if msg.TargetMessage != nil {
		ev = ev.Str("target_message_id", string(msg.TargetMessage.ID)).
			Stringer("target_mxid", msg.TargetMessage.MXID)
	}
	ev.Msg("PHASE3 PROBE: message removed (no action yet)")
	return nil
}
