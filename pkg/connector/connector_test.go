package connector

import (
	"testing"

	"github.com/iFixRobots/emaildawg/pkg/email"
)

func TestNewEmailConnector_ReaderModeDefaults(t *testing.T) {
	ec := NewEmailConnector()
	if !ec.Config.Processing.ReaderMode {
		t.Fatalf("ReaderMode should default to true")
	}
	if ec.Config.Processing.ReaderModeMinImgPx != email.DefaultReaderModeMinImgPx {
		t.Fatalf("ReaderModeMinImgPx default = %d, want %d",
			ec.Config.Processing.ReaderModeMinImgPx, email.DefaultReaderModeMinImgPx)
	}
}
