package email

import (
	"context"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// fakeAttachment is a minimal MatrixAttachment for testing ConvertAttachmentPart.
type fakeAttachment struct {
	name        string
	contentType string
	size        int64
	data        []byte
}

func (f fakeAttachment) GetName() string        { return f.name }
func (f fakeAttachment) GetContentType() string { return f.contentType }
func (f fakeAttachment) GetSize() int64         { return f.size }
func (f fakeAttachment) GetBytes() []byte       { return f.data }

// mockIntent embeds bridgev2.MatrixAPI and overrides only UploadMedia.
// Any other method call would panic on the nil embedded interface, which keeps
// the test honest: ConvertAttachmentPart must not touch the rest of the API.
type mockIntent struct {
	bridgev2.MatrixAPI
	uploadCalls int
	lastData    []byte
	lastName    string
	lastMime    string
}

func (m *mockIntent) UploadMedia(_ context.Context, _ id.RoomID, data []byte, fileName, mimeType string) (id.ContentURIString, *event.EncryptedFileInfo, error) {
	m.uploadCalls++
	m.lastData = data
	m.lastName = fileName
	m.lastMime = mimeType
	return id.ContentURIString("mxc://example.org/abc123"), nil, nil
}

func TestConvertAttachmentPart_PDF_File(t *testing.T) {
	att := fakeAttachment{name: "report.pdf", contentType: "application/pdf", size: 1024, data: []byte("PDFDATA")}
	intent := &mockIntent{}

	part, err := ConvertAttachmentPart(context.Background(), att, intent, "att-1-report.pdf", 25*1024*1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if intent.uploadCalls != 1 {
		t.Fatalf("expected 1 upload call, got %d", intent.uploadCalls)
	}
	if part.Content.MsgType != event.MsgFile {
		t.Fatalf("expected m.file, got %s", part.Content.MsgType)
	}
	if part.Content.URL != "mxc://example.org/abc123" {
		t.Fatalf("unexpected MXC URL: %s", part.Content.URL)
	}
	if part.Content.Body != "report.pdf" {
		t.Fatalf("unexpected body: %s", part.Content.Body)
	}
	if string(part.ID) != "att-1-report.pdf" {
		t.Fatalf("unexpected part ID: %s", part.ID)
	}
	if part.Content.Info == nil || part.Content.Info.MimeType != "application/pdf" || part.Content.Info.Size != 1024 {
		t.Fatalf("unexpected FileInfo: %+v", part.Content.Info)
	}
}

func TestConvertAttachmentPart_PNG_Image(t *testing.T) {
	att := fakeAttachment{name: "photo.png", contentType: "image/png", size: 2048, data: []byte("PNGDATA")}
	intent := &mockIntent{}

	part, err := ConvertAttachmentPart(context.Background(), att, intent, "att-2-photo.png", 25*1024*1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if intent.uploadCalls != 1 {
		t.Fatalf("expected 1 upload call, got %d", intent.uploadCalls)
	}
	if part.Content.MsgType != event.MsgImage {
		t.Fatalf("expected m.image, got %s", part.Content.MsgType)
	}
}

func TestConvertAttachmentPart_OverCap_Notice(t *testing.T) {
	att := fakeAttachment{name: "huge.zip", contentType: "application/zip", size: 50 * 1024 * 1024, data: []byte("BIG")}
	intent := &mockIntent{}

	part, err := ConvertAttachmentPart(context.Background(), att, intent, "att-3-huge.zip", 25*1024*1024)
	if err != nil {
		t.Fatalf("over-cap must not error, got: %v", err)
	}
	if intent.uploadCalls != 0 {
		t.Fatalf("over-cap must not upload, got %d calls", intent.uploadCalls)
	}
	if part.Content.MsgType != event.MsgNotice {
		t.Fatalf("expected m.notice, got %s", part.Content.MsgType)
	}
	if part.Content.Body != "📎 for stor til at sende: huge.zip" {
		t.Fatalf("unexpected notice body: %q", part.Content.Body)
	}
	if string(part.ID) != "att-3-huge.zip" {
		t.Fatalf("notice part must keep deterministic ID, got: %s", part.ID)
	}
}
