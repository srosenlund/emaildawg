package graph

import (
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
	IsRead             bool
	HasAttachments     bool
	ReceivedDateTime   time.Time
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

	return &GraphMessage{
		ID:               raw.ID,
		InternetMessageID: raw.InternetMessageID,
		ConversationID:   raw.ConversationID,
		ParentFolderID:   raw.ParentFolderID,
		Subject:          raw.Subject,
		FromName:         raw.From.EmailAddress.Name,
		FromAddress:      raw.From.EmailAddress.Address,
		To:               to,
		BodyText:         raw.Body.Content,
		IsRead:           raw.IsRead,
		HasAttachments:   raw.HasAttachments,
		ReceivedDateTime: received,
	}, nil
}
