# CLAUDE.md

Read-only, project-organised dashboard for a small shared server. One Go binary,
two roles: `serve` (web UI + JSON API on 127.0.0.1, no privileges) and `probe`
(CAP_DAC_READ_SEARCH, no network, writes `/var/lib/vps-dashboard/probe.json`).
The owner is a Node/NestJS developer new to Go: explain Go with Node analogies
(`docs/how-it-works.md` is the tour).

## Build and test

```sh
make ci      # gofmt check, vet (also linux/arm64), go test -race, release build
make demo    # made-up data on 127.0.0.1:8400; the only way to see the UI on macOS
```

## Invariants worth not breaking

- No env value may leave `internal/envfile`: `state.EnvVar` has only name/set/duplicate
  (a reflection test enforces it), and `envfile.Scrubber` overrides every way of
  printing itself. `internal/e2e/nosecrets_test.go` must stay green and must fail
  if scrubbing or redaction is switched off.
- The probe never gets network; the server never gets file privileges. New
  collectors that read private files go in the probe and reach the server only
  as names, counts, timestamps or redacted text through the snapshot.
- nginx paths and query strings are never stored, only counts.
- Every HTTP route is GET; the Host header must be loopback.
- The frontend escapes every API string with `esc()`; CSP allows scripts from 'self' only.

## Conventions

gofmt; errors wrapped as `fmt.Errorf("pkg: doing x: %w", err)`; `log/slog`;
comments 1-2 lines, only for the why. Conventional Commits, no Co-Authored-By,
no "Generated with Claude Code" footer, never `--no-verify`. Merge your own green
PRs on Olayanju-1234 repos and deploy. The real server config and layout live in
the git-ignored `config.local.json` and `CLAUDE.local.md`; never commit them.
