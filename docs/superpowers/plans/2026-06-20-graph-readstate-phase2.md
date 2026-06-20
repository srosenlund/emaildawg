# Graph Read-State Two-Way (Phase 2) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`).

**Goal:** Sync read/unread state both directions between the Outlook mailbox and Beeper: marking a mail read in Beeper marks it read in Outlook, and vice-versa — without feedback loops.

**Architecture:** Builds on Phase 1's Graph client + webhook + deliver + message-mapping. Adds: (flow 3) `HandleMatrixReadReceipt` → Graph `PATCH isRead`; (flow 2) webhook `updated` events → `simplevent.Receipt` to Matrix; a short-lived suppression cache so a change we make on one side doesn't echo back from the other.

**Tech Stack:** Go 1.24, mautrix bridgev2, Microsoft Graph v1.0 (app-only, Mail.ReadWrite).

## Global Constraints

- Build on branch `feat/graph-twoway` (Phase 1, deployed + working). Mailbox `mail@stefanrosenlund.dk`.
- App-only Graph; `Prefer: IdType="ImmutableId"` on all message calls (consistent with Phase 1).
- Message dedup / mapping key: `networkid.MessageID("email:"+internetMessageID)`. Look up via `ec.Bridge.DB.Message.GetPartByMXID(ctx, eventID)` (MXID→remote) and `GetLastPartByID(ctx, loginID, msgID)` (remote→MXID).
- Verified bridgev2 APIs: `ReadReceiptHandlingNetworkAPI` (networkinterface.go:609) → `HandleMatrixReadReceipt(ctx, *MatrixReadReceipt) error`; `MatrixReadReceipt{Portal, EventID id.EventID, ExactMessage *database.Message (MAY be nil), ReadUpTo, LastRead time.Time}`; incoming via `simplevent.Receipt{LastTarget networkid.MessageID, Targets []networkid.MessageID, ReadUpTo time.Time}` + `EventMeta{Type: bridgev2.RemoteEventReadReceipt, PortalKey, Sender}`.
- Graph: `PATCH /users/{userID}/messages/{id}` body `{"isRead": true|false}` → 200.
- Build verification after each task: `cd ~/Projects/emaildawg && export PATH="/opt/homebrew/bin:$HOME/go/bin:$PATH" CGO_ENABLED=1 && OLM=$(brew --prefix libolm) && CGO_CFLAGS="-I${OLM}/include" CGO_LDFLAGS="-L${OLM}/lib" go build ./... && go vet ./pkg/...` (ignore the harmless duplicate-libraries ld warning).
- Go unit tests for pure logic (suppression cache, payload routing); build + deploy smoke for integration.
- Commit after each task.

---

## Task 1: Graph SetRead + suppression cache

**Files:**
- Modify: `pkg/graph/client.go` (add SetRead)
- Create: `pkg/connector/graph_suppress.go`
- Test: `pkg/connector/graph_suppress_test.go`

**Interfaces:**
- Produces: `func (c *graph.Client) SetRead(ctx, msgID string, isRead bool) error` — `PATCH /users/{userID}/messages/{msgID}` with `Prefer: IdType="ImmutableId"`, body `{"isRead":isRead}`; non-200 → error with body.
- Produces: `type suppressCache struct {...}` with `Suppress(msgID string)` (record now), `IsSuppressed(msgID string) bool` (true if recorded within TTL, default 45s), and internal pruning. Concurrency-safe (sync.Mutex). On `EmailConnector`: a `suppress *suppressCache` field, initialized in Init/Start.

- [ ] **Step 1: Failing test** `graph_suppress_test.go`: `Suppress(id)` then `IsSuppressed(id)`==true immediately; a never-suppressed id → false; an id suppressed with an artificially old timestamp (inject clock or expose a `suppressAt(id, t)` test helper) → false after TTL.
- [ ] **Step 2:** Run `go test ./pkg/connector/ -run Suppress` → FAIL.
- [ ] **Step 3:** Implement `suppressCache` (map[string]time.Time + Mutex, TTL const 45s, prune on access) and `Client.SetRead`.
- [ ] **Step 4:** Run test → PASS. Build + vet. Commit (`feat:`).

---

## Task 2: Flow 3 — read in Beeper → Outlook

> **Critical id distinction:** our bridgev2 message dedup id is `"email:<internetMessageID>"` (RFC822 Message-ID). But Graph `PATCH /messages/{id}` needs Graph's INTERNAL message id (a base64url resource id), which is a DIFFERENT value. So we must resolve internetMessageID → Graph id before PATCHing. And the **suppression key is `internetMessageID`** (the one stable value available on BOTH the PATCH side here and the webhook side in Task 3 — the webhook's `resourceData.id` is a Graph id we'll convert back to internetMessageID via GetMessage).

**Files:**
- Modify: `pkg/graph/client.go` (add internetMessageID→Graph-id resolver)
- Create: `pkg/connector/graph_readreceipt.go`
- Modify: `pkg/connector/connector.go` (compile-time assertion `*EmailClient` satisfies `ReadReceiptHandlingNetworkAPI`)

**Interfaces:**
- Consumes: `Client.SetRead` (Task 1), `suppress` (Task 1), `ec.Bridge.DB.Message`.
- Produces:
  - `func (c *graph.Client) FindGraphIDByInternetID(ctx, internetID string) (string, error)` — `GET /users/{userID}/messages?$filter=internetMessageId eq '<internetID>'&$select=id&$top=1` (URL-encode the whole query; OData single-quotes in the value doubled if any). Returns `value[0].id` (Graph internal id) or "" if not found. Sends `Prefer: IdType="ImmutableId"` so the returned id is the immutable form (survives moves — needed for Phase 3).
  - `func (ec *EmailClient) HandleMatrixReadReceipt(ctx context.Context, rcpt *bridgev2.MatrixReadReceipt) error`.

- [ ] **Step 1:** Implement `FindGraphIDByInternetID` in client.go (filter query, return id).
- [ ] **Step 2:** Implement `HandleMatrixReadReceipt`:
```go
func (ec *EmailClient) HandleMatrixReadReceipt(ctx context.Context, rcpt *bridgev2.MatrixReadReceipt) error {
    msg := rcpt.ExactMessage
    if msg == nil {
        var err error
        msg, err = ec.Main.Bridge.DB.Message.GetPartByMXID(ctx, rcpt.EventID)
        if err != nil || msg == nil { return nil } // not a bridged message; ignore
    }
    internetID := strings.TrimPrefix(string(msg.ID), "email:")
    if internetID == string(msg.ID) { return nil } // unexpected format; skip
    ec.Main.suppress.Suppress(internetID)          // suppression key = internetMessageID (consistent with Task 3)
    graphID, err := ec.Main.graphClient.FindGraphIDByInternetID(ctx, internetID)
    if err != nil || graphID == "" { ec.Main.suppress.Forget(internetID); return err }
    if err := ec.Main.graphClient.SetRead(ctx, graphID, true); err != nil {
        ec.Main.suppress.Forget(internetID)        // PATCH failed → let webhook handle it
        return err
    }
    return nil
}
```
   (Verify `ec.Main` is the `*EmailConnector` and `graphClient`/`suppress` are reachable, per Phase 1 LoadUserLogin. `Forget` exists from Task 1.)
- [ ] **Step 3:** Add `var _ bridgev2.ReadReceiptHandlingNetworkAPI = (*EmailClient)(nil)` near the EmailClient type. Build fails if the signature is wrong.
- [ ] **Step 4:** Build + vet. Commit (`feat:`).

---

## Task 3: Flow 2 — read in Outlook → Beeper

**Files:**
- Modify: `pkg/connector/connector.go` (webhook `updated` branch) + `pkg/connector/graph_deliver.go` (add a receipt helper)

**Interfaces:**
- Consumes: webhook notification items with `changeType=="updated"` (already delivered by the `created,updated` subscription), `GetMessage`, `suppress`, `deliver`.
- Produces: on `updated`, fetch the message; if `IsRead` and NOT suppressed, queue a `simplevent.Receipt` marking it read in Matrix.

- [ ] **Step 1:** In the webhook background worker (`processWebhookItem`), branch on `changeType`:
  - `created` → existing deliver path (unchanged).
  - `updated` → fetch via `GetMessage(graphID)` (graphID = webhook `resourceData.id`); then check suppression on the message's `InternetMessageID` (NOT the Graph id): `if ec.suppress.IsSuppressed(g.InternetMessageID) { return }` (this is the echo of our own PATCH — drop it); else if `g.IsRead`, call `client.deliverReadReceipt(ctx, g)`.
  (`webhookItem` already carries `changeType` from Phase 1 Task 3 fix. The suppression key is `InternetMessageID` on both sides — Task 2 suppresses it before PATCHing, here we check it after resolving the Graph id back to InternetMessageID via GetMessage.)
- [ ] **Step 2:** Implement `deliverReadReceipt` on `*EmailClient` (graph_deliver.go):
```go
func (ec *EmailClient) deliverReadReceipt(ctx context.Context, g *graph.GraphMessage) {
    ec.UserLogin.QueueRemoteEvent(&simplevent.Receipt{
        EventMeta: simplevent.EventMeta{
            Type: bridgev2.RemoteEventReadReceipt,
            PortalKey: networkid.PortalKey{ID: networkid.PortalID("thread:"+g.ConversationID), Receiver: ec.UserLogin.ID},
            Sender: bridgev2.EventSender{Sender: networkid.UserID("email:"+g.FromAddress)},
        },
        LastTarget: networkid.MessageID("email:"+g.InternetMessageID),
        ReadUpTo: time.Now(),
    })
}
```
- [ ] **Step 3:** Build + vet. Commit (`feat:`).

---

## Task 4: Smoke verification + deploy

**Files:** none (deploy + verify)

- [ ] **Step 1:** Build the Sliplane image locally if Docker available (`docker build -f Dockerfile.sliplane .`) — confirms it compiles in-container. (Optional; CI/Sliplane build covers it.)
- [ ] **Step 2:** Commit any remaining changes; push `feat/graph-twoway`.
- [ ] **Step 3:** Redeploy the `emaildawg` Sliplane service (delete+recreate, public=true, healthcheck `/_matrix/mau/live`, EMAILDAWG_CONFIG_B64 secret, volume emaildawg-data2). Wait live.
- [ ] **Step 4: Verify both directions** (the agent can do this):
  - Beeper→Outlook: mark a known email read in Beeper → `GET /users/{mailbox}/messages/{id}?$select=isRead` via Graph shows `isRead:true` within seconds.
  - Outlook→Beeper: mark an email read in Outlook (or via Graph `PATCH isRead:true`) → bridge delivers a read receipt (verify via bridge DB / Beeper). Confirm NO infinite loop (suppression works — check logs/DB don't show repeated PATCH/receipt ping-pong).

---

## Self-Review

- **Spec coverage:** flow 3 (Beeper→Outlook read) = Task 2; flow 2 (Outlook→Beeper read) = Task 3; feedback-loop protection = Task 1 suppress cache used by both. Archive/delete (flows 4-7) + reply (flow 8) remain Phase 3-4.
- **Type consistency:** graphID form `strings.TrimPrefix(remoteID,"email:")` is the SAME key suppressed (Task 2) and checked (Task 3). `simplevent.Receipt.LastTarget` = `"email:"+InternetMessageID` matches the deliver-side MessageID from Phase 1 Task 2.
- **Loop safety:** Beeper read → suppress(graphID) → PATCH → Graph `updated` webhook → IsSuppressed→drop. The reverse (Outlook read → receipt to Beeper) does not trigger a Matrix read receipt back to the bridge, so no loop; suppression is belt-and-suspenders.
- **Risk:** if `HandleMatrixReadReceipt` fires before the message is in the DB mapping (race), it returns nil (ignored) — acceptable; the user re-reading or the next state change re-syncs.
