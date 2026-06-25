package connector

import (
	"context"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/iFixRobots/emaildawg/pkg/graph"
)

// mockDeliverIntent embeds bridgev2.MatrixAPI and overrides only UploadMedia.
// Any other method call panics on the nil embedded interface, keeping the test
// honest: convertGraphMessage must not touch the rest of the API.
type mockDeliverIntent struct {
	bridgev2.MatrixAPI
	uploadCalls int
}

func (m *mockDeliverIntent) UploadMedia(_ context.Context, _ id.RoomID, _ []byte, _, _ string) (id.ContentURIString, *event.EncryptedFileInfo, error) {
	m.uploadCalls++
	return id.ContentURIString("mxc://example.org/abc123"), nil, nil
}

// TestConvertGraphMessage_AttachmentsAndNotices verifies the full ConvertMessageFunc
// body: text part 1, a media part per (capped) file attachment, an over-cap notice,
// and an item-attachment notice — all with deterministic PartIDs.
func TestConvertGraphMessage_AttachmentsAndNotices(t *testing.T) {
	g := &graph.GraphMessage{
		ID:             "AAMkAGxyz",
		Subject:        "Hej",
		BodyText:       "Her er filerne",
		HasAttachments: true,
		Attachments: []graph.GraphAttachment{
			{Name: "photo.png", ContentType: "image/png", Bytes: []byte("PNGDATA"), Size: 7},
			{Name: "huge.zip", ContentType: "application/zip", Bytes: []byte("BIG"), Size: 50 * 1024 * 1024},
		},
	}
	const itemCount = 1
	intent := &mockDeliverIntent{}

	conv, err := convertGraphMessage(context.Background(), intent, g, itemCount, 25*1024*1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(conv.Parts) != 4 {
		t.Fatalf("expected 4 parts (text, image, over-cap notice, item notice), got %d", len(conv.Parts))
	}

	// Part 1: text body.
	p1 := conv.Parts[0]
	if p1.Content.MsgType != event.MsgText {
		t.Fatalf("part 1: expected m.text, got %s", p1.Content.MsgType)
	}
	if p1.Content.Body != "Hej\n\nHer er filerne" {
		t.Fatalf("part 1: unexpected body %q", p1.Content.Body)
	}

	// Part 2: png → m.image with deterministic ID att-1-photo.png.
	p2 := conv.Parts[1]
	if p2.Content.MsgType != event.MsgImage {
		t.Fatalf("part 2: expected m.image, got %s", p2.Content.MsgType)
	}
	if string(p2.ID) != "att-1-photo.png" {
		t.Fatalf("part 2: unexpected PartID %q", p2.ID)
	}
	if intent.uploadCalls != 1 {
		t.Fatalf("expected exactly 1 upload (only the in-cap file), got %d", intent.uploadCalls)
	}

	// Part 3: over-cap zip → notice, but still deterministic ID att-2-huge.zip.
	p3 := conv.Parts[2]
	if p3.Content.MsgType != event.MsgNotice {
		t.Fatalf("part 3: expected m.notice (over cap), got %s", p3.Content.MsgType)
	}
	if string(p3.ID) != "att-2-huge.zip" {
		t.Fatalf("part 3: unexpected PartID %q", p3.ID)
	}

	// Part 4: item-attachment notice.
	p4 := conv.Parts[3]
	if p4.Content.MsgType != event.MsgNotice {
		t.Fatalf("part 4: expected m.notice (item), got %s", p4.Content.MsgType)
	}
	if p4.Content.Body != "[vedhæftet besked — ikke understøttet]" {
		t.Fatalf("part 4: unexpected item-notice body %q", p4.Content.Body)
	}
}

// TestConvertGraphMessage_TextOnly verifies the no-attachment path stays a single
// text part (slim delta path unchanged).
func TestConvertGraphMessage_TextOnly(t *testing.T) {
	g := &graph.GraphMessage{Subject: "Emne", BodyText: "Krop"}
	intent := &mockDeliverIntent{}

	conv, err := convertGraphMessage(context.Background(), intent, g, 0, 25*1024*1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(conv.Parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(conv.Parts))
	}
	if intent.uploadCalls != 0 {
		t.Fatalf("expected 0 uploads, got %d", intent.uploadCalls)
	}
}

// TestConvertGraphMessage_OverflowNotice verifies that with >maxAttachmentParts
// file attachments, only the first maxAttachmentParts are delivered and a single
// "+N flere bilag" notice is appended.
func TestConvertGraphMessage_OverflowNotice(t *testing.T) {
	atts := make([]graph.GraphAttachment, 0, maxAttachmentParts+5)
	for i := 0; i < maxAttachmentParts+5; i++ {
		atts = append(atts, graph.GraphAttachment{Name: "f.bin", ContentType: "application/octet-stream", Bytes: []byte("x"), Size: 1})
	}
	g := &graph.GraphMessage{Subject: "S", HasAttachments: true, Attachments: atts}
	intent := &mockDeliverIntent{}

	conv, err := convertGraphMessage(context.Background(), intent, g, 0, 25*1024*1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 1 text + maxAttachmentParts media + 1 overflow notice
	if len(conv.Parts) != 1+maxAttachmentParts+1 {
		t.Fatalf("expected %d parts, got %d", 1+maxAttachmentParts+1, len(conv.Parts))
	}
	last := conv.Parts[len(conv.Parts)-1]
	if last.Content.MsgType != event.MsgNotice {
		t.Fatalf("last part: expected overflow notice, got %s", last.Content.MsgType)
	}
	if intent.uploadCalls != maxAttachmentParts {
		t.Fatalf("expected %d uploads, got %d", maxAttachmentParts, intent.uploadCalls)
	}
}
