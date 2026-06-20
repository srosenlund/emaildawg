package graph

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSendReply_Sequence(t *testing.T) {
	var steps []string
	var patchBody string
	preferOK := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Prefer"), `IdType="ImmutableId"`) {
			preferOK = false
		}
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/createReply"):
			steps = append(steps, "createReply")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"DRAFT"}`))
		case r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/messages/DRAFT"):
			steps = append(steps, "patch")
			b, _ := io.ReadAll(r.Body)
			patchBody = string(b)
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
	if strings.Join(steps, ",") != "createReply,patch,send" {
		t.Errorf("step order = %v, want createReply,patch,send", steps)
	}
	if !strings.Contains(patchBody, "Hej tilbage") || !strings.Contains(patchBody, `"contentType":"text"`) {
		t.Errorf("patch body = %q", patchBody)
	}
	if !preferOK {
		t.Error("a request was missing Prefer: IdType=ImmutableId")
	}
}

func TestSendReply_SendFailureReturnsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/createReply"):
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"DRAFT"}`))
		case strings.HasSuffix(r.URL.Path, "/messages/DRAFT") && r.Method == http.MethodPatch:
			w.WriteHeader(http.StatusOK)
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
