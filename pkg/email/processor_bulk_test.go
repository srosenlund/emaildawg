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
