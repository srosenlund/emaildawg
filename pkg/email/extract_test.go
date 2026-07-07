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

func TestPruneBulkContent_OutlookWrapperNotEmptied(t *testing.T) {
	// Outlook/Graph-formen: meta+style-elementer i toppen og AL synlig tekst i
	// ét <center>-wrapper-element med footer-keywords nederst. Style-CSS må
	// ikke tælle som indhold i retention-guarden, og wrapper-blokken må ikke
	// fældes af footerens "unsubscribe" — det tømte hele mailen, så Beeper kun
	// viste subject-linjen.
	css := "<style><!--\n" + strings.Repeat("p { margin:10px 0; padding:0 }\ntable { border-collapse:collapse }\n", 120) + "--></style>"
	in := `<meta charset="utf-8"><meta name="viewport" content="width=device-width"><meta http-equiv="X-UA-Compatible" content="IE=edge">` +
		css +
		`<center>` +
		`<p>Hi Stefan, You are reading edition 252 of the newsletter with insights and deals.</p>` +
		`<p>Google has to pay PriceRunner owners a large sum. A court ruled against the tech giant in a landmark antitrust case with damages and interest.</p>` +
		`<p>A new AI factory raised a large round to build datacenters across the region with several funds participating in the consortium.</p>` +
		`<p><a href="https://x.com/unsub">Unsubscribe</a> · <a href="https://x.com/browser">View in browser</a> · © 2026 All rights reserved.</p>` +
		`</center>`
	clean, plain := toReaderModeHTML(in, readerModeOptions{MinImgPx: 32, Extract: true})
	if !strings.Contains(plain, "PriceRunner") {
		t.Fatalf("main content lost (clean=%d bytes): %q", len(clean), plain)
	}
	if strings.Contains(plain, "Unsubscribe") {
		t.Fatalf("footer should still be pruned inside the wrapper: %q", plain)
	}
}

func TestPruneBulkContent_NonBulkUntouched(t *testing.T) {
	in := `<p>Hej Stefan</p><p><a href="https://x.com/unsub">Unsubscribe</a></p>`
	off, _ := toReaderModeHTML(in, readerModeOptions{MinImgPx: 32, Extract: false})
	if !strings.Contains(off, "Unsubscribe") {
		t.Fatalf("Extract:false must not prune: %q", off)
	}
}

func TestPruneBulkContent_TrailingCTAPreserved(t *testing.T) {
	// En ren link-række uden footer-keywords/social-navne i bunden af en
	// wrapped digest er ofte artiklens eneste CTA — må aldrig fældes.
	in := `<div>` +
		`<p>Story one about the market with plenty of context and detail for readers today.</p>` +
		`<p>Story two about a large funding round with several participating funds this week.</p>` +
		`<p>Story three about an acquisition in the region with numbers and commentary.</p>` +
		`<p><a href="https://x.com/story">Read the full story</a></p>` +
		`</div>`
	_, plain := toReaderModeHTML(in, readerModeOptions{MinImgPx: 32, Extract: true})
	if !strings.Contains(plain, "Read the full story") {
		t.Fatalf("trailing CTA pruned: %q", plain)
	}
}

func TestPruneBulkContent_DataTableRowsUntouched(t *testing.T) {
	// En bulk-mail hvis krop er én data-tabel (har <th> → overlever
	// unwrapLayoutTables): descend må ikke gå ind i tabel-struktur og score
	// rækker som blokke — en © -række i en prisliste er data, ikke footer.
	in := `<table><thead><tr><th>Product</th><th>Price</th></tr></thead><tbody>` +
		`<tr><td>Widget A with a long descriptive product name</td><td>100</td></tr>` +
		`<tr><td>Widget B with another long descriptive name</td><td>200</td></tr>` +
		`<tr><td>© 2026 special edition widget bundle</td><td>300</td></tr>` +
		`</tbody></table>`
	_, plain := toReaderModeHTML(in, readerModeOptions{MinImgPx: 32, Extract: true})
	if !strings.Contains(plain, "special edition widget bundle") {
		t.Fatalf("data table row pruned: %q", plain)
	}
}

func TestPruneBulkContent_SingleParagraphAnchorsUntouched(t *testing.T) {
	// Descend må ikke gå ind i <p> — inline-anchors i løbende prosa må ikke
	// scores som blokke og fjernes midt i en sætning.
	in := `<div><p>Read our terms in the ` +
		`<a href="https://x.com/a">first document</a> and the ` +
		`<a href="https://x.com/b">second document</a> before you ` +
		`<a href="https://x.com/unsub">unsubscribe from the service</a> at any time.</p></div>`
	_, plain := toReaderModeHTML(in, readerModeOptions{MinImgPx: 32, Extract: true})
	if !strings.Contains(plain, "unsubscribe from the service") {
		t.Fatalf("inline anchor pruned mid-prose: %q", plain)
	}
}

func TestPruneBulkContent_BareTextWrapperFooterPruned(t *testing.T) {
	// Wrapper med prosa som rene tekst-noder (br-separeret) og kun anchors som
	// element-børn: tekst-noderne skal tælle i retention-guarden, så footer-
	// anchors stadig kan fældes.
	in := `<div>` +
		`This is the main story of the newsletter with plenty of prose and context.<br><br>` +
		`Another paragraph of real content that the reader actually wants to see here.<br>` +
		`<a href="https://x.com/report">Read the report</a><br>` +
		`<a href="https://x.com/unsub">Unsubscribe</a> ` +
		`<a href="https://x.com/browser">View in browser</a>` +
		`</div>`
	_, plain := toReaderModeHTML(in, readerModeOptions{MinImgPx: 32, Extract: true})
	if !strings.Contains(plain, "main story of the newsletter") {
		t.Fatalf("prose lost: %q", plain)
	}
	if strings.Contains(plain, "Unsubscribe") {
		t.Fatalf("footer anchor survived in bare-text wrapper: %q", plain)
	}
	if !strings.Contains(plain, "Read the report") {
		t.Fatalf("content CTA lost: %q", plain)
	}
}
