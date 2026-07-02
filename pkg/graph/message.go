package graph

import (
	"strings"
	"encoding/json"
	"fmt"
	"time"
)

// Addr holds a display name and email address pair.
type Addr struct {
	Name    string
	Address string
}

// GraphMessage is the parsed representation of a Microsoft Graph message resource.
type GraphMessage struct {
	ID                 string
	InternetMessageID  string
	ConversationID     string
	ParentFolderID     string
	Subject            string
	FromName           string
	FromAddress        string
	To                 []Addr
	BodyText           string
	// BodyHTML is set when Graph returns an HTML body (Prefer: body-content-type="html").
	BodyHTML string
	// IsBulk marks newsletters/marketing (List-Unsubscribe or Precedence: bulk/list).
	IsBulk bool
	IsRead             bool
	HasAttachments     bool
	ReceivedDateTime   time.Time
	// Attachments is populated lazily by the deliver layer via
	// Client.FetchAttachments; the delta/message parser leaves it nil.
	Attachments []GraphAttachment
}

// graphMessageJSON mirrors the Graph API JSON shape for a message resource.
type graphMessageJSON struct {
	ID                 string `json:"id"`
	InternetMessageID  string `json:"internetMessageId"`
	ConversationID     string `json:"conversationId"`
	ParentFolderID     string `json:"parentFolderId"`
	Subject            string `json:"subject"`
	IsRead             bool   `json:"isRead"`
	HasAttachments     bool   `json:"hasAttachments"`
	ReceivedDateTime   string `json:"receivedDateTime"`
	Body               struct {
		ContentType string `json:"contentType"`
		Content     string `json:"content"`
	} `json:"body"`
	From struct {
		EmailAddress struct {
			Name    string `json:"name"`
			Address string `json:"address"`
		} `json:"emailAddress"`
	} `json:"from"`
	ToRecipients []struct {
		EmailAddress struct {
			Name    string `json:"name"`
			Address string `json:"address"`
		} `json:"emailAddress"`
	} `json:"toRecipients"`
	InternetMessageHeaders []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"internetMessageHeaders"`
}

// parseGraphMessage unmarshals a Graph API message JSON payload into a GraphMessage.
// It flattens nested emailAddress fields and parses receivedDateTime as RFC3339.
func parseGraphMessage(data []byte) (*GraphMessage, error) {
	var raw graphMessageJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("unmarshal graph message: %w", err)
	}

	var received time.Time
	if raw.ReceivedDateTime != "" {
		var err error
		received, err = time.Parse(time.RFC3339Nano, raw.ReceivedDateTime)
		if err != nil {
			return nil, fmt.Errorf("parse receivedDateTime %q: %w", raw.ReceivedDateTime, err)
		}
	}

	to := make([]Addr, 0, len(raw.ToRecipients))
	for _, r := range raw.ToRecipients {
		to = append(to, Addr{
			Name:    r.EmailAddress.Name,
			Address: r.EmailAddress.Address,
		})
	}

	m := &GraphMessage{
		ID:               raw.ID,
		InternetMessageID: raw.InternetMessageID,
		ConversationID:   raw.ConversationID,
		ParentFolderID:   raw.ParentFolderID,
		Subject:          raw.Subject,
		FromName:         raw.From.EmailAddress.Name,
		FromAddress:      raw.From.EmailAddress.Address,
		To:               to,
		IsRead:           raw.IsRead,
		HasAttachments:   raw.HasAttachments,
		ReceivedDateTime: received,
	}
	if strings.EqualFold(raw.Body.ContentType, "html") {
		m.BodyHTML = raw.Body.Content
	} else {
		m.BodyText = raw.Body.Content
	}
	for _, h := range raw.InternetMessageHeaders {
		switch {
		case strings.EqualFold(h.Name, "List-Unsubscribe"):
			m.IsBulk = true
		case strings.EqualFold(h.Name, "Precedence"):
			v := strings.ToLower(strings.TrimSpace(h.Value))
			if v == "bulk" || v == "list" {
				m.IsBulk = true
			}
		}
	}
	return m, nil
}
