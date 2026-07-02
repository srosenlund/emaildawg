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
	// To store keyword-blokke ville fjerne >60% af teksten → guard skal give Lag 1-output uændret
	kw := `<p><a href="https://x/u">Unsubscribe alle nyhedsbreve og opdateringer fra afsenderen med det samme</a></p>`
	in := `<p>Kort.</p>` + kw + kw
	clean, _ := toReaderModeHTML(in, readerModeOptions{MinImgPx: 32, Extract: true})
	if !strings.Contains(clean, "Unsubscribe alle") || !strings.Contains(clean, "Kort.") {
		t.Fatalf("retention guard failed, content dropped: %q", clean)
	}
}

func TestPruneBulkContent_MidBodyCTAPreserved(t *testing.T) {
	// En kort link-tung CTA midt i mailen er ofte hovedindholdet — må ikke prunes.
	in := `<p>Vi har udgivet årets store rapport om det nordiske marked med analyser og data fra hele branchen.</p>` +
		`<p><a href="https://x.com/report">Læs hele rapporten her</a></p>` +
		`<p>Rapporten dækker fonde, exits og dealflow over de sidste tolv måneder med kommentarer fra aktørerne.</p>` +
		`<p>Med venlig hilsen fra hele redaktionen bag årets udgivelse.</p>` +
		`<p><a href="https://x.com/unsub">Unsubscribe</a> · <a href="https://x.com/browser">View in browser</a></p>`
	clean, plain := toReaderModeHTML(in, readerModeOptions{MinImgPx: 32, Extract: true})
	if !strings.Contains(clean, "Læs hele rapporten her") {
		t.Fatalf("mid-body CTA pruned: %q", clean)
	}
	if strings.Contains(plain, "Unsubscribe") {
		t.Fatalf("footer survived: %q", plain)
	}
}

func TestPruneBulkContent_NonBulkUntouched(t *testing.T) {
	in := `<p>Hej Stefan</p><p><a href="https://x.com/unsub">Unsubscribe</a></p>`
	off, _ := toReaderModeHTML(in, readerModeOptions{MinImgPx: 32, Extract: false})
	if !strings.Contains(off, "Unsubscribe") {
		t.Fatalf("Extract:false must not prune: %q", off)
	}
}
