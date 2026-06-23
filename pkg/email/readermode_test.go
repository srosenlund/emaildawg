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
