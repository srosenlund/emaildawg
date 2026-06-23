package email

import (
	"bytes"
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
	if err != nil {
		clean := sanitizeMatrixHTML(htmlStr)
		return clean, simpleHTMLToText(clean)
	}

	container := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
	for _, n := range nodes {
		container.AppendChild(n)
	}

	dropTrackingImages(container, opts.MinImgPx)
	unwrapLayoutTables(container)

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

func hasDescendant(n *html.Node, a atom.Atom) bool {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.DataAtom == a {
			return true
		}
		if hasDescendant(c, a) {
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
