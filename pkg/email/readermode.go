package email

import (
	"github.com/microcosm-cc/bluemonday"
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
