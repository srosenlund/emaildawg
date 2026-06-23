package email

import (
	"strings"
	"testing"
)

func TestProcessor_finalizeHTML_ReaderModeOn(t *testing.T) {
	p := &Processor{ReaderMode: true, ReaderModeMinImgPx: 32}
	in := `<table role="presentation"><tr><td><p>Hi there</p></td></tr></table>` +
		`<img src="mxc://h/track" width="1" height="1"><script>x()</script>`
	formatted, plain := p.finalizeHTML(in)
	if strings.Contains(formatted, "<table") {
		t.Fatalf("layout table survived: %q", formatted)
	}
	if strings.Contains(formatted, "<script") || strings.Contains(formatted, "x()") {
		t.Fatalf("script survived: %q", formatted)
	}
	if strings.Contains(formatted, "mxc://h/track") {
		t.Fatalf("tracking pixel survived: %q", formatted)
	}
	if !strings.Contains(plain, "Hi there") {
		t.Fatalf("plaintext missing content: %q", plain)
	}
}

func TestProcessor_finalizeHTML_ReaderModeOff(t *testing.T) {
	p := &Processor{ReaderMode: false}
	formatted, plain := p.finalizeHTML(`<p>raw &amp; co</p>`)
	if plain != "" {
		t.Fatalf("legacy path should not set plain, got %q", plain)
	}
	if !strings.Contains(formatted, "raw & co") {
		t.Fatalf("legacy unescape failed: %q", formatted)
	}
}
