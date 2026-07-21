# emaildawg

## Project Context

emaildawg er en Matrix-til-email-bro bygget på mautrix `bridgev2`-frameworket i Go. Én email-tråd mapper til ét Matrix-rum; deltagere (To/CC/BCC) bliver Matrix ghost-brugere, og vedhæftninger uploades til homeserverens media-repo. Rummene er read-only mod Matrix: broen sender aldrig udgående mail fra Matrix.

Dette er Stefans fork (`srosenlund/emaildawg`) af upstream `iFixRobots/emaildawg`. Forken tilføjer en to-vejs Microsoft Graph/Outlook-sti (`pkg/graph/` + `pkg/connector/graph_*.go`: subscriptions, delta, webhook, archive/delete, read-state, reply) oven på upstreams IMAP IDLE-basis. Go-modulstien er stadig `github.com/iFixRobots/emaildawg`; den kanoniske deploy-remote er forken. Default-branch = `feat/graph-twoway`.

Drift: kører på NiPoGi som Komodo-stack `emaildawg`, bygget fra `Dockerfile.sliplane` (Debian-slim med shell, ikke distroless). Graph-webhook-endpointet (port 29319) er bundet til host-LAN-IP og reverse-proxyes offentligt via Caddy som `emaildawg.stefanrosenlund.dk`, så Microsoft Graph kan nå validerings- og notifikations-URL'en. Broen fodrer Stefans Beeper/Matrix.

### Byg og kør
- **Stack:** Go 1.23 (toolchain 1.24.6), CGO påkrævet. `libolm` er nødvendig for E2EE.
- **Byg:** `make build` (sætter selv CGO_CFLAGS/CGO_LDFLAGS + libolm-prefix pr. platform) → binær `./emaildawg`. `make dev-deps` installerer libolm.
- **Test/verify:** `make test` (wrapper om `go test ./...` med CGO+libolm-flags).
- **Config:** `config.yaml` (bbctl/websocket mod Beeper hungryserv) eller `--generate-example-config`. SQLite-data i `./data/` (host) eller `/home/nonroot/app/data` (container).

### Struktur
- `cmd/emaildawg/main.go` · entrypoint (`mxmain.BridgeMain`, registrerer `connector.EmailConnector`).
- `pkg/connector/` · bridgev2-connector: login, commands, database, plus Graph-stien (`graph_delta.go`, `graph_poll.go`, `graph_deliver.go`, `graph_selfheal.go`, `graph_archive_delete.go`, `graph_subscription.go` m.fl.).
- `pkg/graph/` · Microsoft Graph-klient: `subscriptions.go`, `delta.go`, `attachments.go`, `webhook.go`, `token.go`, `folders.go`.
- `pkg/imap/`, `pkg/email/`, `pkg/matrix/`, `pkg/reliability/`, `pkg/logging/`, `pkg/common/`, `pkg/coordinator/`.
- `docs/superpowers/` · specs og planer for Graph-to-vejs-synk (readpath, readstate, archive/delete, reply, reader-mode).

## Metodik

Dette repo følger den kanoniske udviklings- og deployment-tilgang i `claude-loadout/core/METHODOLOGY.md` (fingerprint stack og domæne · vault-secrets aldrig disk · git draft-PR · propose-only · verifikation før "done" · superpowers-triggers) samt klasse-profilen `core/profiles/code.md` (Go: verify = `go test ./...` + `go vet ./...`, i praksis via `make test` der sætter CGO+libolm-flags). På dev-box er kerne og profil i kontekst via user-memory. Repo-specifikke regler nedenfor vinder ved konflikt.

## Gotchas

- **libolm/CGO er obligatorisk.** Byg og test kræver `libolm` + `CGO_ENABLED=1`. Brug `make build` / `make test` som auto-detekterer libolm-prefix (macOS: `brew install libolm`; Linux: `libolm-dev`). Plain `go test ./...` / `go vet ./...` fejler uden CGO-flags. **Byg eller kør ALDRIG med `nocrypto`** · libolm er nødvendig for korrekt E2EE.
- **`data/` committes aldrig.** Mappen indeholder crypto-salt plus connector- og Graph-state (gitignored). Ingen `*.db`, `*.log`, `config.yaml`, `registration.yaml`, `.env` i git.
- **Secrets via vault, aldrig disk.** `EMAILDAWG_PASSPHRASE` krypterer de gemte email-credentials. I prod injiceres secrets via BWS (homelab-standard; entrypoint re-exec'er under `bws run` når `BWS_ACCESS_TOKEN` er sat), med Doppler som fallback, renderet til `/opt/emaildawg/.env` af Komodo. Passphrase/graph_state-nøglen SKAL pinnes i BWS, ellers churner Graph-subscriptionen ved hver rotation.
- **Deploy er GitOps, ikke manuelt.** NiPoGi-stacken bygger lokalt fra `Dockerfile.sliplane` via Komodo (poll). Manipulér ALDRIG containeren manuelt (ingen manuel `docker restart`/redeploy/deploy uden Stefans godkendelse). Graph-webhook-porten 29319 er LAN-bundet bag Caddy.
- **Fork-drift.** Modulstien peger på upstream (`iFixRobots`), men den aktive kilde er `srosenlund`-forken med Graph-to-vejs-synk. Læs `docs/superpowers/specs/` før du rører Graph-stien.
