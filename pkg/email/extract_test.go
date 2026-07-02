package email

import (
	"strings"
	"testing"
)

func TestPruneBulkContent_DropsFooterAndSocial(t *testing.T) {
	in := `<p>Vigtig nyhed: AI-panelet er bekræftet til konferencen med to store fonde og en fuld dagsorden for eftermiddagen.</p>` +
		`<p>Læs mere om programmet og tilmeld dig via vores hjemmeside hvor alle detaljer løbende opdateres.</p>` +
		`<p><a href="https://x.com/unsub">Unsubscribe</a> · <a href="https://x.com/prefs">Preferences</a> · <a href="https://x.com/browser">View in browser</a></p>` +
		`<p>© 2026 NIO Partners. All rights reserved. You are receiving this email because you signed up.</p>` +
		`<p><a href="https://facebook.com/x">Facebook</a> <a href="https://linkedin.com/x">LinkedIn</a> <a href="https://instagram.com/x">Instagram</a></p>`
	clean, plain := toReaderModeHTML(in, readerModeOptions{MinImgPx: 32, Extract: true})
	if !strings.Contains(clean, "AI-panelet er bekræftet") {
		t.Fatalf("main content lost: %q", clean)
	}
	for _, junk := range []string{"Unsubscribe", "All rights reserved", "Facebook"} {
		if strings.Contains(plain, junk) {
			t.Fatalf("footer junk survived %q: %q", junk, plain)
		}
	}
}

func TestPruneBulkContent_RetentionGuardFallsBack(t *testing.T) {
	// Alt ligner footer/link-suppe → pruning ville fjerne >60% → guard skal give Lag 1-output uændret
	in := `<p><a href="https://a">A</a></p><p><a href="https://b">B</a></p><p><a href="https://c">C</a></p>`
	clean, _ := toReaderModeHTML(in, readerModeOptions{MinImgPx: 32, Extract: true})
	for _, keep := range []string{">A<", ">B<", ">C<"} {
		if !strings.Contains(clean, keep) {
			t.Fatalf("retention guard failed, content dropped: %q", clean)
		}
	}
}

func TestPruneBulkContent_NonBulkUntouched(t *testing.T) {
	in := `<p>Hej Stefan</p><p><a href="https://x.com/unsub">Unsubscribe</a></p>`
	off, _ := toReaderModeHTML(in, readerModeOptions{MinImgPx: 32, Extract: false})
	if !strings.Contains(off, "Unsubscribe") {
		t.Fatalf("Extract:false must not prune: %q", off)
	}
}
