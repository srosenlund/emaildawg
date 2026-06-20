# emaildawg → Microsoft Graph two-way sync — Design

**Status:** Approved 2026-06-20
**Owner:** Stefan Rosenlund (srosenlund/emaildawg fork)

## Goal

Turn emaildawg from a read-only IMAP email→Matrix bridge into a **full two-way
sync** between a Microsoft 365 mailbox (`mail@stefanrosenlund.dk`) and Beeper:
new mail, read/unread, archive, delete — all synced in both directions — plus
replying from Beeper. Built on Microsoft Graph (not IMAP), in real time via
Graph change notifications (webhooks).

## Why Graph, not IMAP

IMAP is built to *read* mail, not to sync state. Graph has clean APIs for
read-state (`isRead`), move (archive), delete, and threaded send, plus real-time
change notifications. The mailbox's Entra app already holds `Mail.ReadWrite` +
`Mail.Send` (app-only), and the XOAUTH2 token-provider built in the read-only
phase is reused as-is (just the Graph resource scope instead of the IMAP one).

## Architecture

Keep the **bridgev2 framework** (Matrix↔Beeper layer, portal/room mapping,
threading, attachment upload — all the ~100 KB of battle-tested emaildawg code).
Replace only the transport layer: retire `pkg/imap/` in favor of a new
`pkg/graph/`.

### New components

| Component | Responsibility |
|---|---|
| `pkg/graph/client.go` | Graph API calls: list/get messages, `PATCH isRead`, `move`, `delete`, `createReply`/`sendMail`. Reuses the XOAUTH2 token-provider (Graph scope). |
| `pkg/graph/subscriptions.go` | Create + renew Graph change-subscriptions (~3-day expiry), persist subscription id. |
| `pkg/graph/webhook.go` | HTTP endpoint receiving notifications; validates `validationToken` (creation) + `clientState` (every callback); dispatches to connector. |
| `pkg/graph/delta.go` | Initial backfill + reconciliation via `/messages/delta` (fallback for missed webhooks). |
| Connector Matrix→Graph handlers | read-receipt → `PATCH isRead`; room archive → `move`; redaction → `delete`; Matrix message → `createReply`. |

### Deployment change

The Sliplane service becomes `public: true` with an HTTPS domain so Microsoft can
deliver webhook callbacks. The webhook URL must be live **before** a subscription
is created (Graph validates the endpoint at creation). Everything else
(XOAUTH2 token-provider, config-injection via `EMAILDAWG_CONFIG_B64`, volume)
carries over from the read-only deployment.

## Sync flows

| # | Flow | Trigger | Graph action |
|---|---|---|---|
| 1 | New mail → Beeper | webhook `created` | `GET /messages/{id}` → create Matrix room + message |
| 2 | Read in Outlook → Beeper | webhook `updated` (isRead) | send Matrix read-receipt |
| 3 | Read in Beeper → Outlook | Matrix read-receipt | `PATCH /messages/{id}` `{isRead:true}` |
| 4 | Archive in Beeper → Outlook | Beeper archive-chat | `POST /messages/{id}/move` → Archive folder |
| 5 | Archived in Outlook → Beeper | webhook (folder change) | archive Matrix room |
| 6 | Delete in Beeper → Outlook | Beeper delete-chat | `DELETE /messages/{id}` → Deleted Items (soft) |
| 7 | Deleted in Outlook → Beeper | webhook `deleted` | tombstone/remove Matrix room |
| 8 | Reply from Beeper → Outlook | Matrix message in room | `POST /messages/{id}/createReply` + send, correct threading; sender `mail@stefanrosenlund.dk`; attachments uploaded as Graph attachments |

### Key design decisions

- **Delete is soft** — move to Deleted Items, not permanent. Recoverable in
  Outlook. Hard-delete can be a later option.
- **Feedback-loop protection (critical):** every two-way flow risks a loop
  (Beeper marks read → bridge PATCHes Graph → Graph fires `updated` webhook →
  bridge must NOT re-send to Beeper). Mitigation: a short-lived suppression
  cache that ignores webhooks for changes the bridge itself just made.
- **Reply always in-thread** via `createReply` (preserves In-Reply-To /
  References).

## State & persistence

- **Thread mapping:** one email thread (Graph `conversationId`) = one Matrix
  room. Reuses bridgev2 portal mapping. `conversationId` is the primary thread
  key (supersedes emaildawg's Message-ID threading).
- **Message mapping:** Graph `message-id` stored as bridgev2 `remote_id` — the
  key every state flow looks up.
- **New table:** subscription id + expiry (renewal) + delta-token (reconciliation).

## Implementation phases

The end goal is all flows, but built and shipped in order — each phase is
independently deployable and testable:

1. **Graph read-path** — new mail → Beeper via Graph (replaces IMAP read-only) +
   webhook infrastructure + subscription renewal. The foundation everything
   else hangs on.
2. **Read-state two-way** (flows 2 + 3) — builds on message mapping.
3. **Archive + delete two-way** (flows 4–7).
4. **Reply/send** (flow 8) — most complex, last.

## Risks & mitigations

| Risk | Mitigation |
|---|---|
| Webhook subscription expires / renewal cron fails | Delta-polling fallback catches missed changes |
| Graph `conversationId` vs emaildawg Message-ID threading | Unify in Phase 1 — conversationId is primary |
| Graph rate-limiting / throttling | Batch + exponential backoff (reuse pattern from agent's Graph layer) |
| Spoofed webhook notifications | Validate `clientState` on every callback |
| Webhook URL must be live before subscription creation | Deploy public service first, create subscription after health-check |
| Loops between directions | Suppression cache (see design decisions) |

## Out of scope (v1)

- Multiple mailboxes (single mailbox: `mail@stefanrosenlund.dk`)
- Hard-delete (soft-delete only)
- Calendar / contacts (mail only)
- Non-Microsoft providers (Graph-specific)
