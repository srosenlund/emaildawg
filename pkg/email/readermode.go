package email

import (
	"bytes"
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

// dropTrackingImages is implemented in Task 3.
func dropTrackingImages(root *html.Node, minPx int) {}

// unwrapLayoutTables is implemented in Task 4.
func unwrapLayoutTables(root *html.Node) {}
