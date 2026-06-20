# Graph Read-Path (Phase 1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace emaildawg's IMAP read path with a Microsoft Graph read path: new mail in the Outlook mailbox appears in Beeper in real time via Graph webhooks, with a delta-query backfill/reconciliation safety net.

**Architecture:** Keep the bridgev2 framework + Matrix/portal/threading layer. Add `pkg/graph/` (client, subscriptions, webhook, delta). Deliver mail to Matrix via `UserLogin.QueueRemoteEvent(simplevent.Message[...])`. The webhook HTTP route is registered on the bridge's existing appservice mux (`Bridge.Matrix.GetRouter()`), which requires `appservice.public_address` to be set. Reuse the XOAUTH2 client-credentials token-provider, with the Graph resource scope.

**Tech Stack:** Go 1.24, maunium.net/go/mautrix bridgev2, Microsoft Graph v1.0 (app-only), Sliplane (public service + HTTPS domain).

## Global Constraints

- Mailbox: `mail@stefanrosenlund.dk`; tenant `5914f039-e261-46a7-bb08-27abf512079d`; app `3337199c-7da3-4035-bbdf-0bf452e8b46b` (Entra app "Claude Code", has `Mail.ReadWrite` application).
- App-only everywhere: address mailbox as `/users/mail@stefanrosenlund.dk/...`, never `/me`.
- **Always send `Prefer: IdType="ImmutableId"`** on every Graph message call so stored ids survive moves (Phase 3+ depends on this).
- Graph base: `https://graph.microsoft.com/v1.0`. Token scope: `https://graph.microsoft.com/.default` (client-credentials).
- Thread key = Graph `conversationId`. Message dedup key = `internetMessageId`.
- Webhook needs `appservice.public_address` set + Sliplane service `public: true` with HTTPS domain.
- Build verification after every task: `cd ~/Projects/emaildawg && export PATH="/opt/homebrew/bin:$HOME/go/bin:$PATH" CGO_ENABLED=1 && OLM=$(brew --prefix libolm) && CGO_CFLAGS="-I${OLM}/include" CGO_LDFLAGS="-L${OLM}/lib" go build ./... && go vet ./pkg/...` — expect no errors (ignore the harmless `duplicate libraries` ld warning).
- Go unit tests for pure logic; build + local smoke-run for integration (no live-Graph unit tests).
- Commit after each task. Fork: `srosenlund/emaildawg` branch `feat/graph-twoway` (new branch off `feat/xoauth2-o365`).

---

## Task 0: Branch + Graph token scope

**Files:**
- Modify: `pkg/imap/xoauth2.go` (generalize token scope) → or Create `pkg/graph/token.go`

**Interfaces:**
- Produces: `graph.TokenProvider` with `Token(ctx) (string, error)` returning a `graph.microsoft.com/.default` app-only token. Reuses the same client-credentials HTTP flow already proven in `pkg/imap/xoauth2.go:XOAuth2TokenProvider`.

- [ ] **Step 1:** Create branch: `git checkout -b feat/graph-twoway` (off `feat/xoauth2-o365`).
- [ ] **Step 2:** Create `pkg/graph/token.go` with a `TokenProvider` struct (tenantID, clientID, clientSecret, scope) and `Token(ctx) (string,error)` — copy the cache+refresh logic verbatim from `pkg/imap/xoauth2.go` but with `scope = "https://graph.microsoft.com/.default"`. Keep it standalone (no IMAP import).
- [ ] **Step 3:** Unit test `pkg/graph/token_test.go`: test the 60s-early-refresh cache logic with an injected clock/HTTP stub (table test: fresh token returned from cache; expired token triggers refetch). Run `go test ./pkg/graph/ -run TestToken` → PASS.
- [ ] **Step 4:** Build verification (Global Constraints command). Commit.

---

## Task 1: Graph client — get message + parse

**Files:**
- Create: `pkg/graph/client.go`
- Create: `pkg/graph/message.go` (GraphMessage model + parse)
- Test: `pkg/graph/message_test.go`

**Interfaces:**
- Consumes: `graph.TokenProvider` (Task 0).
- Produces:
  - `type Client struct { ... }` + `NewClient(tp *TokenProvider, userID string) *Client`
  - `func (c *Client) GetMessage(ctx, id string) (*GraphMessage, error)` — `GET /users/{userID}/messages/{id}` with headers `Authorization: Bearer`, `Prefer: IdType="ImmutableId"`, `Prefer: outlook.body-content-type="text"`.
  - `type GraphMessage struct { ID, InternetMessageID, ConversationID, Subject, FromName, FromAddress string; To []Addr; BodyText string; IsRead bool; HasAttachments bool; ReceivedDateTime time.Time }`
  - `func parseGraphMessage([]byte) (*GraphMessage, error)`

- [ ] **Step 1: Write failing test** `pkg/graph/message_test.go` — feed the confirmed sample JSON (from message-get docs: id, receivedDateTime, internetMessageId, subject, bodyPreview, conversationId, isRead, body{contentType,content}, from.emailAddress{name,address}, toRecipients[]) into `parseGraphMessage` and assert each field maps correctly (FromAddress=="adelev@contoso.com", ConversationID set, BodyText from body.content when contentType text).

```go
func TestParseGraphMessage(t *testing.T) {
    raw := []byte(`{"id":"AAM=","receivedDateTime":"2018-09-09T03:15:08Z","internetMessageId":"<x@y>","subject":"concert","conversationId":"AAQ=","isRead":true,"hasAttachments":false,"body":{"contentType":"text","content":"hi"},"from":{"emailAddress":{"name":"Adele","address":"adelev@contoso.com"}},"toRecipients":[{"emailAddress":{"name":"Alex","address":"alexw@contoso.com"}}]}`)
    m, err := parseGraphMessage(raw)
    if err != nil { t.Fatal(err) }
    if m.FromAddress != "adelev@contoso.com" || m.ConversationID != "AAQ=" || m.BodyText != "hi" || !m.IsRead { t.Fatalf("bad parse: %+v", m) }
}
```
- [ ] **Step 2:** Run `go test ./pkg/graph/ -run TestParseGraphMessage` → FAIL (undefined).
- [ ] **Step 3:** Implement `GraphMessage`, `parseGraphMessage` (encoding/json into a shaped struct, flatten emailAddress), and `Client.GetMessage` (net/http GET with the two Prefer headers + bearer token; non-200 → error with body).
- [ ] **Step 4:** Run test → PASS.
- [ ] **Step 5:** Build verification. Commit.

---

## Task 2: Deliver a Graph message to Matrix

**Files:**
- Create: `pkg/connector/graph_deliver.go`

**Interfaces:**
- Consumes: `*graph.GraphMessage`, `*EmailClient` (has `UserLogin`), bridgev2 `simplevent`.
- Produces: `func (ec *EmailClient) deliverGraphMessage(ctx, g *graph.GraphMessage)` — builds `simplevent.Message[*graph.GraphMessage]` with `CreatePortal:true`, PortalKey from `conversationId`, Sender from `from.address`, dedup ID = `"email:"+internetMessageId`, and a `ConvertMessageFunc` producing a text `ConvertedMessage`. Calls `ec.UserLogin.QueueRemoteEvent(evt)`.

- [ ] **Step 1:** Implement `deliverGraphMessage` per the confirmed bridgev2 pattern:
```go
evt := &simplevent.Message[*graph.GraphMessage]{
  EventMeta: simplevent.EventMeta{
    Type: bridgev2.RemoteEventMessage,
    PortalKey: networkid.PortalKey{ID: networkid.PortalID("thread:"+g.ConversationID), Receiver: ec.UserLogin.ID},
    Sender: bridgev2.EventSender{Sender: networkid.UserID("email:"+g.FromAddress)},
    CreatePortal: true,
    Timestamp: g.ReceivedDateTime,
  },
  ID: networkid.MessageID("email:"+g.InternetMessageID),
  Data: g,
  ConvertMessageFunc: func(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, d *graph.GraphMessage) (*bridgev2.ConvertedMessage, error) {
    body := d.Subject
    if d.BodyText != "" { body = d.Subject+"\n\n"+d.BodyText }
    return &bridgev2.ConvertedMessage{Parts: []*bridgev2.ConvertedMessagePart{{
      Type: event.EventMessage,
      Content: &event.MessageEventContent{MsgType: event.MsgText, Body: body},
    }}}, nil
  },
}
if !ec.UserLogin.QueueRemoteEvent(evt).Success { ec.UserLogin.Log.Error().Msg("queue graph message failed") }
```
   (Reuse `pkg/email` HTML→text + sender/ghost-info helpers if they fit; keep the first version text-only. Ghost display name via portal `GetChatInfo`/member info can come in a follow-up — text body is enough to prove the path.)
- [ ] **Step 2:** Build verification (no isolated unit test — covered by Task 6 smoke run). Commit.

---

## Task 3: Webhook endpoint (validation + notifications)

**Files:**
- Create: `pkg/graph/webhook.go`
- Modify: `pkg/connector/connector.go` (register routes in `Start()`)
- Test: `pkg/graph/webhook_test.go`

**Interfaces:**
- Produces:
  - `func ValidationResponse(w http.ResponseWriter, r *http.Request) bool` — if `?validationToken=` present, write it back URL-decoded as `text/plain` 200 and return true.
  - `type Notification struct { Value []NotificationItem }`; `NotificationItem{ SubscriptionID, ClientState, ChangeType string; ResourceData struct{ ID string } }`
  - `func parseNotifications([]byte) (*Notification, error)`
  - On `*EmailConnector`: `handleGraphWebhook(w,r)` — validate clientState, parse, for each item enqueue `{changeType, messageID}` to an internal channel, respond `202` within 3s.

- [ ] **Step 1: Failing test** `webhook_test.go`: (a) `ValidationResponse` with `?validationToken=abc%20def` writes `abc def` + 200 + `text/plain`; (b) `parseNotifications` on the confirmed payload extracts `resourceData.id` + `clientState` + `changeType`.
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement `ValidationResponse`, `parseNotifications`, and `handleGraphWebhook` (reject if `clientState != ec.expectedClientState`; respond 202 immediately; dispatch ids to a buffered channel processed by a goroutine that calls `GetMessage` → `deliverGraphMessage`).
- [ ] **Step 4:** Register in `Start()` (confirmed pattern):
```go
if router := ec.Bridge.Matrix.GetRouter(); router != nil {
    router.HandleFunc("POST /_email/graph/webhook", ec.handleGraphWebhook)
    router.HandleFunc("GET /_email/graph/webhook", func(w http.ResponseWriter, r *http.Request){ graph.ValidationResponse(w, r) })
} else {
    ec.Bridge.Log.Warn().Msg("No public_address; Graph webhooks disabled")
}
```
   (Graph sends validation as POST `?validationToken=`; handle it inside `handleGraphWebhook` first — if `ValidationResponse` returns true, stop.)
- [ ] **Step 5:** Run test → PASS. Build verification. Commit.

---

## Task 4: Subscription lifecycle (create + renew + lifecycle events)

**Files:**
- Create: `pkg/graph/subscriptions.go`
- Create: `pkg/connector/graph_subscription.go` (wire into Start + renewal loop)
- Test: `pkg/graph/subscriptions_test.go`

**Interfaces:**
- Consumes: `graph.Client`, public address (from `Bridge.Matrix.GetPublicAddress()`).
- Produces:
  - `func (c *Client) CreateSubscription(ctx, notifyURL, clientState string) (*Subscription, error)` — POST /subscriptions, `resource:"users/{userID}/mailFolders('inbox')/messages"`, `changeType:"created,updated"`, `lifecycleNotificationUrl`=notifyURL, `expirationDateTime`=now+6 days, `latestSupportedTlsVersion:"v1_2"`.
  - `func (c *Client) RenewSubscription(ctx, id string, exp time.Time) error` — PATCH.
  - `func (c *Client) DeleteSubscription(ctx, id string) error`
  - `type Subscription struct { ID string; ExpirationDateTime time.Time }`
  - Persist subscription id+expiry in a new bridge DB row (see Task 5 store) or `email_accounts`-adjacent table.

- [ ] **Step 1: Failing test:** `subscriptions_test.go` — `buildSubscriptionBody(userID, notifyURL, clientState, exp)` produces JSON with correct `resource`, `changeType:"created,updated"`, `lifecycleNotificationUrl==notificationUrl`, expiry within 7-day cap. Pure function, no HTTP.
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement `buildSubscriptionBody` + `CreateSubscription`/`RenewSubscription`/`DeleteSubscription` (HTTP). On `EmailConnector`: in `Start()` after autoLogin, if public address present: create (or reuse persisted) subscription, store id+expiry; start a renewal goroutine that PATCHes when within 24h of expiry (ticker every 1h). Generate a random `clientState`, persist it, validate against it in `handleGraphWebhook`.
- [ ] **Step 4:** Lifecycle handling: extend `handleGraphWebhook` (or a second route `/_email/graph/lifecycle`) to parse `lifecycleEvent`: `reauthorizationRequired` → PATCH renew (single call, not reauthorize+patch within 10min); `subscriptionRemoved` → recreate + trigger delta reconcile (Task 6); `missed` → trigger delta reconcile.
- [ ] **Step 5:** Run test → PASS. Build verification. Commit.

---

## Task 5: Subscription/delta state store

**Files:**
- Create: `pkg/connector/graph_store.go`
- Modify: `pkg/connector/database.go` (migration for new table)

**Interfaces:**
- Produces: a `graph_state` table (single row per mailbox): `user_mxid, email, subscription_id, subscription_expiry, client_state, inbox_delta_link`. Methods: `GetGraphState(ctx, userMXID, email)`, `UpsertGraphState(ctx, *GraphState)`.

- [ ] **Step 1: Failing test** `graph_store_test.go` using an in-memory sqlite (follow how `pkg/connector/database.go` tests/init the DB) — upsert then get round-trips `subscription_id` + `inbox_delta_link`.
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement the table DDL (CREATE TABLE IF NOT EXISTS in the connector's DB init path next to `email_accounts`), the `GraphState` struct, and `GetGraphState`/`UpsertGraphState`. Encrypt `client_state` with the existing `encryptString` helper used for passwords.
- [ ] **Step 4:** Run test → PASS. Build verification. Commit.

---

## Task 6: Delta backfill + reconciliation

**Files:**
- Create: `pkg/graph/delta.go`
- Modify: `pkg/connector/graph_subscription.go` (call backfill on first run + reconcile on missed/removed)
- Test: `pkg/graph/delta_test.go`

**Interfaces:**
- Produces:
  - `func (c *Client) DeltaPage(ctx, url string) (msgs []*GraphMessage, removed []string, nextLink, deltaLink string, err error)` — GET either `users/{id}/mailFolders/inbox/messages/delta` (initial) or a saved nextLink/deltaLink. Parses items (full message or `@removed`).
  - `func parseDeltaPage([]byte) (...)` (pure, tested).
  - On connector: `runBackfill(ctx)` — walk pages from no-token until a `@odata.deltaLink`, deliver each message, store deltaLink; `reconcile(ctx)` — GET stored deltaLink, deliver new/changed, store new deltaLink. Handle `410 Gone` / `syncStateNotFound` by re-baselining.

- [ ] **Step 1: Failing test** `delta_test.go`: `parseDeltaPage` on a page with one normal message + one `{"id":"X","@removed":{"reason":"deleted"}}` + an `@odata.deltaLink` → returns 1 msg, 1 removed id, deltaLink set, nextLink empty.
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement `parseDeltaPage`, `DeltaPage`, `runBackfill`, `reconcile`. Backfill runs once on startup if no stored deltaLink (bounded: respect `Prefer: odata.maxpagesize=50`, log count). Wire `reconcile` to the `missed`/`subscriptionRemoved` lifecycle handlers (Task 4 step 4). Tolerate stray read-state/`@removed` events per the documented collection-level gotcha.
- [ ] **Step 4:** Run test → PASS. Build verification. Commit.

---

## Task 7: Config, IMAP cutover, deploy

**Files:**
- Modify: `pkg/connector/connector.go` (gate IMAP vs Graph), `pkg/connector/config.go` + `example-config.yaml` (graph webhook toggle), Sliplane config

**Interfaces:**
- Produces: a config switch `network.graph.enabled` (default false) that, when true, disables the IMAP auto-login path and uses the Graph read-path instead.

- [ ] **Step 1:** Add `GraphConfig{Enabled bool}` under `network` (mirror oauth2 wiring in `config.go` + `upgradeConfig` copy + example-config). When `graph.enabled`, skip `autoLogin`'s IMAP `AddAccount` and instead start the Graph subscription + backfill for the same mailbox.
- [ ] **Step 2:** Build verification.
- [ ] **Step 3: Local smoke test** — set `appservice.public_address` to a temporary tunnel (or skip webhook, test backfill only) in a local config; run the binary; verify logs show: Graph token acquired, subscription created (or backfill ran), and a delivered message creates a Matrix room. Document the observed log lines. (If no public tunnel available locally, verify backfill path only locally; webhook validated on Sliplane in step 5.)
- [ ] **Step 4: Commit + push** branch `feat/graph-twoway`.
- [ ] **Step 5: Deploy to Sliplane** — recreate the `emaildawg` service with `public: true` + an HTTPS domain (managed `*.sliplane.app` is fine), set `appservice.public_address` in config to that domain, set `network.graph.enabled: true`, deploy from `feat/graph-twoway`. Verify via logs: subscription created, validation handshake succeeded. Send a test mail → confirm a room appears in Beeper.

---

## Self-Review

- **Spec coverage (Phase 1 scope):** new-mail→Beeper via Graph ✅ (Tasks 1,2,6); webhooks + validation + clientState ✅ (Task 3); subscription create/renew/lifecycle ✅ (Task 4); delta backfill/reconcile fallback ✅ (Task 6); state persistence ✅ (Task 5); public-endpoint deploy ✅ (Task 7). Phases 2-4 (read-state/archive/delete/send) are out of scope for this plan — separate plans once these interfaces land.
- **Placeholders:** none — each task has concrete signatures, Graph request shapes (from verified research), and bridgev2 calls.
- **Type consistency:** `GraphMessage` fields used identically in Tasks 1/2/6; dedup ID `"email:"+InternetMessageID` and PortalKey `"thread:"+ConversationID` consistent across Tasks 2/6; `TokenProvider` from Task 0 consumed by Task 1.
- **Open risk:** ghost display-names/HTML-rendering kept minimal in v1 (text body) — richer rendering is a follow-up, not a Phase 1 blocker.
