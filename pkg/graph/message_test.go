package graph

import (
	"testing"
)

func TestParseGraphMessage(t *testing.T) {
	raw := []byte(`{"id":"AAM=","receivedDateTime":"2018-09-09T03:15:08Z","internetMessageId":"<x@y>","subject":"concert","conversationId":"AAQ=","isRead":true,"hasAttachments":false,"body":{"contentType":"text","content":"hi"},"from":{"emailAddress":{"name":"Adele","address":"adelev@contoso.com"}},"toRecipients":[{"emailAddress":{"name":"Alex","address":"alexw@contoso.com"}}]}`)
	m, err := parseGraphMessage(raw)
	if err != nil {
		t.Fatal(err)
	}
	if m.FromAddress != "adelev@contoso.com" {
		t.Fatalf("want FromAddress=adelev@contoso.com got %q", m.FromAddress)
	}
	if m.FromName != "Adele" {
		t.Fatalf("want FromName=Adele got %q", m.FromName)
	}
	if m.ConversationID != "AAQ=" {
		t.Fatalf("want ConversationID=AAQ= got %q", m.ConversationID)
	}
	if m.BodyText != "hi" {
		t.Fatalf("want BodyText=hi got %q", m.BodyText)
	}
	if !m.IsRead {
		t.Fatal("want IsRead=true")
	}
	if m.HasAttachments {
		t.Fatal("want HasAttachments=false")
	}
	if m.ID != "AAM=" {
		t.Fatalf("want ID=AAM= got %q", m.ID)
	}
	if m.InternetMessageID != "<x@y>" {
		t.Fatalf("want InternetMessageID=<x@y> got %q", m.InternetMessageID)
	}
	if m.Subject != "concert" {
		t.Fatalf("want Subject=concert got %q", m.Subject)
	}
	if len(m.To) != 1 || m.To[0].Address != "alexw@contoso.com" || m.To[0].Name != "Alex" {
		t.Fatalf("want To=[{Alex alexw@contoso.com}] got %+v", m.To)
	}
	if m.ReceivedDateTime.IsZero() {
		t.Fatal("want ReceivedDateTime parsed, got zero")
	}
	if m.ReceivedDateTime.Year() != 2018 || m.ReceivedDateTime.Month() != 9 || m.ReceivedDateTime.Day() != 9 {
		t.Fatalf("want ReceivedDateTime=2018-09-09 got %v", m.ReceivedDateTime)
	}
}
