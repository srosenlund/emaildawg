# Reader-mode HTML for Matrix/Beeper rendering

**Date:** 2026-06-23
**Status:** Approved design, ready for implementation plan
**Branch context:** `feat/graph-twoway`

## Problem

Incoming email HTML is passed almost raw into the Matrix `formatted_body`
(`pkg/email/processor.go` → `ConvertMessage()`, l. 849–1237). The only current
cleaning is comment/script/style removal, entity unescape, invisible-Unicode
filtering, and a size-minify above 24 KiB.

Matrix clients (Beeper) only render a limited HTML subset
(`org.matrix.custom.html`). Everything outside it — nested layout tables, inline
CSS, spacer graphics, tracking pixels — is either stripped or rendered as an ugly
wall. The two worst offenders in practice:

1. **Newsletters / marketing** — table layouts, buttons, spacer images, inline CSS.
2. **Inline images & logos** — tracking pixels and tiny logos clutter the view.

## Goal

Render incoming HTML email as **reader-mode**: flat, legible text with headings,
paragraphs, lists, links and meaningful images preserved — but layout tables,
button styling, and spacer/tracking graphics removed. Comparable to Safari Reader.
Faithful visual reproduction is impossible in Matrix anyway, so we optimise for
readability.

## Approach

Whitelist-sanitizer + targeted DOM transforms (chosen over HTML→Markdown→re-render
and over a minimal minify-only extension).

- Parse HTML with `golang.org/x/net/html` (DOM, not the current regex/string
  munging — both `x/net` and `goldmark` are already deps).
- Run a small set of DOM transforms.
- Final whitelist pass to the Matrix-supported tag set.
- Output stays **HTML** in `formatted_body`, so the existing CID→MXC image
  pipeline (l. 906–997) is not disturbed.

This hits both symptoms (newsletters + image clutter), stays in HTML so the image
pipeline is preserved, and adds XSS hardening for free.

## Architecture

### Module boundary & insertion point

New file `pkg/email/readermode.go` exposing a single pure function:

```go
func toReaderModeHTML(html string, opts readerModeOptions) (cleanHTML, plainText string)
```

- No I/O, no network → trivially unit-testable.
- Called from `ConvertMessage()` **after** the CID→MXC rewrite (~l. 997) and
  **before** `FormattedBody` is assigned (l. 1027). It therefore operates on the
  already-resolved `mxc://` images, so the image filter sees the final images and
  the pipeline does not break.
- The returned `plainText` replaces the current plaintext `body` fallback.

### Reader-mode pipeline (single DOM walk)

Parse the post-MXC HTML with `x/net/html`, then in order:

1. **Drop tracking/spacer images** — `<img>` with `width`/`height` ≤ threshold,
   exact `1x1`, or `display:none` (see Image policy).
2. **Unwrap layout tables** — tables that are `role="presentation"`, single-cell,
   or whose cells contain block / image / nested-table content → linearised into
   document flow. **Genuine data tables** (have `<th>`, or only short text cells)
   are **preserved**.
3. **Strip presentational attributes** — remove `style`, `bgcolor`, `align`,
   `class`, `role`, and `width`/`height` on non-image elements; unwrap `<font>`.
4. **Whitelist** — serialise, then run `bluemonday` with a Matrix-subset policy:
   `a, p, br, b/strong, i/em, u, del, h1-h6, blockquote, ul/ol/li, pre, code, hr,
   span, img[mxc src + alt], table/thead/tbody/tr/th/td`. Everything else dropped.
   Provides XSS hardening as a side effect.
5. **Plaintext** — run the existing `simpleHTMLToText()` on the cleaned HTML to
   produce the plain `body`.

### Image policy (smart filter)

Drop an `<img>` when:
- `width`/`height` attribute **or** inline `style` dimension ≤ threshold
  (default **32 px**), **or**
- exactly `1x1`, **or**
- `display:none`.

Keep images **without** explicit small dimensions (hero / product images rarely
carry small dims). Complements the existing `<1 KB`-at-extraction filter
(`processor.go` l. 588).

## Dependencies

- **New:** `github.com/microcosm-cc/bluemonday` (industry-standard HTML whitelist).
- **Already present:** `golang.org/x/net/html`, `goldmark` (indirect).
- **Alternative considered:** hand-rolled allowlist on `x/net/html` (no new dep) —
  rejected because it makes us own XSS edge cases (mutation-XSS etc.). bluemonday
  is hardened and maintained.

## Configuration

- `READER_MODE` env flag — default **on**. Lets the whole pass be disabled / A/B'd
  live without a redeploy of logic.
- `READER_MODE_MIN_IMG_PX` env — default **32**. Tunes the image-drop threshold.

## Testing (TDD)

`pkg/email/readermode_test.go`, table-driven with fixture emails:
- A real newsletter (layout tables + tracking pixel + hero image).
- A data-table email.
- A plain reply.

Assertions:
- Tracking pixel removed; hero image kept.
- Layout table linearised; data table preserved.
- Output passes the Matrix whitelist (no disallowed tags).
- No `style=` attribute remains.
- `READER_MODE=off` is a pass-through (output == input behaviour of today).

## Scope

**In:** the IMAP→Matrix HTML path (`ConvertMessage`).

**Out (YAGNI):**
- Quoted-reply collapsing & signature trimming — reply threads were not flagged as
  a pain point.
- Graph path (`pkg/connector/graph_deliver.go`) currently delivers plain text only,
  so it is unaffected. Reader-mode for Graph would require Graph to deliver an
  HTML body — flagged as a possible follow-up, not in this scope.
