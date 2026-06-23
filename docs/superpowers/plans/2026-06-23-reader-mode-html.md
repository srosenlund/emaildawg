# Reader-mode HTML Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Render incoming email HTML as clean reader-mode HTML for Matrix/Beeper — strip layout tables, tracking/spacer images, and inline CSS while preserving headings, text, lists, links and meaningful images.

**Architecture:** A new pure function `toReaderModeHTML` parses the post-MXC HTML with `golang.org/x/net/html`, runs two structural DOM transforms (drop tracking images, unwrap layout tables), then whitelists the result to the Matrix tag subset with `bluemonday`. It is invoked from `ConvertMessage()` via a thin `Processor.finalizeHTML` seam, gated by config (default on).

**Tech Stack:** Go, `golang.org/x/net/html` (already an indirect dep), `github.com/microcosm-cc/bluemonday` (new), standard `testing`.

## Global Constraints

- Target Go 1.23+ (prod toolchain 1.24.6). No new language features beyond that floor.
- Tests use the standard `testing` package only — NO testify. Assert with `t.Fatalf("want %q, got %q", want, got)`.
- Output stays **HTML** in `content.FormattedBody`. Do not switch the pipeline to Markdown.
- Reader-mode defaults **ON**. Image-drop threshold default **32 px** (`DefaultReaderModeMinImgPx = 32`).
- Follow the existing YAML-config pattern (`ProcessingConfig` in `pkg/connector/config.go`, defaults set in `NewEmailConnector`, applied to the processor in `connector.go`). Do NOT introduce `os.Getenv` for these settings.
- Package `email` must not import package `connector` (the dependency direction is connector → email). Shared constants live in package `email`.
- Run all tests with `make test` (wraps `go test ./...`). A single package: `go test ./pkg/email` or `./pkg/connector`.

---

### Task 1: Matrix whitelist policy

**Files:**
- Create: `pkg/email/readermode.go`
- Test: `pkg/email/readermode_test.go`

**Interfaces:**
- Produces: `func sanitizeMatrixHTML(s string) string` — whitelists an HTML string to the Matrix-supported subset; `var matrixPolicy *bluemonday.Policy`; `const DefaultReaderModeMinImgPx = 32`.

- [ ] **Step 1: Add the bluemonday dependency**

Run:
```bash
cd /Users/stefanrosenlund/Projects/emaildawg
go get github.com/microcosm-cc/bluemonday@latest
```
Expected: `go.mod` gains `github.com/microcosm-cc/bluemonday` as a direct require.

- [ ] **Step 2: Write the failing test**

Create `pkg/email/readermode_test.go`:
```go
package email

import (
	"strings"
	"testing"
)

func TestSanitizeMatrixHTML(t *testing.T) {
	cases := []struct {
		name           string
		in             string
		mustContain    []string
		mustNotContain []string
	}{
		{"strips script", `<p>hi</p><script>alert(1)</script>`, []string{"hi"}, []string{"alert", "<script"}},
		{"strips style block", `<style>.x{color:red}</style><p>hi</p>`, []string{"hi"}, []string{"color:red", "<style"}},
		{"strips style attr", `<p style="color:red">x</p>`, []string{"x"}, []string{"style", "color:red"}},
		{"strips bgcolor", `<td bgcolor="#fff">y</td>`, []string{"y"}, []string{"bgcolor"}},
		{"keeps link", `<a href="https://x.com">l</a>`, []string{`href="https://x.com"`}, nil},
		{"keeps mxc img", `<img src="mxc://h/abc" alt="a">`, []string{"mxc://h/abc"}, nil},
		{"keeps basic formatting", `<strong>b</strong><ul><li>i</li></ul>`, []string{"<strong>b</strong>", "<li>i</li>"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sanitizeMatrixHTML(c.in)
			for _, s := range c.mustContain {
				if !strings.Contains(got, s) {
					t.Fatalf("want contains %q, got %q", s, got)
				}
			}
			for _, s := range c.mustNotContain {
				if strings.Contains(got, s) {
					t.Fatalf("want NOT contains %q, got %q", s, got)
				}
			}
		})
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./pkg/email -run TestSanitizeMatrixHTML`
Expected: FAIL — `undefined: sanitizeMatrixHTML`.

- [ ] **Step 4: Write minimal implementation**

Create `pkg/email/readermode.go`:
```go
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
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./pkg/email -run TestSanitizeMatrixHTML -v`
Expected: PASS (all subtests).

- [ ] **Step 6: Commit**

```bash
cd /Users/stefanrosenlund/Projects/emaildawg
git add go.mod go.sum pkg/email/readermode.go pkg/email/readermode_test.go
git commit -m "feat(reader-mode): Matrix HTML whitelist policy"
```

---

### Task 2: Reader-mode orchestrator (parse → transform stubs → sanitize)

**Files:**
- Modify: `pkg/email/readermode.go`
- Test: `pkg/email/readermode_test.go`

**Interfaces:**
- Consumes: `sanitizeMatrixHTML`, `simpleHTMLToText` (existing, `pkg/email/processor.go:1688`).
- Produces: `type readerModeOptions struct { MinImgPx int }`; `func toReaderModeHTML(htmlStr string, opts readerModeOptions) (cleanHTML, plainText string)`; stub funcs `dropTrackingImages(root *html.Node, minPx int)` and `unwrapLayoutTables(root *html.Node)`.

- [ ] **Step 1: Write the failing test**

Append to `pkg/email/readermode_test.go`:
```go
func TestToReaderModeHTML_PassThrough(t *testing.T) {
	in := `<p>Hello <strong>world</strong></p><a href="https://x.com">link</a>`
	clean, plain := toReaderModeHTML(in, readerModeOptions{MinImgPx: 32})
	if !strings.Contains(clean, "<strong>world</strong>") {
		t.Fatalf("formatting lost: %q", clean)
	}
	if !strings.Contains(clean, `href="https://x.com"`) {
		t.Fatalf("link lost: %q", clean)
	}
	if strings.Contains(clean, "<html") || strings.Contains(clean, "<body") {
		t.Fatalf("document wrapper leaked: %q", clean)
	}
	if !strings.Contains(plain, "Hello world") || !strings.Contains(plain, "link") {
		t.Fatalf("plaintext wrong: %q", plain)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/email -run TestToReaderModeHTML_PassThrough`
Expected: FAIL — `undefined: toReaderModeHTML`.

- [ ] **Step 3: Write minimal implementation**

Add to the imports block of `pkg/email/readermode.go`:
```go
import (
	"bytes"

	"github.com/microcosm-cc/bluemonday"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)
```

Append to `pkg/email/readermode.go`:
```go
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
```

Add `"strings"` to the import block (used by `strings.NewReader`).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/email -run TestToReaderModeHTML_PassThrough -v`
Expected: PASS.

- [ ] **Step 5: Tidy and verify the build**

Run:
```bash
go mod tidy
go build ./...
```
Expected: clean build; `golang.org/x/net` becomes a direct require.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum pkg/email/readermode.go pkg/email/readermode_test.go
git commit -m "feat(reader-mode): orchestrator with transform stubs"
```

---

### Task 3: Drop tracking / spacer / tiny images

**Files:**
- Modify: `pkg/email/readermode.go`
- Test: `pkg/email/readermode_test.go`

**Interfaces:**
- Produces: real `dropTrackingImages(root *html.Node, minPx int)`; helpers `isTrackingImage(img *html.Node, minPx int) bool`, `parsePixels(s string) int`, `styleDimension(style, prop string) int`.

- [ ] **Step 1: Write the failing test**

Append to `pkg/email/readermode_test.go`:
```go
func TestToReaderModeHTML_Images(t *testing.T) {
	opts := readerModeOptions{MinImgPx: 32}
	cases := []struct {
		name string
		in   string
		keep bool
	}{
		{"1x1 tracking", `<img src="mxc://h/track" width="1" height="1">`, false},
		{"tiny logo 16px", `<img src="mxc://h/logo" width="16" height="16">`, false},
		{"threshold 32px", `<img src="mxc://h/edge" width="32" height="32">`, false},
		{"display none", `<img src="mxc://h/x" style="display:none">`, false},
		{"style px tiny", `<img src="mxc://h/s" style="width:2px;height:2px">`, false},
		{"hero no dims", `<img src="mxc://h/hero">`, true},
		{"hero 600px", `<img src="mxc://h/big" width="600" height="400">`, true},
		{"percent width", `<img src="mxc://h/pct" width="100%">`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := toReaderModeHTML(c.in, opts)
			has := strings.Contains(got, "mxc://h/")
			if has != c.keep {
				t.Fatalf("keep=%v, got html=%q", c.keep, got)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/email -run TestToReaderModeHTML_Images`
Expected: FAIL — e.g. tracking pixel still present (stub is a no-op).

- [ ] **Step 3: Write the implementation**

Add `"regexp"` and `"strconv"` to the import block of `pkg/email/readermode.go`.

Replace the `dropTrackingImages` stub with:
```go
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/email -run TestToReaderModeHTML_Images -v`
Expected: PASS (all subtests).

- [ ] **Step 5: Commit**

```bash
git add pkg/email/readermode.go pkg/email/readermode_test.go
git commit -m "feat(reader-mode): drop tracking and spacer images"
```

---

### Task 4: Unwrap layout tables (keep data tables)

**Files:**
- Modify: `pkg/email/readermode.go`
- Test: `pkg/email/readermode_test.go`

**Interfaces:**
- Produces: real `unwrapLayoutTables(root *html.Node)`; helpers `isLayoutTable(table *html.Node) bool`, `hasDescendant(n *html.Node, a atom.Atom) bool`, `linearizeTable(table *html.Node)`.

- [ ] **Step 1: Write the failing test**

Append to `pkg/email/readermode_test.go`:
```go
func TestToReaderModeHTML_Tables(t *testing.T) {
	opts := readerModeOptions{MinImgPx: 32}

	// role=presentation layout table -> unwrapped, content preserved.
	layout := `<table role="presentation"><tr><td><p>Hello</p></td></tr>` +
		`<tr><td><p>World</p></td></tr></table>`
	got, _ := toReaderModeHTML(layout, opts)
	if strings.Contains(got, "<table") {
		t.Fatalf("layout table not unwrapped: %q", got)
	}
	if !strings.Contains(got, "Hello") || !strings.Contains(got, "World") {
		t.Fatalf("layout content lost: %q", got)
	}

	// No <th> -> treated as layout, unwrapped.
	noTh := `<table><tr><td><p>A</p></td><td><p>B</p></td></tr></table>`
	got2, _ := toReaderModeHTML(noTh, opts)
	if strings.Contains(got2, "<table") {
		t.Fatalf("headerless table not unwrapped: %q", got2)
	}
	if !strings.Contains(got2, "A") || !strings.Contains(got2, "B") {
		t.Fatalf("headerless content lost: %q", got2)
	}

	// Data table with <th> -> preserved.
	data := `<table><tr><th>Name</th><th>Age</th></tr>` +
		`<tr><td>Ann</td><td>30</td></tr></table>`
	got3, _ := toReaderModeHTML(data, opts)
	if !strings.Contains(got3, "<table") {
		t.Fatalf("data table dropped: %q", got3)
	}
	if !strings.Contains(got3, "Ann") || !strings.Contains(got3, "Name") {
		t.Fatalf("data content lost: %q", got3)
	}

	// Nested layout tables flatten completely.
	nested := `<table role="presentation"><tr><td>` +
		`<table role="presentation"><tr><td><p>Deep</p></td></tr></table>` +
		`</td></tr></table>`
	got4, _ := toReaderModeHTML(nested, opts)
	if strings.Contains(got4, "<table") {
		t.Fatalf("nested layout not flattened: %q", got4)
	}
	if !strings.Contains(got4, "Deep") {
		t.Fatalf("nested content lost: %q", got4)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/email -run TestToReaderModeHTML_Tables`
Expected: FAIL — tables still present (stub is a no-op).

- [ ] **Step 3: Write the implementation**

Replace the `unwrapLayoutTables` stub with:
```go
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/email -run TestToReaderModeHTML_Tables -v`
Expected: PASS (all subtests).

- [ ] **Step 5: Run the full package suite**

Run: `go test ./pkg/email`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/email/readermode.go pkg/email/readermode_test.go
git commit -m "feat(reader-mode): unwrap layout tables, keep data tables"
```

---

### Task 5: Config wiring (default-on)

**Files:**
- Modify: `pkg/connector/config.go:68-77` (the `ProcessingConfig` struct + defaults const)
- Modify: `pkg/connector/connector.go:102-105` (defaults) and `pkg/connector/connector.go:186-189` (apply to processor)
- Modify: `pkg/email/processor.go` (add fields to the `Processor` struct)
- Test: `pkg/connector/connector_test.go`

**Interfaces:**
- Produces: `ProcessingConfig.ReaderMode bool`, `ProcessingConfig.ReaderModeMinImgPx int`; `Processor.ReaderMode bool`, `Processor.ReaderModeMinImgPx int` (consumed by Task 6).

- [ ] **Step 1: Add fields to the Processor struct**

In `pkg/email/processor.go`, locate the `type Processor struct { ... }` definition (it already holds `MaxUploadBytes int` and `GzipLargeBodies bool`). Add next to them:
```go
	ReaderMode         bool
	ReaderModeMinImgPx int
```

- [ ] **Step 2: Add fields to ProcessingConfig**

In `pkg/connector/config.go`, extend the `ProcessingConfig` struct (currently l. 68-77):
```go
type ProcessingConfig struct {
	MaxUploadBytes     int  `yaml:"max_upload_bytes"`
	GzipLargeBodies    bool `yaml:"gzip_large_bodies"`
	ReaderMode         bool `yaml:"reader_mode"`
	ReaderModeMinImgPx int  `yaml:"reader_mode_min_img_px"`
}
```

- [ ] **Step 3: Set defaults in NewEmailConnector**

In `pkg/connector/connector.go` (defaults block, ~l. 102-105), extend the `ProcessingConfig` literal:
```go
		Processing: ProcessingConfig{
			MaxUploadBytes:     DefaultMaxUploadBytes,
			GzipLargeBodies:    true,
			ReaderMode:         true,
			ReaderModeMinImgPx: email.DefaultReaderModeMinImgPx,
		},
```
Ensure `pkg/email` is imported in `connector.go` (it already references the processor type, so the import exists — confirm the alias used for the package and prefix `DefaultReaderModeMinImgPx` accordingly).

- [ ] **Step 4: Apply config to the processor**

In `pkg/connector/connector.go` (~l. 186-189, where `MaxUploadBytes`/`GzipLargeBodies` are applied), add:
```go
	ec.Processor.ReaderMode = ec.Config.Processing.ReaderMode
	ec.Processor.ReaderModeMinImgPx = ec.Config.Processing.ReaderModeMinImgPx
	if ec.Processor.ReaderModeMinImgPx <= 0 {
		ec.Processor.ReaderModeMinImgPx = email.DefaultReaderModeMinImgPx
	}
```

- [ ] **Step 5: Write the failing test**

Create `pkg/connector/connector_test.go` (if it does not exist; otherwise append the function). Use the existing package name of other files in `pkg/connector`:
```go
package connector

import (
	"testing"

	"github.com/your-org/emaildawg/pkg/email" // adjust to the module's actual import path
)

func TestNewEmailConnector_ReaderModeDefaults(t *testing.T) {
	ec := NewEmailConnector()
	if !ec.Config.Processing.ReaderMode {
		t.Fatalf("ReaderMode should default to true")
	}
	if ec.Config.Processing.ReaderModeMinImgPx != email.DefaultReaderModeMinImgPx {
		t.Fatalf("ReaderModeMinImgPx default = %d, want %d",
			ec.Config.Processing.ReaderModeMinImgPx, email.DefaultReaderModeMinImgPx)
	}
}
```
First confirm the module path: `head -1 go.mod` gives the `module` line; replace `github.com/your-org/emaildawg` with `<module>/pkg/email`.

- [ ] **Step 6: Run test to verify it fails, then passes**

Run: `go test ./pkg/connector -run TestNewEmailConnector_ReaderModeDefaults -v`
Expected: with the fields/defaults in place from Steps 1-4, PASS. If it fails to compile first (fields not yet wired), fix the wiring until PASS.

- [ ] **Step 7: Commit**

```bash
git add pkg/connector/config.go pkg/connector/connector.go pkg/email/processor.go pkg/connector/connector_test.go
git commit -m "feat(reader-mode): config plumbing, default on"
```

---

### Task 6: Integrate into ConvertMessage

**Files:**
- Modify: `pkg/email/processor.go` (add `finalizeHTML` method; replace the FormattedBody block at ~l. 1024-1030)
- Test: `pkg/email/processor_test.go`

**Interfaces:**
- Consumes: `Processor.ReaderMode`, `Processor.ReaderModeMinImgPx`, `toReaderModeHTML`, existing `filterInvisibleUnicode` (`processor.go:1710`) and `html.UnescapeString`.
- Produces: `func (p *Processor) finalizeHTML(origHTML string) (formatted, plain string)`.

- [ ] **Step 1: Write the failing test**

Create `pkg/email/processor_test.go`:
```go
package email

import (
	"strings"
	"testing"
)

func TestProcessor_finalizeHTML_ReaderModeOn(t *testing.T) {
	p := &Processor{ReaderMode: true, ReaderModeMinImgPx: 32}
	in := `<table role="presentation"><tr><td><p>Hi there</p></td></tr></table>` +
		`<img src="mxc://h/track" width="1" height="1"><script>x()</script>`
	formatted, plain := p.finalizeHTML(in)
	if strings.Contains(formatted, "<table") {
		t.Fatalf("layout table survived: %q", formatted)
	}
	if strings.Contains(formatted, "<script") || strings.Contains(formatted, "x()") {
		t.Fatalf("script survived: %q", formatted)
	}
	if strings.Contains(formatted, "mxc://h/track") {
		t.Fatalf("tracking pixel survived: %q", formatted)
	}
	if !strings.Contains(plain, "Hi there") {
		t.Fatalf("plaintext missing content: %q", plain)
	}
}

func TestProcessor_finalizeHTML_ReaderModeOff(t *testing.T) {
	p := &Processor{ReaderMode: false}
	formatted, plain := p.finalizeHTML(`<p>raw &amp; co</p>`)
	if plain != "" {
		t.Fatalf("legacy path should not set plain, got %q", plain)
	}
	if !strings.Contains(formatted, "raw & co") {
		t.Fatalf("legacy unescape failed: %q", formatted)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/email -run TestProcessor_finalizeHTML`
Expected: FAIL — `p.finalizeHTML undefined`.

- [ ] **Step 3: Add the finalizeHTML method**

In `pkg/email/processor.go`, add (near the other `*Processor` methods):
```go
// finalizeHTML produces the Matrix formatted body (and, in reader mode, a
// cleaned plaintext fallback) from the post-MXC email HTML.
func (p *Processor) finalizeHTML(origHTML string) (formatted, plain string) {
	if p.ReaderMode {
		minPx := p.ReaderModeMinImgPx
		if minPx <= 0 {
			minPx = DefaultReaderModeMinImgPx
		}
		return toReaderModeHTML(origHTML, readerModeOptions{MinImgPx: minPx})
	}
	// Legacy path: decode entities + strip invisible Unicode, no plaintext rewrite.
	formatted = filterInvisibleUnicode(html.UnescapeString(origHTML))
	return formatted, ""
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/email -run TestProcessor_finalizeHTML -v`
Expected: PASS.

- [ ] **Step 5: Wire finalizeHTML into ConvertMessage**

In `pkg/email/processor.go`, replace the existing block (~l. 1024-1030):
```go
	// Add HTML formatting if available
	if origHTML != "" && origHTML != e.emailMessage.TextContent {
		content.Format = event.FormatHTML
		// Decode HTML entities in the formatted body before sending to Matrix
		content.FormattedBody = html.UnescapeString(origHTML)
		// Filter out invisible Unicode characters from HTML content too
		content.FormattedBody = filterInvisibleUnicode(content.FormattedBody)
	}
```
with:
```go
	// Add HTML formatting if available
	if origHTML != "" && origHTML != e.emailMessage.TextContent {
		content.Format = event.FormatHTML
		formatted, plain := e.processor.finalizeHTML(origHTML)
		content.FormattedBody = formatted
		// In reader mode, prefer the cleaned plaintext as the body fallback.
		if plain != "" {
			content.Body = plain
		}
	}
```

- [ ] **Step 6: Verify build and full suite**

Run:
```bash
go build ./...
make test
```
Expected: clean build; all tests PASS.

- [ ] **Step 7: Commit**

```bash
git add pkg/email/processor.go pkg/email/processor_test.go
git commit -m "feat(reader-mode): wire finalizeHTML into ConvertMessage"
```

---

## Self-Review

**Spec coverage:**
- Module boundary / pure function → Task 2 (`toReaderModeHTML`), Task 6 (`finalizeHTML` seam). ✓
- Insertion after MXC rewrite, before FormattedBody → Task 6 Step 5. ✓
- Drop tracking/spacer images by dimension → Task 3. ✓
- Unwrap layout tables, keep data tables → Task 4. ✓
- Whitelist to Matrix subset + XSS hardening → Task 1. ✓
- Plaintext fallback from cleaned HTML → Task 2 (`simpleHTMLToText`), Task 6 (assigned to `content.Body`). ✓
- bluemonday dep → Task 1 Step 1. ✓
- Config `ReaderMode` (default on) + `ReaderModeMinImgPx` (default 32) → Task 5. ✓
- Tests with fixtures (newsletter/data-table/plain) → Tasks 3, 4, 6. ✓
- READER_MODE off = pass-through to legacy → Task 6 (`ReaderModeOff` test). ✓
- Out of scope: quoted-reply/signature trimming, Graph path → not implemented, by design. ✓

**Placeholder scan:** No TBD/TODO; every code step shows complete code. The two transform stubs in Task 2 are intentional and replaced in Tasks 3-4. The connector test's import path and the `Processor` struct field location require a one-line confirmation against the actual module (`head -1 go.mod`) and struct — flagged inline, not left vague.

**Type consistency:** `toReaderModeHTML(string, readerModeOptions) (string, string)`, `readerModeOptions{MinImgPx int}`, `dropTrackingImages(*html.Node, int)`, `unwrapLayoutTables(*html.Node)`, `finalizeHTML(string) (string, string)`, `DefaultReaderModeMinImgPx = 32` — all referenced consistently across tasks.
