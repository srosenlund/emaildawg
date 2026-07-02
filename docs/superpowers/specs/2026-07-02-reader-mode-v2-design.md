# Reader Mode v2 — pæn HTML-email-rendering i Beeper

**Dato:** 2026-07-02
**Status:** GODKENDT retning af Stefan ("ja kør") — lagdelt løsning.

## Problem (empirisk verificeret mod rigtige bridged mails)

Reader mode v1 (sanitize + tracking-pixel-drop + tabel-linearisering) efterlader:

1. **Usynlig preheader-padding vises:** hundredevis af `U+034F`/`U+00AD` ("͏ ­") fylder skærmen — `filterInvisibleUnicode` kører kun på plaintext-fallbacken, ikke på `FormattedBody` (readermode.go:78).
2. **Skjult preheader-tekst vises:** "Journalist Christian Bennike hosts..." (display:none/1px-font) — pipeline dropper kun skjulte *billeder*, ikke skjult tekst.
3. **Rå entities i visningen:** `&quot;Daniel&quot; &lt;daniel@...&gt;` — dobbelt-encodede entities (typisk Gmail-forwards) dekodes kun én gang.
4. **Støj:** `________________`-separatorer, `&nbsp;`-runs, gentagne `<br>`, kæmpe overskrifter, footere med unsubscribe/social/juridisk boilerplate.

## Løsning — tre lag i eksisterende reader-mode-pipeline

### Lag 1 — Hygiejne (deterministisk, ALLE mails, kan aldrig klippe indhold)

I `readermode.go`, som nye passes i `toReaderModeHTML` (mellem parse og sanitize):

- **`dropHiddenElements`**: fjern elementer hvor style matcher `display:none`, `visibility:hidden`, `opacity:0`, `font-size:0`/`1px`, `max-height:0`, `mso-hide:all`. Generaliserer den eksisterende billed-logik til alle elementer. Tracking-billede-logikken (minPx) bevares uændret for `<img>`.
- **`stripJunkUnicode`** (tekst-noder i HTML-stien): fjern en KONSERVATIV junk-mængde — `U+034F`, `U+00AD`, `U+200B`–`U+200F`, `U+2060`, `U+FEFF` — IKKE hele Cf/Mn-kategorierne (ville ødelægge legitime kombinerende tegn i ikke-latinsk tekst). Kollaps derefter runs af whitespace+`&nbsp;` (>3 i træk → ét mellemrum).
- **`decodeStableEntities`** (tekst-noder): kør `html.UnescapeString` én ekstra gang på tekst-noder der stadig indeholder `&[a-z]+;`/`&#\d+;`-mønstre (dobbelt-encodede kilder). Accepteret trade-off: emails der bevidst VISER "&lt;" er ekstremt sjældne.
- **`collapseNoise`**: >2 `<br>` i træk → 2; kæder af tomme `<p>`/`<div>` → én; tekst-runs af 5+ `_`/`-`/`=` → `<hr>`.

### Lag 2 — Indholds-ekstraktion (KUN bulk-mails, med retention-guard)

**Gate — mailen deklarerer selv sin type:** nyt felt `ParsedEmail.IsBulk`, sat under header-parse: `List-Unsubscribe`-header til stede ELLER `Precedence: bulk|list`. Personlige og transaktionsmails (ingen af delene) rører Lag 2 aldrig.

**Email-tunet densitets-pruner** (ny fil `pkg/email/extract.go`) — IKKE go-readability: artikel-heuristikker (class-hints, `<article>`, `<p>`-densitet) misfirer på email-tabel-suppe. I stedet, post-linearisering (blokkene er allerede fladet ud af `unwrapLayoutTables`):

1. Segmentér containerens top-level blokke.
2. Score per blok: tekstlængde, **link-densitet** (linkede tegn / alle tegn), footer-nøgleord (unsubscribe, afmeld, view in browser, privacy policy, all rights reserved, ©, "du modtager denne", "why did I get this", social-navne), position (sidste 30% af dokumentet).
3. Drop blokke der er: (høj link-densitet >0.5 OG kort tekst) ELLER (footer-nøgleord OG i bund-zonen) ELLER rene social-/nav-link-rækker.
4. **Retention-guard:** hvis tilbageværende tekst < 40% af input-teksten → smid ekstraktionen væk og brug Lag 1-output. En pruner der er i tvivl, gør ingenting.

**Fejlhåndtering:** panic/fejl i Lag 2 → recover → Lag 1-output. En mail må aldrig tabes eller forsinkes af ekstraktion.

### Lag 3 — Præsentations-polish (alle mails, efter Lag 1/2)

- **Overskrifts-demotion:** `h1`→`h3`, `h2`→`h4` (Beeper renderer h1/h2 skrigende store). h3–h6 uændret.
- Maks ét tomt afsnit i træk (dækket af collapseNoise).
- Links bevares som klikbar tekst (sanitizer-policy uændret).

## Wiring

- `ParsedEmail` (+`IsBulk bool`) sættes i `parseIMAPFetchData`-header-parsen (`msg.Header.Get("List-Unsubscribe")`, `Precedence`).
- `finalizeHTML(origHTML)` → `finalizeHTML(origHTML string, isBulk bool)`; kaldsstedet (processor.go:1039) har `e.emailMessage` i scope.
- Config (`pkg/connector/config.go` + defaults i connector.go): `reader_mode_extract bool` (default true) gater Lag 2. Lag 1+3 er en del af `reader_mode` selv.

## Rækkefølge i toReaderModeHTML

parse → dropHiddenElements → dropTrackingImages → unwrapLayoutTables → [Lag 2 hvis isBulk+config] → stripJunkUnicode → decodeStableEntities → collapseNoise → demoteHeadings → sanitize → plaintext.

## Test

Udvid `readermode_test.go` + ny `extract_test.go` med fixtures fra de verificerede rigtige mails:
- Preheader-padding (U+034F/U+00AD-runs) → væk fra formatted body.
- `display:none`-preheader-tekst → væk; synlig tekst bevaret.
- Dobbelt-encodet `&amp;quot;` → `"`.
- `____`-run → `<hr>`; 4×`<br>` → 2.
- h1 → h3.
- Bulk-mail med footer (unsubscribe + social) → footer væk, brødtekst bevaret.
- Ikke-bulk mail → Lag 2 rører intet (byte-identisk med Lag 1).
- Retention-guard: pruner der ville fjerne 70% → fallback til Lag 1.
- IsBulk: List-Unsubscribe → true; Precedence: bulk → true; ingen → false.

## Ude af scope

- LLM-resumé af mails (forkert lag, dyrt).
- go-readability-dependency (artikel-tunet, misfirer på emails).
- Ændringer i plaintext-mails uden HTML.
