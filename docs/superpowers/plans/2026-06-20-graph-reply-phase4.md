# Graph Reply from Beeper (Phase 4) Implementation Plan

> REQUIRED SUB-SKILL: superpowers:subagent-driven-development (or inline). Steps use `- [ ]`.

**Goal:** Let Stefan reply to an email from inside Beeper: a Matrix message in an email room → sent as a threaded reply from mail@stefanrosenlund.dk via Microsoft Graph.

**Architecture:** Replace the read-only `HandleMatrixMessage` stub. On an outgoing Matrix message: resolve the email to reply to (the message's ReplyTo, else the latest email in the thread), resolve its Graph id, and send a reply via Graph's createReply→send flow. Persist the sent message's mapping so the round-trip dedups.

**Tech Stack:** Go 1.24, mautrix bridgev2, Microsoft Graph v1.0 (app-only, Mail.ReadWrite + Mail.Send — both confirmed granted 2026-06-20).

## Global Constraints
- Branch `feat/graph-twoway`. Mailbox mail@stefanrosenlund.dk. App-only Graph, `Prefer: IdType="ImmutableId"`.
- v1 = TEXT replies only. Attachments are a documented follow-up (Task 4).
- Build: `cd ~/Projects/emaildawg && export PATH="/opt/homebrew/bin:$HOME/go/bin:$PATH" CGO_ENABLED=1 && OLM=$(brew --prefix libolm) && export CGO_CFLAGS="-I${OLM}/include" CGO_LDFLAGS="-L${OLM}/lib" && go build ./... && go vet ./pkg/...`
- In-place deploy via redeploy_service. Commit after each task.

## Verified APIs
- `NetworkAPI.HandleMatrixMessage(ctx, *bridgev2.MatrixMessage) (*bridgev2.MatrixMessageResponse, error)` — fork currently stubs it read-only (pkg/connector/client.go:579).
- `MatrixMessage`: `.Content *event.MessageEventContent` (`.Body`, `.MsgType`), `.ReplyTo *database.Message`, `.ThreadRoot *database.Message`, `.Portal`, `.Event`.
- `MatrixMessageResponse{ DB *database.Message, StreamOrder int64, ... }`.
- `portal.Bridge.DB.Message.GetLastThreadMessage(ctx, portalKey, threadRoot)` / `GetLastNInPortal(ctx, portalKey, n)` — find the email to reply to. `FindGraphIDByInternetID` (exists) → Graph id.
- Graph: createReply flow — `POST /users/{id}/messages/{origId}/createReply` (201 → draft `{id}`) → `PATCH /users/{id}/messages/{draftId}` `{"body":{"contentType":"text","content":...}}` → `POST /users/{id}/messages/{draftId}/send` (202). createReply auto-sets threading + recipients.

---

## Task 1: Graph `SendReply`
**Files:** Modify `pkg/graph/client.go`; Test `pkg/graph/client_reply_test.go`.
**Produces:** `func (c *Client) SendReply(ctx context.Context, origMsgID, bodyText string) (string, error)` — createReply→PATCH→send; returns the draft/sent Graph id (for mapping). Non-2xx → error with body. Same token/Prefer pattern as SetRead/MoveMessage.

- [ ] Failing httptest: assert sequence POST `/messages/{id}/createReply` (returns `{"id":"DRAFT"}`), PATCH `/messages/DRAFT` (body has `contentType:text` + content), POST `/messages/DRAFT/send`. Each carries `Prefer: IdType="ImmutableId"`. Non-2xx on send → error with body.
- [ ] Run → FAIL. Implement. Run → PASS. Build+vet. Commit.

---

## Task 2: Implement `HandleMatrixMessage` (replace stub)
**Files:** Modify `pkg/connector/client.go` (the existing stub).
**Consumes:** `SendReply`, `FindGraphIDByInternetID`, `ec.Main.Bridge.DB.Message`.
**Logic:**
1. If `ec.Main.graphClient == nil` → keep read-only warn + return `{DB:nil}` (IMAP mode safety).
2. Determine the target email: `target := msg.ReplyTo`; if nil → `target, _ = ec.Main.Bridge.DB.Message.GetLastNInPortal(ctx, msg.Portal.PortalKey, 1)` (latest in thread). If still nil → warn "no email to reply to" + return `{DB:nil}`.
3. `internetID := strings.TrimPrefix(string(target.ID), "email:")`; if unchanged → return `{DB:nil}`.
4. `body := msg.Content.Body`; if empty → return `{DB:nil}`.
5. `graphID, err := ec.Main.graphClient.FindGraphIDByInternetID(ctx, internetID)`; if err/empty → return error.
6. `newID, err := ec.Main.graphClient.SendReply(ctx, graphID, body)`; on err → return error (bridgev2 marks the Matrix message failed).
7. Return `&bridgev2.MatrixMessageResponse{ DB: &database.Message{ ID: networkid.MessageID("email:reply:"+newID), Room: msg.Portal.PortalKey, SenderID: networkid.UserID("email:"+ec.Email), Timestamp: time.Now() } }` (mapping so it isn't re-bridged).

- [ ] Implement. Build+vet. Commit.

---

## Task 3: Deploy + verify
- [ ] In-place redeploy. From Beeper, reply in an email room → verify in Outlook the reply is Sent + threaded (same conversation), sender mail@stefanrosenlund.dk. Confirm no echo loop (the sent reply isn't re-delivered as a new inbound).

---

## Task 4 (follow-up): Attachments
Download Matrix media via `intent.DownloadMedia(ctx, Content.URL, Content.File)`; base64; `POST /messages/{draftId}/attachments` (#microsoft.graph.fileAttachment) before send. Deferred from v1.

## Self-Review
- Reply target: ReplyTo first, else latest-in-thread — covers both "reply to specific" and "type in room". Threading via createReply (auto In-Reply-To/References + conversationId). Sender = mailbox (app-only). Echo-safety: the sent reply lands in Sent (not Inbox), so the inbox subscription won't re-deliver it; the DB mapping is belt-and-suspenders.
