package connector

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"go.mau.fi/util/dbutil"
	bridgedb "maunium.net/go/mautrix/bridgev2/database"

	_ "github.com/mattn/go-sqlite3"
)

// newTestDB opens an in-memory SQLite database and returns an EmailAccountQuery
// backed by it. The caller is responsible for closing db.RawDB when done.
func newTestDB(t *testing.T) (*EmailAccountQuery, *dbutil.Database) {
	t.Helper()
	sqlDB, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	rawDB, err := dbutil.NewWithDB(sqlDB, "sqlite3")
	if err != nil {
		t.Fatalf("dbutil.NewWithDB: %v", err)
	}
	// Wrap in bridgev2 database.Database (just the embedded dbutil, no migrations needed).
	bdb := &bridgedb.Database{Database: rawDB}
	eaq := &EmailAccountQuery{DB: bdb}

	// Set a known passphrase so encryptString/decryptString work deterministically.
	t.Setenv("EMAILDAWG_PASSPHRASE", "test-passphrase-for-unit-tests-only")

	return eaq, rawDB
}

func TestGetGraphState_MissingReturnsNilNil(t *testing.T) {
	eaq, rawDB := newTestDB(t)
	defer rawDB.RawDB.Close()

	ctx := context.Background()
	if err := eaq.CreateTable(ctx); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}

	gs, err := eaq.GetGraphState(ctx, "@alice:example.com", "alice@example.com")
	if err != nil {
		t.Fatalf("GetGraphState returned error for missing row: %v", err)
	}
	if gs != nil {
		t.Fatalf("expected nil GraphState for missing row, got %+v", gs)
	}
}

func TestUpsertAndGetGraphState_RoundTrips(t *testing.T) {
	eaq, rawDB := newTestDB(t)
	defer rawDB.RawDB.Close()

	ctx := context.Background()
	if err := eaq.CreateTable(ctx); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}

	// Truncate to second precision so SQLite TEXT round-trip compares cleanly.
	expiry := time.Now().UTC().Truncate(time.Second)

	want := &GraphState{
		UserMXID:        "@alice:example.com",
		Email:           "alice@example.com",
		SubscriptionID:  "sub-abc-123",
		SubscriptionExpiry: expiry,
		ClientState:     "my-secret-client-state",
		InboxDeltaLink:  "https://graph.microsoft.com/v1.0/me/mailFolders/inbox/delta?$skiptoken=abc",
	}

	if err := eaq.UpsertGraphState(ctx, want); err != nil {
		t.Fatalf("UpsertGraphState: %v", err)
	}

	got, err := eaq.GetGraphState(ctx, want.UserMXID, want.Email)
	if err != nil {
		t.Fatalf("GetGraphState: %v", err)
	}
	if got == nil {
		t.Fatal("GetGraphState returned nil for existing row")
	}

	if got.SubscriptionID != want.SubscriptionID {
		t.Errorf("SubscriptionID: got %q, want %q", got.SubscriptionID, want.SubscriptionID)
	}
	// Compare as Unix seconds to avoid timezone representation issues.
	if got.SubscriptionExpiry.Unix() != want.SubscriptionExpiry.Unix() {
		t.Errorf("SubscriptionExpiry: got %v, want %v", got.SubscriptionExpiry, want.SubscriptionExpiry)
	}
	// client_state is encrypted at rest; verify it round-trips to original plaintext.
	if got.ClientState != want.ClientState {
		t.Errorf("ClientState: got %q, want %q", got.ClientState, want.ClientState)
	}
	if got.InboxDeltaLink != want.InboxDeltaLink {
		t.Errorf("InboxDeltaLink: got %q, want %q", got.InboxDeltaLink, want.InboxDeltaLink)
	}
}

func TestUpsertGraphState_UpdatesExistingRow(t *testing.T) {
	eaq, rawDB := newTestDB(t)
	defer rawDB.RawDB.Close()

	ctx := context.Background()
	if err := eaq.CreateTable(ctx); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}

	expiry := time.Now().UTC().Truncate(time.Second)
	gs := &GraphState{
		UserMXID:           "@alice:example.com",
		Email:              "alice@example.com",
		SubscriptionID:     "sub-v1",
		SubscriptionExpiry: expiry,
		ClientState:        "state-v1",
		InboxDeltaLink:     "https://delta/v1",
	}

	if err := eaq.UpsertGraphState(ctx, gs); err != nil {
		t.Fatalf("first UpsertGraphState: %v", err)
	}

	// Now update the subscription.
	gs.SubscriptionID = "sub-v2"
	gs.InboxDeltaLink = "https://delta/v2"
	if err := eaq.UpsertGraphState(ctx, gs); err != nil {
		t.Fatalf("second UpsertGraphState: %v", err)
	}

	got, err := eaq.GetGraphState(ctx, gs.UserMXID, gs.Email)
	if err != nil {
		t.Fatalf("GetGraphState: %v", err)
	}
	if got == nil {
		t.Fatal("GetGraphState returned nil")
	}
	if got.SubscriptionID != "sub-v2" {
		t.Errorf("expected updated SubscriptionID %q, got %q", "sub-v2", got.SubscriptionID)
	}
	if got.InboxDeltaLink != "https://delta/v2" {
		t.Errorf("expected updated InboxDeltaLink %q, got %q", "https://delta/v2", got.InboxDeltaLink)
	}
}
