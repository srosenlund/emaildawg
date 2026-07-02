package email

import (
	"bytes"
	htmlpkg "html"
	"regexp"
	"strconv"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// DefaultReaderModeMinImgPx is the dimension (px) at or below which an inline
// image is treated as a tracking pixel / spacer / tiny logo and dropped.
const DefaultReaderModeMinImgPx = 32

// matrixPolicy whitelists HTML down to the subset Matrix clients (Beeper)
// render reliably (org.matrix.custom.html), and hardens against XSS.
var matrixPolicy = newMatrixPolicy()

func newMatrixPolicy() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowElements(
		"p", "br", "div", "span",
		"b", "strong", "i", "em", "u", "del", "s",
		"h1", "h2", "h3", "h4", "h5", "h6",
		"blockquote", "ul", "ol", "li",
		"pre", "code", "hr",
		"table", "thead", "tbody", "tr", "th", "td",
	)
	p.AllowElements("a")
	p.AllowAttrs("href").OnElements("a")
	p.AllowElements("img")
	p.AllowAttrs("src", "alt", "title").OnElements("img")
	p.AllowURLSchemes("http", "https", "mailto", "mxc")
	// Drop the textual content of these elements entirely, not just the tags.
	p.SkipElementsContent("script", "style", "head", "title", "noscript")
	return p
}

// sanitizeMatrixHTML reduces arbitrary HTML to the Matrix-safe subset.
func sanitizeMatrixHTML(s string) string {
	return matrixPolicy.Sanitize(s)
}

type readerModeOptions struct {
	MinImgPx int
	// Extract enables layer-2 content extraction (bulk mails only).
	Extract bool
}

// toReaderModeHTML parses post-MXC email HTML, linearises newsletter layout
// and strips tracking imagery, then whitelists the result for Matrix. It also
// returns a plaintext rendering for the message body fallback.
func toReaderModeHTML(htmlStr string, opts readerModeOptions) (cleanHTML, plainText string) {
	nodes, err := html.ParseFragment(strings.NewReader(htmlStr), &html.Node{
		Type:     html.ElementNode,
		Data:     "body",
		DataAtom: atom.Body,
	})
	// Defensive: ParseFragment over a strings.Reader has no I/O error path, so this is currently unreachable.
	if err != nil {
		clean := sanitizeMatrixHTML(htmlStr)
		return clean, simpleHTMLToText(clean)
	}

	container := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
	for _, n := range nodes {
		container.AppendChild(n)
	}

	dropHiddenElements(container)
	dropTrackingImages(container, opts.MinImgPx)
	unwrapLayoutTables(container)
	if opts.Extract {
		pruneBulkContent(container)
	}
	stripJunkUnicode(container)
	decodeStableEntities(container)
	collapseNoise(container)
	demoteHeadings(container)

	var buf bytes.Buffer
	for c := container.FirstChild; c != nil; c = c.NextSibling {
		_ = html.Render(&buf, c)
	}
	cleanHTML = sanitizeMatrixHTML(buf.String())
	plainText = simpleHTMLToText(cleanHTML)
	return cleanHTML, plainText
}

var styleDimRe = regexp.MustCompile(`(?i)(width|height)\s*:\s*(\d+)\s*px`)

// dropTrackingImages removes <img> nodes that look like tracking pixels,
// spacers, or tiny logos (dimension <= minPx, exact 1x1, or display:none).
func dropTrackingImages(root *html.Node, minPx int) {
	var toRemove []*html.Node
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode && c.DataAtom == atom.Img && isTrackingImage(c, minPx) {
				toRemove = append(toRemove, c)
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

func isTrackingImage(img *html.Node, minPx int) bool {
	w, h := -1, -1
	var style string
	for _, a := range img.Attr {
		switch strings.ToLower(a.Key) {
		case "width":
			w = parsePixels(a.Val)
		case "height":
			h = parsePixels(a.Val)
		case "style":
			style = strings.ToLower(a.Val)
		}
	}
	if strings.Contains(style, "display:none") || strings.Contains(style, "display: none") {
		return true
	}
	if sw := styleDimension(style, "width"); sw >= 0 {
		w = sw
	}
	if sh := styleDimension(style, "height"); sh >= 0 {
		h = sh
	}
	if w >= 0 && w <= minPx {
		return true
	}
	if h >= 0 && h <= minPx {
		return true
	}
	return false
}

// parsePixels parses "16" or "16px" to an int; returns -1 for non-pixel
// values such as "100%" or empty strings (treated as "unknown", i.e. keep).
func parsePixels(s string) int {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.TrimSuffix(s, "px")
	s = strings.TrimSpace(s)
	if s == "" {
		return -1
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return -1
	}
	return n
}

func styleDimension(style, prop string) int {
	for _, m := range styleDimRe.FindAllStringSubmatch(style, -1) {
		if strings.EqualFold(m[1], prop) {
			if n, err := strconv.Atoi(m[2]); err == nil {
				return n
			}
		}
	}
	return -1
}

// unwrapLayoutTables linearises tables used purely for layout (role=presentation
// or no <th>) into document flow, while leaving genuine data tables intact.
// Tables are processed deepest-first so nested layout tables flatten fully.
func unwrapLayoutTables(root *html.Node) {
	var tables []*html.Node
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
			if c.Type == html.ElementNode && c.DataAtom == atom.Table {
				tables = append(tables, c) // post-order: children appended before parents
			}
		}
	}
	walk(root)
	for _, t := range tables {
		if isLayoutTable(t) {
			linearizeTable(t)
		}
	}
}

func isLayoutTable(table *html.Node) bool {
	for _, a := range table.Attr {
		if strings.EqualFold(a.Key, "role") && strings.EqualFold(strings.TrimSpace(a.Val), "presentation") {
			return true
		}
	}
	return !hasDescendant(table, atom.Th)
}

func hasDescendant(n *html.Node, target atom.Atom) bool {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.DataAtom == target {
			return true
		}
		if hasDescendant(c, target) {
			return true
		}
	}
	return false
}

// linearizeTable replaces a layout table with the child nodes of its cells,
// in document order, dropping the table/tbody/tr/td wrappers.
func linearizeTable(table *html.Node) {
	parent := table.Parent
	if parent == nil {
		return
	}
	var content []*html.Node
	var collect func(n *html.Node)
	collect = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode && (c.DataAtom == atom.Td || c.DataAtom == atom.Th) {
				for child := c.FirstChild; child != nil; child = child.NextSibling {
					content = append(content, child)
				}
			} else {
				collect(c)
			}
		}
	}
	collect(table)

	for _, n := range content {
		if n.Parent != nil {
			n.Parent.RemoveChild(n)
		}
	}
	for _, n := range content {
		parent.InsertBefore(n, table)
	}
	parent.RemoveChild(table)
}

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

// isJunkRune reports preheader-padding characters. Deliberately a conservative
// set — NOT all of Cf/Mn, which would damage legitimate combining marks in
// non-Latin text: U+034F, U+00AD, U+200B–U+200F, U+2060, U+FEFF.
func isJunkRune(r rune) bool {
	switch {
	case r == 0x034F, r == 0x00AD, r == 0x2060, r == 0xFEFF:
		return true
	case r >= 0x200B && r <= 0x200F:
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
	// br-runs: fjern br nr. 3+ i en ubrudt (whitespace-tolerant) kæde
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

	// separator-runs i tekst-noder → <hr> hvis noden kun er separatoren
	walkTextNodes(root, func(n *html.Node) {
		trimmed := strings.TrimSpace(n.Data)
		if trimmed != "" && separatorRunRe.ReplaceAllString(trimmed, "") == "" {
			n.Data = ""
			if n.Parent != nil {
				hr := &html.Node{Type: html.ElementNode, Data: "hr", DataAtom: atom.Hr}
				n.Parent.InsertBefore(hr, n)
			}
		} else {
			n.Data = separatorRunRe.ReplaceAllString(n.Data, "")
		}
	})

	// tomme p/div (ingen tekst, ingen img/a/hr/br) fjernes
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
