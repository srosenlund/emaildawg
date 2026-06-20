# Graph Archive + Delete Two-Way (Phase 3) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Sync archive and delete both directions between the Outlook mailbox and Beeper (flows 4–7 of the two-way design), without feedback loops.

**Architecture:** Builds on Phase 1 (Graph read-path) + Phase 2 (read-state two-way). Adds Graph `move`/`delete` calls, two Matrix→Graph handlers (`HandleRoomTag`, `HandleMatrixMessageRemove`), and Graph→Matrix archive/delete delivery (`simplevent.ChatInfoChange` tag + `simplevent.ChatDelete`). Reuses Phase 2's `suppressCache` for loop protection and `FindGraphIDByInternetID` for id resolution.

**Tech Stack:** Go 1.24, mautrix bridgev2 (v0.24.3-...196164ed6749), Microsoft Graph v1.0 (app-only, Mail.ReadWrite).

**Approach decision (Stefan, 2026-06-20): PROBE-FIRST.** The Beeper→Outlook triggers (flow 4 archive, flow 6 delete) cannot be determined from framework code alone — it is unknown whether Beeper's "archive chat" emits an `m.tag` event (→ `HandleRoomTag`) or a room capability with no tag event, and whether "delete chat" emits per-message redactions (→ `HandleMatrixMessageRemove`). Tasks 1–2 build the Graph foundation + **instrumented handlers that only log** what Beeper sends. After deploy, Stefan archives and deletes one test chat; the captured events determine the final handler code (Tasks 3–7), which are detailed only once the probe data is in.

## Global Constraints

- Branch `feat/graph-twoway`. Mailbox `mail@stefanrosenlund.dk`.
- App-only Graph; `Prefer: IdType="ImmutableId"` on every message call (consistent with Phase 1/2).
- Build verification after each task: `cd ~/Projects/emaildawg && export PATH="/opt/homebrew/bin:$HOME/go/bin:$PATH" CGO_ENABLED=1 && OLM=$(brew --prefix libolm) && CGO_CFLAGS="-I${OLM}/include" CGO_LDFLAGS="-L${OLM}/lib" && go build ./... && go vet ./pkg/...` (ignore the harmless duplicate-libraries ld warning).
- Suppression key is `internetMessageID` on both directions (matches Phase 2). Archive/delete we initiate on one side must not echo back from the other.
- Delete is **soft** (Deleted Items / recoverable). No hard-delete.
- Deploy is **in-place** via `redeploy_service` (preserves volume `emaildawg-data2`, `EMAILDAWG_PASSPHRASE`, SSH). Never delete+recreate — that loses graph_state + SSH (root-caused 2026-06-20).
- Commit after each task.

## Verified bridgev2 / Graph APIs (from 2026-06-20 research)

- `TagHandlingNetworkAPI { HandleRoomTag(ctx, *MatrixRoomTag) error }` (networkinterface.go:647). `MatrixRoomTag = MatrixRoomMeta[*event.TagEventContent]`: fields `.Content *event.TagEventContent` (`.Tags map[event.RoomTag]event.TagMetadata`), `.PrevContent *event.TagEventContent`, `.Portal *Portal`, `.Event *event.Event`.
- `RedactionHandlingNetworkAPI { HandleMatrixMessageRemove(ctx, *MatrixMessageRemove) error }` (networkinterface.go:601). `MatrixMessageRemove`: `.TargetMessage *database.Message`, `.Portal *Portal`, `.Event *event.Event`. No Action field — presence of TargetMessage = a bridged message was redacted.
- Tag constants (event/accountdata.go): `event.RoomTagFavourite="m.favourite"`, `event.RoomTagLowPriority="m.lowpriority"`. No dedicated "archive" constant; mautrix convention maps archive↔`m.lowpriority`. Beeper also exposes `RoomFeatures.Archive bool` capability (event/capabilities.go:54) — probe will reveal which path fires.
- Double puppet: `userLogin.User.DoublePuppet(ctx) MatrixAPI`; `MatrixAPI.TagRoom(ctx, roomID, tag event.RoomTag, isTagged bool) error` (matrixinterface.go:177).
- Graph→Matrix archive: `simplevent.ChatInfoChange{ EventMeta{Type: bridgev2.RemoteEventChatInfoChange, PortalKey, Sender}, ChatInfoChange: &bridgev2.ChatInfoChange{ ChatInfo: &bridgev2.ChatInfo{ UserLocal: &bridgev2.UserLocalPortalInfo{ Tag: &event.RoomTagLowPriority } } } }` → framework calls `dp.TagRoom` via `portal.updateUserLocalInfo`. NOTE: gated on `Config.OnlyBridgeTags` containing the tag (portal.go:3994) — config must allow `m.lowpriority` or be empty-of-restriction; verify during Task 6.
- Graph→Matrix delete: `simplevent.ChatDelete{ EventMeta{Type: bridgev2.RemoteEventChatDelete, PortalKey, Sender}, OnlyForMe: false }` → framework calls `portal.Delete` + `Bot.DeleteRoom` (portal.go:3256).
- Graph existing client patterns (pkg/graph/client.go): `graphBaseURL` const; methods acquire token via `c.tp.Token(ctx)`, build `fmt.Sprintf("%s/users/%s/messages/%s", graphBaseURL, c.userID, url.PathEscape(msgID))`, set `Authorization: Bearer`, `Prefer: IdType="ImmutableId"`, check status, return body on error.
- Graph delta (pkg/graph/delta.go `parseDeltaPage`): returns `removed []string` from `@removed` items (`deltaRemovedItem{ID, Removed{Reason}}`). This is the robust signal for "message left inbox" (archive OR delete) — preferred over the beta `deleted` changeType.
- Webhook branch (pkg/connector/graph_subscription.go:367): currently `if item.ChangeType != "created" { continue }`. NOTE: Phase 2 actually handles `updated` too — confirm exact current branching before editing in Task 5.

---

## Task 1: Graph `MoveMessage` + `DeleteMessage`

**Files:**
- Modify: `pkg/graph/client.go` (add two methods)
- Test: `pkg/graph/client_move_test.go` (httptest server asserting method/path/body/headers)

**Interfaces:**
- Consumes: existing `Client{tp, userID, httpc}`, `graphBaseURL`.
- Produces:
  - `func (c *Client) MoveMessage(ctx context.Context, msgID, destWellKnownFolder string) error` — `POST {base}/users/{userID}/messages/{msgID}/move`, body `{"destinationId":"<dest>"}`, header `Prefer: IdType="ImmutableId"` + `Content-Type: application/json`; non-2xx → error with body. `destWellKnownFolder` is a Graph well-known folder name (`"archive"`, `"deleteditems"`).
  - `func (c *Client) DeleteMessage(ctx context.Context, msgID string) error` — `DELETE {base}/users/{userID}/messages/{msgID}`, header `Prefer: IdType="ImmutableId"`; non-2xx → error with body. (Soft-delete: Graph DELETE on a message moves it to Deleted Items.)

- [ ] **Step 1: Write failing tests** in `client_move_test.go`: spin an `httptest.NewServer`, point a `Client` at it (override `graphBaseURL` via a test constructor or an injected base — match how existing client tests inject the base; if none exist, add a `baseURL` field defaulting to `graphBaseURL`). Assert: `MoveMessage(ctx,"AAA","archive")` issues `POST /users/{userID}/messages/AAA/move` with body `{"destinationId":"archive"}` and the `Prefer: IdType="ImmutableId"` header; `DeleteMessage(ctx,"AAA")` issues `DELETE /users/{userID}/messages/AAA` with the Prefer header. Add a non-2xx case → error contains the response body.
- [ ] **Step 2:** Run `go test ./pkg/graph/ -run Move` → FAIL (undefined methods).
- [ ] **Step 3:** Implement both methods following the `SetRead` pattern verbatim (token, URL via `url.PathEscape(msgID)`, headers, status check, body-on-error).
- [ ] **Step 4:** Run tests → PASS. Build + vet.
- [ ] **Step 5:** Commit (`feat(graph): MoveMessage + DeleteMessage`).

---

## Task 2: Instrumented probe handlers (Beeper→Outlook discovery)

**Files:**
- Create: `pkg/connector/graph_archive_delete.go` (the two handlers, log-only for now)
- Modify: `pkg/connector/connector.go` (compile-time assertions)

**Interfaces:**
- Produces:
  - `func (ec *EmailClient) HandleRoomTag(ctx context.Context, msg *bridgev2.MatrixRoomTag) error` — logs at INFO: portal id, `portal.MXID`, every key in `msg.Content.Tags`, every key in `msg.PrevContent.Tags` (guard nil), and the resolved `internetMessageID` for the room's last message (via `GetLastPartByID` on the portal's last part, then `TrimPrefix "email:"`). Returns nil. **Acts on nothing yet.**
  - `func (ec *EmailClient) HandleMatrixMessageRemove(ctx context.Context, msg *bridgev2.MatrixMessageRemove) error` — logs at INFO: portal id, `msg.TargetMessage.ID` (the remote message id), `msg.TargetMessage.MXID`. Returns nil. **Acts on nothing yet.**
- Add assertions: `var _ bridgev2.TagHandlingNetworkAPI = (*EmailClient)(nil)` and `var _ bridgev2.RedactionHandlingNetworkAPI = (*EmailClient)(nil)`.

- [ ] **Step 1:** Implement both log-only handlers (guard all nils; never error). Add compile-time assertions.
- [ ] **Step 2:** Build + vet. Commit (`feat(connector): instrument archive/delete probe handlers`).
- [ ] **Step 3 (deploy + probe — controller, not subagent):** Push branch; `redeploy_service(project_4ejf2kxei1nt, service_iukfg3x2matn)` in-place; wait for fresh `Bridge started`. Ask Stefan to: (a) archive ONE email chat in Beeper, (b) delete ONE different email chat in Beeper. Capture the resulting log lines (`HandleRoomTag` / `HandleMatrixMessageRemove` firing, with the tag keys / target ids). **This is the probe gate — do not write Tasks 3–4 until the events are observed.**

---

## Tasks 3–7 (detailed AFTER the Task 2 probe)

The probe output determines the exact handler code. Planned shape:

- **Task 3 — Flow 4 (Beeper archive → Outlook):** In `HandleRoomTag`, when the archive tag (identity confirmed by probe — hypothesis `m.lowpriority`) transitions absent→present, resolve room's last message → `internetMessageID` → `FindGraphIDByInternetID` → `suppress.Suppress(internetID)` → `MoveMessage(graphID, "archive")`. `Forget` on failure. If probe shows Beeper archive does NOT emit a tag event, redesign (e.g. capability-based or poll Beeper state).
- **Task 4 — Flow 6 (Beeper delete → Outlook):** In `HandleMatrixMessageRemove`, resolve `msg.TargetMessage.ID` → internetMessageID → graphID → `suppress.Suppress` → `DeleteMessage(graphID)`. If probe shows "delete chat" emits no redaction, redesign.
- **Task 5 — Outlook→Beeper detection:** Use delta `@removed` ids (robust) rather than beta `deleted` changeType. For each removed message, if suppressed → drop (our own action). Else classify archive-vs-delete: query the message by id (`GetMessage` / a lightweight folder lookup) — if found in Archive folder → archive; if in Deleted Items / not found → delete. Add a `classifyRemoval` helper (unit-tested pure function on a folder/`@removed`-reason input).
- **Task 6 — Flow 5 (Outlook archive → Beeper):** Emit `simplevent.ChatInfoChange` with `UserLocal.Tag = &event.RoomTagLowPriority`. Verify `Config.OnlyBridgeTags` does not filter it out (adjust config if needed). Suppress to avoid echo.
- **Task 7 — Flow 7 (Outlook delete → Beeper):** Emit `simplevent.ChatDelete{OnlyForMe:false}`. Suppress to avoid echo.

---

## Task 8: Deploy + end-to-end verification + cleanup

- [ ] In-place `redeploy_service`; wait fresh `Bridge started`; confirm clean boot (no decrypt error, subscription active).
- [ ] **Drop `EMAILDAWG_RESYNC=1`** from the service env via `update_service` (full env set: `EMAILDAWG_CONFIG_B64`, `EMAILDAWG_PASSPHRASE`) so resync stops running on every boot now that read-state backfill is done.
- [ ] Verify all four flows end-to-end (archive/delete each direction) with NO ping-pong (suppression holds — logs/DB show a single action per change, no echo).

---

## Self-Review

- **Spec coverage:** flow 4 = Task 3; flow 5 = Task 6; flow 6 = Task 4; flow 7 = Task 7; Graph primitives = Task 1; loop protection = Phase 2 `suppressCache` reused throughout; detection = Task 5. Reply (flow 8) is Phase 4.
- **Probe-first integrity:** Tasks 3–4 are intentionally not fully coded — their triggers are empirically unknown and are confirmed by Task 2's probe before being written. This is the agreed approach, not a placeholder gap.
- **Type consistency:** suppression key = `internetMessageID` everywhere (matches Phase 2). Tag type `event.RoomTag`. Graph dest strings are well-known folder names `"archive"`/`"deleteditems"`.
- **Loop safety:** every self-initiated change suppresses `internetMessageID` before the Graph/Matrix call; the echoing webhook/delta or Matrix event checks suppression and drops.
