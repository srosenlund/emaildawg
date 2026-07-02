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
		// Rene social-rækker
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
