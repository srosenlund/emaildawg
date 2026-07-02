# Reader Mode v2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Pæn HTML-email-rendering i Beeper — fjern usynlig junk/skjult preheader-tekst/dobbelt-entities i alle mails, og prune footers/boilerplate i bulk-mails (nyhedsbreve) med retention-guard.

**Architecture:** Alt bygges i emaildawgs eksisterende reader-mode-pipeline. Lag 1+3 (hygiejne + polish) er nye tree-passes i `pkg/email/readermode.go`, aktive for alle mails. Lag 2 (densitets-pruner) er ny fil `pkg/email/extract.go`, gated på `ParsedEmail.IsBulk` (List-Unsubscribe/Precedence-headere) + config `reader_mode_extract`, med 40%-retention-guard og panic-recover der falder tilbage til Lag 1-output.

**Tech Stack:** Go 1.22+, golang.org/x/net/html (allerede dependency), bluemonday (uændret). Ingen nye dependencies.

**Spec:** `docs/superpowers/specs/2026-07-02-reader-mode-v2-design.md`.

## Global Constraints

- **Ingen nye Go-dependencies** — go-readability er eksplicit fravalgt (artikel-tunet, misfirer på email-tabel-suppe).
- **En mail må aldrig tabes:** enhver fejl/panic i Lag 2 → Lag 1-output. Retention-guard: < 40% tekst tilbage → Lag 1-output.
- **Ikke-bulk mails må ALDRIG røres af Lag 2** (byte-identisk med Lag 1-adfærd).
- **Konservativ unicode-junk-mængde:** KUN `U+034F`, `U+00AD`, `U+200B`–`U+200F`, `U+2060`, `U+FEFF` — ikke hele Cf/Mn (ville skade ikke-latinsk tekst). (Plaintext-stiens eksisterende `filterInvisibleUnicode` forbliver uændret.)
- **Pipeline-rækkefølge i `toReaderModeHTML`:** parse → dropHiddenElements → dropTrackingImages → unwrapLayoutTables → [pruneBulkContent hvis Extract] → stripJunkUnicode → decodeStableEntities → collapseNoise → demoteHeadings → render → sanitize → plaintext.
- **Test-stil:** table-driven som eksisterende `readermode_test.go` (mustContain/mustNotContain).
- Kør altid `go build ./... && go test ./pkg/email/` efter hver task. Arbejd på branchen `feat/reader-mode-v2` (fra `feat/graph-twoway`, som er den udrullede branch).

---

### Task 0: Branch

- [ ] **Step 1:**

```bash
cd ~/Projects/emaildawg && git checkout -b feat/reader-mode-v2
```

---

### Task 1: Lag 1 — hygiejne-passes i readermode.go

**Files:**
- Modify: `pkg/email/readermode.go`
- Test: `pkg/email/readermode_test.go` (append)

**Interfaces:**
- Produces (bruges af Task 3-wiring og Task 2-pipeline):
  - `dropHiddenElements(root *html.Node)` — fjerner skjulte elementer.
  - `stripJunkUnicode(root *html.Node)` — renser tekst-noder for junk-runer + kollapser nbsp/space-runs.
  - `decodeStableEntities(root *html.Node)` — anden dekodning af dobbelt-encodede entities i tekst-noder.
  - `collapseNoise(root *html.Node)` — br-runs, tomme blokke, `____`-runs → `<hr>`.
  - `demoteHeadings(root *html.Node)` — h1→h3, h2→h4.
  - `readerModeOptions` får feltet `Extract bool`.

- [ ] **Step 1: Skriv failing tests (append til readermode_test.go)**

```go
func TestReaderModeV2_HiddenElementsDropped(t *testing.T) {
	in := `<div style="display:none">PREHEADER JUNK</div>` +
		`<div style="max-height:0px;overflow:hidden">MORE JUNK</div>` +
		`<span style="font-size:1px">TINY</span>` +
		`<span style="mso-hide:all">MSO</span>` +
		`<p>Synligt indhold</p>`
	clean, plain := toReaderModeHTML(in, readerModeOptions{MinImgPx: 32})
	for _, junk := range []string{"PREHEADER JUNK", "MORE JUNK", "TINY", "MSO"} {
		if strings.Contains(clean, junk) || strings.Contains(plain, junk) {
			t.Fatalf("hidden element leaked %q: %q", junk, clean)
		}
	}
	if !strings.Contains(clean, "Synligt indhold") {
		t.Fatalf("visible content lost: %q", clean)
	}
}

func TestReaderModeV2_JunkUnicodeStripped(t *testing.T) {
	// U+034F combining grapheme joiner + U+00AD soft hyphen — klassisk preheader-padding
	in := "<p>Intro͏ ͏ ͏ ͏ ­ ­ ­ tekst</p>"
	clean, _ := toReaderModeHTML(in, readerModeOptions{MinImgPx: 32})
	if strings.Contains(clean, "͏") || strings.Contains(clean, "­") {
		t.Fatalf("junk unicode survived: %q", clean)
	}
	if !strings.Contains(clean, "Intro") || !strings.Contains(clean, "tekst") {
		t.Fatalf("content lost: %q", clean)
	}
}

func TestReaderModeV2_DoubleEncodedEntities(t *testing.T) {
	// Kilden var dobbelt-encodet (&amp;quot;) — parseren dekoder én gang, vi skal tage anden runde
	in := `<p>&amp;quot;Daniel&amp;quot; &amp;lt;d@x.com&amp;gt;</p>`
	clean, plain := toReaderModeHTML(in, readerModeOptions{MinImgPx: 32})
	if strings.Contains(plain, "&quot;") || strings.Contains(plain, "&lt;") {
		t.Fatalf("entities not decoded: %q", plain)
	}
	if !strings.Contains(plain, `"Daniel"`) {
		t.Fatalf("expected decoded quotes: %q", plain)
	}
	_ = clean
}

func TestReaderModeV2_CollapseNoise(t *testing.T) {
	in := `<p>a</p><br><br><br><br><p></p><div></div><p>________________________</p><p>b</p>`
	clean, _ := toReaderModeHTML(in, readerModeOptions{MinImgPx: 32})
	if strings.Count(clean, "<br") > 2 {
		t.Fatalf("br run not collapsed: %q", clean)
	}
	if strings.Contains(clean, "________") {
		t.Fatalf("underscore separator survived: %q", clean)
	}
	if !strings.Contains(clean, "<hr") {
		t.Fatalf("expected hr replacement: %q", clean)
	}
}

func TestReaderModeV2_HeadingsDemoted(t *testing.T) {
	in := `<h1>Stor</h1><h2>Mellem</h2><h3>Fin</h3>`
	clean, _ := toReaderModeHTML(in, readerModeOptions{MinImgPx: 32})
	if strings.Contains(clean, "<h1") || strings.Contains(clean, "<h2") {
		t.Fatalf("h1/h2 not demoted: %q", clean)
	}
	if !strings.Contains(clean, "<h3>Stor</h3>") || !strings.Contains(clean, "<h4>Mellem</h4>") || !strings.Contains(clean, "<h3>Fin</h3>") {
		t.Fatalf("demotion wrong: %q", clean)
	}
}
```

- [ ] **Step 2: Kør tests — verificér FAIL**

Run: `cd ~/Projects/emaildawg && go test ./pkg/email/ -run TestReaderModeV2 -v`
Expected: FAIL (funktionerne findes ikke / adfærd mangler).

- [ ] **Step 3: Implementér de fem passes i readermode.go**

Tilføj `Extract bool` til `readerModeOptions`:

```go
type readerModeOptions struct {
	MinImgPx int
	Extract  bool
}
```

Tilføj nederst i filen:

```go
// hiddenStyleRe matches inline styles that hide content (preheaders, spacers).
var hiddenStyleRe = regexp.MustCompile(`(?i)display\s*:\s*none|visibility\s*:\s*hidden|opacity\s*:\s*0(?:[;\s"]|$)|font-size\s*:\s*[01]px|font-size\s*:\s*0(?:[;\s"]|$)|max-height\s*:\s*0|mso-hide\s*:\s*all`)

// dropHiddenElements removes any element whose inline style hides it.
// Generalises the img-only hidden check to all elements (hidden preheader text).
func dropHiddenElements(root *html.Node) {
	var toRemove []*html.Node
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode {
				for _, a := range c.Attr {
					if strings.EqualFold(a.Key, "style") && hiddenStyleRe.MatchString(a.Val) {
						toRemove = append(toRemove, c)
						break
					}
				}
			}
			walk(c)
		}
	}
	walk(root)
	for _, n := range toRemove {
		if n.Parent != nil {
			n.Parent.RemoveChild(n)
		}
	}
}

// junkRunes is the conservative set of invisible characters newsletters use as
// preheader padding. Deliberately NOT all of Cf/Mn — that would damage
// legitimate combining marks in non-Latin text.
func isJunkRune(r rune) bool {
	switch {
	case r == '͏', r == '­', r == '⁠', r == '﻿':
		return true
	case r >= '​' && r <= '‏':
		return true
	}
	return false
}

var nbspRunRe = regexp.MustCompile(`[\x{00A0} \t]{4,}`)

// stripJunkUnicode removes preheader-padding runes from text nodes and
// collapses long nbsp/space runs. Skips pre/code content.
func stripJunkUnicode(root *html.Node) {
	walkTextNodes(root, func(n *html.Node) {
		s := strings.Map(func(r rune) rune {
			if isJunkRune(r) {
				return -1
			}
			return r
		}, n.Data)
		n.Data = nbspRunRe.ReplaceAllString(s, " ")
	})
}

var entityRe = regexp.MustCompile(`&(?:[a-zA-Z]{2,10}|#\d{1,7});`)

// decodeStableEntities runs a second entity-decode on text nodes that still
// contain entity patterns after parsing — i.e. the source was double-encoded
// (common in forwarded Gmail HTML).
func decodeStableEntities(root *html.Node) {
	walkTextNodes(root, func(n *html.Node) {
		if entityRe.MatchString(n.Data) {
			n.Data = htmlpkg.UnescapeString(n.Data)
		}
	})
}

// walkTextNodes calls fn on every text node not inside pre/code.
func walkTextNodes(root *html.Node, fn func(*html.Node)) {
	var walk func(n *html.Node, inPre bool)
	walk = func(n *html.Node, inPre bool) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			pre := inPre || (c.Type == html.ElementNode && (c.DataAtom == atom.Pre || c.DataAtom == atom.Code))
			if c.Type == html.TextNode && !inPre {
				fn(c)
			}
			walk(c, pre)
		}
	}
	walk(root, false)
}

var separatorRunRe = regexp.MustCompile(`[_\-=]{5,}`)

// collapseNoise trims visual noise: >2 consecutive <br>, empty p/div chains,
// and text-only separator runs (____) which become a single <hr>.
func collapseNoise(root *html.Node) {
	// 1) br-runs: fjern br nr. 3+ i en ubrudt (whitespace-tolerant) kæde
	var brRemove []*html.Node
	var walkBr func(n *html.Node)
	walkBr = func(n *html.Node) {
		run := 0
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			switch {
			case c.Type == html.ElementNode && c.DataAtom == atom.Br:
				run++
				if run > 2 {
					brRemove = append(brRemove, c)
				}
			case c.Type == html.TextNode && strings.TrimSpace(c.Data) == "":
				// whitespace bryder ikke kæden
			default:
				run = 0
			}
			walkBr(c)
		}
	}
	walkBr(root)
	for _, n := range brRemove {
		if n.Parent != nil {
			n.Parent.RemoveChild(n)
		}
	}

	// 2) separator-runs i tekst-noder → <hr> hvis noden kun er separatoren
	walkTextNodes(root, func(n *html.Node) {
		trimmed := strings.TrimSpace(n.Data)
		if trimmed != "" && separatorRunRe.ReplaceAllString(trimmed, "") == "" {
			n.Data = ""
			hr := &html.Node{Type: html.ElementNode, Data: "hr", DataAtom: atom.Hr}
			n.Parent.InsertBefore(hr, n)
		} else {
			n.Data = separatorRunRe.ReplaceAllString(n.Data, "")
		}
	})

	// 3) tomme p/div (ingen tekst, ingen img/a/hr/br) fjernes
	var emptyRemove []*html.Node
	var walkEmpty func(n *html.Node)
	walkEmpty = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walkEmpty(c)
			if c.Type == html.ElementNode && (c.DataAtom == atom.P || c.DataAtom == atom.Div) {
				if strings.TrimSpace(textOf(c)) == "" && !hasDescendant(c, atom.Img) &&
					!hasDescendant(c, atom.A) && !hasDescendant(c, atom.Hr) && !hasDescendant(c, atom.Br) {
					emptyRemove = append(emptyRemove, c)
				}
			}
		}
	}
	walkEmpty(root)
	for _, n := range emptyRemove {
		if n.Parent != nil {
			n.Parent.RemoveChild(n)
		}
	}
}

// textOf returns the concatenated text content of a node.
func textOf(n *html.Node) string {
	var b strings.Builder
	var walk func(x *html.Node)
	walk = func(x *html.Node) {
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.TextNode {
				b.WriteString(c.Data)
			}
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

// demoteHeadings maps h1→h3 and h2→h4 so newsletter headlines don't scream in
// Matrix clients. h3–h6 render at sane sizes and are left alone.
func demoteHeadings(root *html.Node) {
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode {
				switch c.DataAtom {
				case atom.H1:
					c.Data, c.DataAtom = "h3", atom.H3
				case atom.H2:
					c.Data, c.DataAtom = "h4", atom.H4
				}
			}
			walk(c)
		}
	}
	walk(root)
}
```

Import-ændring øverst: tilføj `htmlpkg "html"` til imports (std-lib `html` for UnescapeString; `golang.org/x/net/html` er allerede importeret som `html`).

Opdatér pipeline i `toReaderModeHTML` (erstat de to eksisterende pass-linjer):

```go
	dropHiddenElements(container)
	dropTrackingImages(container, opts.MinImgPx)
	unwrapLayoutTables(container)
	if opts.Extract {
		pruneBulkContent(container) // Task 2; no-op indtil da — tilføj først i Task 2
	}
	stripJunkUnicode(container)
	decodeStableEntities(container)
	collapseNoise(container)
	demoteHeadings(container)
```

(I denne task udelades `if opts.Extract`-blokken — den tilføjes i Task 2.)

- [ ] **Step 4: Kør tests — verificér PASS + hele pakken**

Run: `cd ~/Projects/emaildawg && go build ./... && go test ./pkg/email/ -v -run 'TestReaderMode|TestSanitize'`
Expected: PASS, ingen regressioner i eksisterende reader-mode-tests.

- [ ] **Step 5: Commit**

```bash
git add pkg/email/readermode.go pkg/email/readermode_test.go
git commit -m "feat(readermode): lag 1+3 — skjulte elementer, junk-unicode, dobbelt-entities, støj-kollaps, heading-demotion"
```

---

### Task 2: Lag 2 — densitets-pruner (extract.go) med retention-guard

**Files:**
- Create: `pkg/email/extract.go`
- Test: `pkg/email/extract_test.go`
- Modify: `pkg/email/readermode.go` (aktivér `if opts.Extract`-blokken)

**Interfaces:**
- Consumes: `textOf`, `hasDescendant` (Task 1/eksisterende).
- Produces: `pruneBulkContent(container *html.Node)` — muterer træet in-place; recover'er selv ved panic; respekterer 40%-retention-guard. Kaldes KUN når `opts.Extract` er true.

- [ ] **Step 1: Skriv failing tests**

```go
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
```

- [ ] **Step 2: Kør tests — verificér FAIL (pruneBulkContent undefined)**

Run: `cd ~/Projects/emaildawg && go test ./pkg/email/ -run TestPruneBulk -v`
Expected: compile-fejl / FAIL.

- [ ] **Step 3: Implementér extract.go**

```go
package email

import (
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// retentionFloor: prunes the extraction entirely if less than this fraction of
// the original text would survive — a pruner in doubt does nothing.
const retentionFloor = 0.40

// footerKeywords mark boilerplate blocks (bottom-zone gated).
var footerKeywords = []string{
	"unsubscribe", "afmeld", "view in browser", "vis i browser",
	"privacy policy", "privatlivspolitik", "all rights reserved",
	"you are receiving this", "du modtager denne", "why did i get this",
	"manage preferences", "email preferences", "opdater dine præferencer",
	"©",
}

// socialNames used to detect pure social-link rows.
var socialNames = []string{"facebook", "twitter", "linkedin", "instagram", "youtube", "tiktok", "x.com"}

// pruneBulkContent removes footer/nav/social boilerplate from bulk email
// (newsletters). Operates on the post-linearisation tree: scores the
// container's top-level blocks and drops low-value ones. Retention guard and
// panic recovery guarantee it never destroys a mail.
func pruneBulkContent(container *html.Node) {
	defer func() {
		_ = recover() // en pruner-fejl må aldrig tabe en mail; Lag 1-træet består
	}()

	type block struct {
		node *html.Node
		text string
		drop bool
	}
	var blocks []block
	totalLen := 0
	for c := container.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode {
			continue
		}
		txt := strings.TrimSpace(textOf(c))
		blocks = append(blocks, block{node: c, text: txt})
		totalLen += len(txt)
	}
	if totalLen == 0 || len(blocks) < 3 {
		return // for lidt struktur til at score meningsfuldt
	}

	bottomZone := int(float64(len(blocks)) * 0.7)
	for i := range blocks {
		b := &blocks[i]
		if b.text == "" {
			continue
		}
		lower := strings.ToLower(b.text)
		ld := linkDensity(b.node)

		// Ren link-suppe: høj link-densitet og kort tekst (nav/social/CTA-rækker)
		if ld > 0.5 && len(b.text) < 200 {
			b.drop = true
			continue
		}
		// Footer-nøgleord i bund-zonen
		if i >= bottomZone {
			for _, kw := range footerKeywords {
				if strings.Contains(lower, kw) {
					b.drop = true
					break
				}
			}
		}
		// Rene social-rækker (alle links er social-navne)
		if !b.drop && ld > 0.8 && isSocialRow(lower) {
			b.drop = true
		}
	}

	kept := 0
	for _, b := range blocks {
		if !b.drop {
			kept += len(b.text)
		}
	}
	if float64(kept) < retentionFloor*float64(totalLen) {
		return // guard: i tvivl → gør ingenting
	}
	for _, b := range blocks {
		if b.drop && b.node.Parent != nil {
			b.node.Parent.RemoveChild(b.node)
		}
	}
}

// linkDensity = linked characters / all characters in the block.
func linkDensity(n *html.Node) float64 {
	total := len(strings.TrimSpace(textOf(n)))
	if total == 0 {
		return 0
	}
	linked := 0
	var walk func(x *html.Node)
	walk = func(x *html.Node) {
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode && c.DataAtom == atom.A {
				linked += len(strings.TrimSpace(textOf(c)))
				continue // undgå dobbelt-tælling af nested links
			}
			walk(c)
		}
	}
	walk(n)
	return float64(linked) / float64(total)
}

func isSocialRow(lowerText string) bool {
	hits := 0
	for _, s := range socialNames {
		if strings.Contains(lowerText, s) {
			hits++
		}
	}
	return hits >= 2
}
```

Aktivér i `toReaderModeHTML` (readermode.go), mellem `unwrapLayoutTables` og `stripJunkUnicode`:

```go
	if opts.Extract {
		pruneBulkContent(container)
	}
```

- [ ] **Step 4: Kør tests**

Run: `cd ~/Projects/emaildawg && go build ./... && go test ./pkg/email/ -v -run 'TestPruneBulk|TestReaderMode'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/email/extract.go pkg/email/extract_test.go pkg/email/readermode.go
git commit -m "feat(readermode): lag 2 — bulk-gated densitets-pruner med retention-guard"
```

---

### Task 3: IsBulk-detektion + wiring (ParsedEmail, finalizeHTML, config)

**Files:**
- Modify: `pkg/email/threading.go` (ParsedEmail +IsBulk)
- Modify: `pkg/email/processor.go` (header-detektion, Processor-felt, finalizeHTML-signatur, kaldssted ~1039)
- Modify: `pkg/connector/config.go` (+ReaderModeExtract), `pkg/connector/connector.go` (default + wiring)
- Test: `pkg/email/processor_bulk_test.go`

**Interfaces:**
- Consumes: `pruneBulkContent` via `readerModeOptions.Extract` (Task 2).
- Produces: `ParsedEmail.IsBulk bool`; `isBulkHeaders(h textproto.MIMEHeader) bool`; `Processor.ReaderModeExtract bool`; `finalizeHTML(origHTML string, isBulk bool)`.

- [ ] **Step 1: Skriv failing test**

```go
// pkg/email/processor_bulk_test.go
package email

import (
	"net/textproto"
	"testing"
)

func TestIsBulkHeaders(t *testing.T) {
	cases := []struct {
		name string
		h    textproto.MIMEHeader
		want bool
	}{
		{"list-unsubscribe", textproto.MIMEHeader{"List-Unsubscribe": {"<https://x.com/u>"}}, true},
		{"precedence bulk", textproto.MIMEHeader{"Precedence": {"bulk"}}, true},
		{"precedence list", textproto.MIMEHeader{"Precedence": {"list"}}, true},
		{"precedence first-class", textproto.MIMEHeader{"Precedence": {"first-class"}}, false},
		{"personal mail", textproto.MIMEHeader{"From": {"a@b.com"}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isBulkHeaders(c.h); got != c.want {
				t.Fatalf("want %v got %v", c.want, got)
			}
		})
	}
}
```

- [ ] **Step 2: Kør test — FAIL**

Run: `go test ./pkg/email/ -run TestIsBulkHeaders -v`
Expected: compile-fejl (isBulkHeaders undefined).

- [ ] **Step 3: Implementér**

`pkg/email/threading.go` — tilføj felt til ParsedEmail (efter `Attachments`):

```go
	// IsBulk marks newsletters/marketing (List-Unsubscribe or Precedence:
	// bulk/list) — gates reader-mode content extraction.
	IsBulk bool
```

`pkg/email/processor.go` — ny funktion ved siden af `extractReferencesFromHeaders`:

```go
// isBulkHeaders reports whether headers mark the mail as bulk (newsletter /
// marketing): List-Unsubscribe present, or Precedence: bulk/list.
func isBulkHeaders(h textproto.MIMEHeader) bool {
	if h.Get("List-Unsubscribe") != "" {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(h.Get("Precedence"))) {
	case "bulk", "list":
		return true
	}
	return false
}

// extractIsBulkFromHeaders scans header body-sections for bulk markers.
func (p *Processor) extractIsBulkFromHeaders(buf *imapclient.FetchMessageBuffer) bool {
	for _, section := range buf.BodySection {
		if section.Section.Specifier == imap.PartSpecifierHeader {
			headers, err := textproto.NewReader(bufio.NewReader(bytes.NewReader(section.Bytes))).ReadMIMEHeader()
			if err != nil {
				continue
			}
			if isBulkHeaders(headers) {
				return true
			}
		}
	}
	return false
}
```

I `parseIMAPFetchData`, efter References-udtrækket (linje ~276):

```go
	parsedEmail.IsBulk = p.extractIsBulkFromHeaders(buf)
```

Processor-felt (ved siden af `ReaderModeMinImgPx`, linje ~73):

```go
	// ReaderModeExtract enables bulk-mail content extraction (layer 2).
	ReaderModeExtract bool
```

`finalizeHTML` (linje ~1767) — ny signatur + Extract-option:

```go
func (p *Processor) finalizeHTML(origHTML string, isBulk bool) (formatted, plain string) {
	if p.ReaderMode {
		minPx := p.ReaderModeMinImgPx
		if minPx <= 0 {
			minPx = DefaultReaderModeMinImgPx
		}
		return toReaderModeHTML(origHTML, readerModeOptions{
			MinImgPx: minPx,
			Extract:  isBulk && p.ReaderModeExtract,
		})
	}
	formatted = filterInvisibleUnicode(html.UnescapeString(origHTML))
	return formatted, ""
}
```

Kaldssted (linje ~1039):

```go
		formatted, plain := e.processor.finalizeHTML(origHTML, e.emailMessage.IsBulk)
```

`pkg/connector/config.go` — tilføj til ProcessingConfig:

```go
	// ReaderModeExtract: prune footers/boilerplate in bulk mails (List-Unsubscribe). Layer 2 of reader mode.
	ReaderModeExtract bool `yaml:"reader_mode_extract"`
```

`pkg/connector/connector.go` — default (i `defaultProcessingConfig`) + wiring (efter linje ~210):

```go
		ReaderModeExtract:  true,
```

```go
	ec.Processor.ReaderModeExtract = ec.Config.Processing.ReaderModeExtract
```

- [ ] **Step 4: Byg + fuld test**

Run: `cd ~/Projects/emaildawg && go build ./... && go test ./pkg/email/ ./pkg/connector/`
Expected: PASS. (Bemærk: `grep -rn "finalizeHTML" pkg/` må kun vise den nye signatur + det opdaterede kaldssted.)

- [ ] **Step 5: Commit**

```bash
git add pkg/email/threading.go pkg/email/processor.go pkg/email/processor_bulk_test.go pkg/connector/config.go pkg/connector/connector.go
git commit -m "feat(processor): IsBulk-detektion (List-Unsubscribe/Precedence) + reader_mode_extract wiring"
```

---

### Task 4: Merge + deploy til Sliplane + live-verifikation

**Files:** ingen kodeændring.

- [ ] **Step 1: Fuld suite + merge til udrullet branch**

```bash
cd ~/Projects/emaildawg && go build ./... && go test ./... 2>&1 | tail -5
git checkout feat/graph-twoway && git merge --no-ff feat/reader-mode-v2 -m "merge: reader mode v2"
git push origin feat/graph-twoway
```

- [ ] **Step 2: Deploy på Sliplane**

Sliplane-servicen (`sh-emaildawg` under stefan-rosenlund) bygger fra GitHub. Brug `/sliplane`-skillet fra agent-stefan til at trigge redeploy af emaildawg-servicen og verificér at deployment går grønt.

- [ ] **Step 3: Live-verifikation (næste nyhedsbrev + næste personlige mail)**

1. Vent på næste bulk-mail (nyhedsbrev) → i Beeper: ingen `͏ ­`-junk, ingen skjult preheader-tekst, footer/unsubscribe/social væk, overskrifter ikke-skrigende.
2. Vent på næste personlige/transaktionsmail → indhold 100% intakt (kun hygiejne anvendt).
3. Ved fejl: `reader_mode_extract: false` i bridge-config er kill-switch for Lag 2.

---

## Self-Review

**Spec-coverage:** Lag 1 hygiejne (4 passes) → Task 1. Lag 2 pruner + gate + guard + recover → Task 2 (pruner/guard/recover) + Task 3 (gate/IsBulk/config). Lag 3 polish (heading-demotion, noise-collapse) → Task 1. Wiring (ParsedEmail, finalizeHTML, config) → Task 3. Test-fixtures fra spec → Task 1+2+3 tests dækker preheader-padding, hidden text, dobbelt-entities, `____`→hr, br-kollaps, h1→h3, footer-prune, retention-guard, non-bulk-untouched, IsBulk-headere. Deploy → Task 4. Ingen gaps.

**Placeholder-scan:** Ingen TBD/TODO; al kode komplet. Task 1's pipeline-snippet viser `pruneBulkContent`-kaldet som Task 2-aktiveret — eksplicit markeret, ikke en placeholder.

**Type-konsistens:** `readerModeOptions{MinImgPx, Extract}` ens i Task 1/2/3. `pruneBulkContent(container *html.Node)` defineret Task 2, kaldt Task 2 (readermode.go). `isBulkHeaders(textproto.MIMEHeader) bool` defineret + testet Task 3. `finalizeHTML(string, bool)` signatur ens i definition + kaldssted. `textOf`/`hasDescendant` defineret Task 1/eksisterende, brugt Task 2. Imports: processor.go har allerede `bufio`, `bytes`, `textproto`, `imap` (bruges af extractReferencesFromHeaders).
