package graph

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSendReply_Sequence(t *testing.T) {
	var steps []string
	var createBody string
	preferOK := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Prefer"), `IdType="ImmutableId"`) {
			preferOK = false
		}
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/createReply"):
			steps = append(steps, "createReply")
			b, _ := io.ReadAll(r.Body)
			createBody = string(b)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"DRAFT"}`))
		case r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/messages/DRAFT"):
			// The new implementation must NOT PATCH the body (that overwrote the
			// quoted original). Record it so the assertion below can fail.
			steps = append(steps, "patch")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"DRAFT"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/messages/DRAFT/send"):
			steps = append(steps, "send")
			w.WriteHeader(http.StatusAccepted)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	id, err := c.SendReply(context.Background(), "ORIG", "Hej tilbage")
	if err != nil {
		t.Fatalf("SendReply error: %v", err)
	}
	if id != "DRAFT" {
		t.Errorf("returned id = %q, want DRAFT", id)
	}
	// Text-only reply: createReply then send, with NO body PATCH (so the quoted
	// original from createReply survives).
	if strings.Join(steps, ",") != "createReply,send" {
		t.Errorf("step order = %v, want createReply,send (no patch)", steps)
	}
	if !strings.Contains(createBody, "Hej tilbage") || !strings.Contains(createBody, `"comment"`) {
		t.Errorf("createReply body = %q, want comment with reply text", createBody)
	}
	if !preferOK {
		t.Error("a request was missing Prefer: IdType=ImmutableId")
	}
}

func TestSendReply_AttachmentBeforeSend(t *testing.T) {
	var steps []string
	var attBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/createReply"):
			steps = append(steps, "createReply")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"DRAFT"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/messages/DRAFT/attachments"):
			steps = append(steps, "attachment")
			b, _ := io.ReadAll(r.Body)
			attBody = string(b)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"ATT"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/messages/DRAFT/send"):
			steps = append(steps, "send")
			w.WriteHeader(http.StatusAccepted)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	want := []byte("hello world")
	_, err := c.SendReply(context.Background(), "ORIG", "se vedhæftet",
		ReplyAttachment{Name: "note.txt", Bytes: want})
	if err != nil {
		t.Fatalf("SendReply error: %v", err)
	}
	// Attachment POST must happen AFTER createReply and BEFORE send.
	if strings.Join(steps, ",") != "createReply,attachment,send" {
		t.Errorf("step order = %v, want createReply,attachment,send", steps)
	}
	if !strings.Contains(attBody, "#microsoft.graph.fileAttachment") ||
		!strings.Contains(attBody, `"name":"note.txt"`) ||
		!strings.Contains(attBody, base64.StdEncoding.EncodeToString(want)) {
		t.Errorf("attachment body = %q", attBody)
	}
}

func TestSendReply_SendFailureReturnsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/createReply"):
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"DRAFT"}`))
		case strings.HasSuffix(r.URL.Path, "/send"):
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("ErrorSendAsDenied"))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, err := c.SendReply(context.Background(), "ORIG", "x")
	if err == nil || !strings.Contains(err.Error(), "ErrorSendAsDenied") {
		t.Fatalf("expected send error with body, got %v", err)
	}
}
