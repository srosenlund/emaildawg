package connector

import (
	"context"
	"strings"
	"testing"

	"maunium.net/go/mautrix/event"

	"github.com/iFixRobots/emaildawg/pkg/graph"
)

// TestConvertAttachmentBackfillMessage verifies the backfill bilag-message: a
// leading "📎 Bilag til tidligere mail" header notice, then one media part per
// attachment, then notices — and crucially NO text body part (the original mail's
// text is already in the room; re-delivering it would duplicate it).
func TestConvertAttachmentBackfillMessage(t *testing.T) {
	g := &graph.GraphMessage{
		ID:             "AAMkAGxyz",
		Subject:        "Faktura marts",
		BodyText:       "Her er filerne", // must NOT be re-delivered as text
		HasAttachments: true,
		Attachments: []graph.GraphAttachment{
			{Name: "photo.png", ContentType: "image/png", Bytes: []byte("PNGDATA"), Size: 7},
		},
	}
	const itemCount = 1
	intent := &mockDeliverIntent{}

	conv, err := convertAttachmentBackfillMessage(context.Background(), intent, g, itemCount, 25*1024*1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// header notice + image + item notice = 3 parts. No m.text part.
	if len(conv.Parts) != 3 {
		t.Fatalf("expected 3 parts (header, image, item notice), got %d", len(conv.Parts))
	}

	header := conv.Parts[0]
	if header.Content.MsgType != event.MsgNotice {
		t.Fatalf("header: expected m.notice, got %s", header.Content.MsgType)
	}
	if !strings.Contains(header.Content.Body, "Bilag til tidligere mail") {
		t.Fatalf("header body missing prefix: %q", header.Content.Body)
	}
	if !strings.Contains(header.Content.Body, "Faktura marts") {
		t.Fatalf("header body missing subject: %q", header.Content.Body)
	}

	for _, p := range conv.Parts {
		if p.Content.MsgType == event.MsgText {
			t.Fatalf("backfill message must NOT contain an m.text part (would duplicate the original text): %q", p.Content.Body)
		}
	}

	img := conv.Parts[1]
	if img.Content.MsgType != event.MsgImage {
		t.Fatalf("part 2: expected m.image, got %s", img.Content.MsgType)
	}
	if string(img.ID) != "att-1-photo.png" {
		t.Fatalf("part 2: unexpected deterministic PartID %q", img.ID)
	}
	if intent.uploadCalls != 1 {
		t.Fatalf("expected exactly 1 upload, got %d", intent.uploadCalls)
	}
}

// TestConvertAttachmentBackfillMessage_NoFiles verifies that when the only
// attachments are itemAttachments (no deliverable files), the message still emits
// the header + the item notice (so the user learns there was an unsupported
// attachment) — never an empty message.
func TestConvertAttachmentBackfillMessage_NoFiles(t *testing.T) {
	g := &graph.GraphMessage{Subject: "Videresendt", HasAttachments: true}
	intent := &mockDeliverIntent{}

	conv, err := convertAttachmentBackfillMessage(context.Background(), intent, g, 1, 25*1024*1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(conv.Parts) != 2 {
		t.Fatalf("expected 2 parts (header, item notice), got %d", len(conv.Parts))
	}
}
