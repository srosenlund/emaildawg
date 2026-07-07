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

// descendAtoms: wrapper-elementer pruningRoot må descende igennem. Bevidst KUN
// generiske containere — aldrig tabel-struktur (rækker i en data-tabel må ikke
// scores som blokke) og aldrig tekst-elementer som <p> (inline-anchors må ikke
// scores som blokke).
var descendAtoms = map[atom.Atom]bool{
	atom.Div: true, atom.Center: true, atom.Section: true,
	atom.Article: true, atom.Main: true, atom.Body: true,
}

// pruningRoot descends through single-wrapper chains (Outlook-mails lægger alt
// indhold i ét <center>/<div>) så scoringen rammer de reelle indholdsblokke.
// Descend kun når wrapperen bærer stort set al renderet tekst — tekst-noder
// direkte under den nuværende root må ikke miste kontekst.
func pruningRoot(container *html.Node) *html.Node {
	root := container
	rootLen := -1 // beregnes lazily; genbruges som forrige iterations wrapLen
	for depth := 0; depth < 10; depth++ {
		var only *html.Node
		n := 0
		for c := root.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode {
				only = c
				n++
			}
		}
		if n != 1 || !descendAtoms[only.DataAtom] {
			break
		}
		if rootLen < 0 {
			rootLen = len(strings.TrimSpace(textOf(root)))
		}
		wrapLen := len(strings.TrimSpace(textOf(only)))
		if rootLen == 0 || float64(wrapLen) < 0.95*float64(rootLen) {
			break
		}
		root = only
		rootLen = wrapLen
	}
	return root
}

// pruneBulkContent removes footer/nav/social boilerplate from bulk email
// (newsletters). Operates on the post-linearisation tree (non-rendered
// elements like <style> are already stripped at pipeline entry): scores the
// pruning root's child blocks and drops low-value ones. Retention guard and
// panic recovery guarantee it never destroys a mail.
func pruneBulkContent(container *html.Node) {
	defer func() {
		_ = recover() // en pruner-fejl må aldrig tabe en mail; Lag 1-træet består
	}()

	root := pruningRoot(container)

	type block struct {
		node *html.Node
		text string
		drop bool
	}
	var blocks []block
	totalLen := 0
	textNodeLen := 0 // tekst direkte under root — kan aldrig droppes, men skal tælle i guarden
	for c := root.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			textNodeLen += len(strings.TrimSpace(c.Data))
			continue
		}
		if c.Type != html.ElementNode {
			continue
		}
		txt := strings.TrimSpace(textOf(c))
		blocks = append(blocks, block{node: c, text: txt})
		totalLen += len(txt)
	}
	totalLen += textNodeLen
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
		ld := linkDensity(b.node, len(b.text))
		kwHit := false
		for _, kw := range footerKeywords {
			if strings.Contains(lower, kw) {
				kwHit = true
				break
			}
		}
		linkSoup := ld > 0.5 && len(b.text) < 200

		// Ren link-suppe uden keyword/social-hit fældes ALDRIG — en trailing
		// CTA ("Read the full story") er ofte hovedindholdet, og rigtige
		// footer-rækker rammer altid keywords eller social-navne.
		switch {
		case kwHit && (i >= bottomZone || linkSoup):
			b.drop = true
		case ld > 0.8 && isSocialRow(lower):
			b.drop = true
		}
	}

	kept := textNodeLen
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

// linkDensity = linked characters / total rendered characters in the block.
// total er blokkens allerede-beregnede tekstlængde (undgår et ekstra tree-walk).
func linkDensity(n *html.Node, total int) float64 {
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
