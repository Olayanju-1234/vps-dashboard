# How it works (for a Node/NestJS developer)

A tour of the code with analogies to the Node stack. Read this next to the source.

## The shape of it

One binary, two jobs, chosen by the first argument, the way a CLI built with
`commander` has subcommands:

```
vps-dashboard serve   # the web half: HTTP server + collectors, no privileges
vps-dashboard probe   # the privileged half: reads private files, no network
vps-dashboard demo    # fake data, for working on the UI on a Mac
```

`cmd/vps-dashboard/main.go` is the entry point (think `src/main.ts`). Everything
else lives under `internal/`, which Go treats like a private folder: other
modules can't import it. Each folder is a package, roughly one NestJS module
without the decorators.

| Package | Job | Node analogy |
|---|---|---|
| `config` | load and validate the JSON config | a `class-validator` DTO, but strict: unknown keys fail |
| `envfile` | turn an env file into names + set/empty | `dotenv.parse` keeping only `Object.keys`, but stricter (below) |
| `redact` | mask credential-shaped text in log lines | a pino `redact` list, but by regex |
| `probe` | the privileged loop that writes `probe.json` | a cron worker writing a cache file |
| `journal` | read new journal lines with `journalctl` | `child_process.spawn` + a line reader |
| `nginxlog` | count requests per site from access logs | `tail -f` + counters, never storing paths |
| `systemd` | unit state via `systemctl show` | same, with `execFile` |
| `host` | CPU, memory, disk from `/proc`; sysstat history | `os.loadavg()` / `os.freemem()`, but finer |
| `netstat` | listening sockets from `/proc/net/tcp` | `ss -ltn` without the binary |
| `checks` | health checks and TLS expiry | `fetch` with a timeout; `tls.connect` |
| `github` | tiny GitHub API client with ETags | an axios instance with a cache interceptor |
| `deploy` | work out the deployed commit | see below |
| `collect` | run everything on timers, build the state | a NestJS service with `@Interval` jobs |
| `server` | the JSON API and static files | an Express app with one middleware |
| `web` | the frontend, compiled into the binary | `express.static`, except the files are inside the binary |

## Go things you'll meet

- **`if err != nil { return fmt.Errorf("pkg: doing x: %w", err) }`**: Go has no
  exceptions. Every call that can fail returns an `error` you check, like awaiting
  a promise and handling the rejection on the spot. `%w` wraps the cause, so the
  final message reads like a stack: `deploy: compare: github: rate limited`.
- **Structs with methods** are classes. `func (c *Collector) Fast(ctx)` is
  `class Collector { fast(ctx) {} }`. The `*` means the method can change `c`.
- **Interfaces are structural**, like TypeScript. `server.Source` says "anything
  with `State()`, `History()` and `Journal()`". The real collector and the demo
  both fit, without either saying `implements`.
- **`context.Context`** is an `AbortSignal` that also carries a deadline. Every
  network call gets one, so a slow GitHub can't hang the loop.
- **Goroutines and `sync.Mutex`**: `go f()` starts `f` concurrently (like firing a
  promise without awaiting it). Because they really run in parallel, shared state
  is guarded by a mutex: lock, read or write, unlock. The tests run with `-race`,
  which catches any access that forgot to lock.
- **`defer f.Close()`** is a `finally` block written next to the thing it cleans up.
- **`go:embed`** pastes the `web/static` folder into the binary at compile time,
  so deploying is copying one file.
- **Build tags**: `statfs_linux.go` and `statfs_darwin.go` hold the same function
  for each OS; the compiler picks one. That's how it builds on the Mac for tests
  and on Linux for the server.

## Why two processes

To list env-file names you must open the file, which means the values pass
through memory. So the process that opens them (`probe`) is given no network at
all by systemd (`PrivateNetwork=yes`), and the process that has a network
(`serve`) can't read those files. They talk through one JSON file that only ever
contains names, timestamps, counts and already-redacted log lines.

The probe gets one Linux capability, `CAP_DAC_READ_SEARCH`: "ignore file read
permissions". That's narrower than root (it can't write anything, change
anything or bind a port) and it lets the probe read `deploy`-owned `0600` env
files and `postgres`-owned backup folders without touching their permissions.
The binary is owned by root so the service user can't swap it out.

The parser walks the file character by character the way systemd does (systemd
is what loads these files into the apps). Because a careless parser could read a
line from the middle of a multi-line secret as `SOMETHING=...` and show
`SOMETHING` as a "name", any line that systemd *or* dotenv might treat as the
inside of a quoted value is counted as "not understood" instead. Being wrong in
that direction hides a name; being wrong the other way would show a piece of a
secret. It also refuses to open anything that isn't a plain file owned by the
expected user (no symlinks, no named pipes), because the probe can read any file
and must not be pointed at a different one.

`envfile.Scrubber` is the one place values survive the parse: for one probe cycle
they're used to replace any value that shows up in a journal line with
`[env:NAME]`. Its `String`, `Format`, `MarshalJSON` and `LogValue` methods are all
overridden, so `fmt.Println(scrubber)`, `slog.Info("x", "s", scrubber)` and
`json.Marshal` print `[scrubber: 12 values]` and nothing else.

## How the deployed commit is found

Nothing in the deploy procedures had to change:

- **workflowd** is a Go binary. Go records the commit it was built from inside
  the binary (`debug/buildinfo`), so the dashboard reads it straight out.
- **OjuSentinel** is rsynced from the laptop with its `.git` folder, so `HEAD` is
  on disk. The repo is private, so instead of asking GitHub it compares with the
  `origin/main` that was copied along.
- **liftoff** and **Hookdash** are deployed from `git archive`, which leaves no
  `.git`. Git names every file by `sha1("blob <size>\0" + content)`. The
  dashboard computes that for each tracked file on disk, fetches the file list
  of the last 20 commits on `main` from GitHub, and picks the newest commit
  whose files all match. Hookdash's `api/` and `web/` are matched separately
  because they're deployed on different days.

## The frontend

Three files, no build step: `index.html`, `styles.css`, `app.js`. `app.js`
fetches `/api/state` every 10 seconds and re-renders the current view from a
template string, like a tiny React without the virtual DOM. Every value from the
API goes through `esc()` before it touches HTML, because log lines are arbitrary
text. Charts are hand-written SVG paths; hovering one finds the nearest sample by
binary search and moves a crosshair.
