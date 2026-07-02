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
		{"strips class attr", `<p class="x">y</p>`, []string{"y"}, []string{"class"}},
		{"drops javascript href", `<a href="javascript:alert(1)">x</a>`, []string{"x"}, []string{"javascript:"}},
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

func TestToReaderModeHTML_DataTableInsideLayout(t *testing.T) {
	in := `<table role="presentation"><tr><td>` +
		`<table><tr><th>Name</th></tr><tr><td>Ann</td></tr></table>` +
		`</td></tr></table>`
	got, _ := toReaderModeHTML(in, readerModeOptions{MinImgPx: 32})
	if !strings.Contains(got, "<table") {
		t.Fatalf("nested data table was dropped: %q", got)
	}
	if !strings.Contains(got, "Name") || !strings.Contains(got, "Ann") {
		t.Fatalf("nested data table content lost: %q", got)
	}
}

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
	_, plain := toReaderModeHTML(in, readerModeOptions{MinImgPx: 32})
	if strings.Contains(plain, "&quot;") || strings.Contains(plain, "&lt;") {
		t.Fatalf("entities not decoded: %q", plain)
	}
	if !strings.Contains(plain, `"Daniel"`) {
		t.Fatalf("expected decoded quotes: %q", plain)
	}
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
