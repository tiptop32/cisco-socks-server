# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
make build               # build for host platform → ./cisco-socks-server
make build-darwin-arm64  # cross-build for macOS arm64
make test                # go test ./... (pure parsers only; nothing needs root)
make vet                 # go vet ./...
golangci-lint run        # lint with rules from .golangci.yml
go mod tidy              # sync deps
```

Run requires root (binds to `:53` and invokes `/opt/cisco/secureclient/bin/vpn` + `pfctl`):

```bash
sudo ./cisco-socks-server [--no-tui] [--debug]
```

Config is read from `$SUDO_USER`'s home as `~/.cisco-socks5.yaml`. The binary refuses to start if `SUDO_USER` is unset, so `sudo` (not `su -`) is required. Required keys: `user`, `password`, `profile`, `dns_servers` (list). Optional: `lan_clients` — list of `{ip, mac}` entries for LAN hosts that must reach the proxy while the VPN is up (see LAN client pinning below); both fields are validated at startup (`net.ParseIP`/`net.ParseMAC`).

Releases are tag-driven via `.github/workflows/release.yaml` (matrix darwin/linux × amd64/arm64) — pushing `vX.Y.Z` builds binaries and creates a GitHub release.

Tests cover only the pure output parsers (`cisco.parseState`, `route.arpEntryMatches`, `route.parseDefaultNonTunnel`); everything else needs root and a live VPN. Linux targets compile (interface binding is stubbed out in `bind_other.go`) but the binary is macOS-only at runtime.

## Architecture

Three concurrent subsystems (plus an optional LAN-client pinner) coordinated through `service.Service` (`internal/service/service.go`), launched by `Service.Start` via `errgroup` — if any goroutine returns an error, the group cancels the context and all of them shut down together. Every goroutine waiting on `ciscoReady` must also select on `ctx.Done()`, otherwise Ctrl+C before the first connect hangs `g.Wait()` forever. Periodic loops use `sleepCtx` rather than `time.Sleep`.

1. **`startCisco`** (`internal/service/cisco.go`) — supervisor loop that polls `cisco.Status` every 5s, calls `cisco.Connect` when disconnected, runs `pfctl -d` to disable the macOS packet filter after connect, **snapshots the LAN interface + subnet at the start of each tick until the first success — it is never refreshed afterwards, so a Wi-Fi/Ethernet switch needs a restart — (before Cisco hijacks the default route) and stores it in `State.LANInterface` so the proxy can bind its listener to that interface**, and signals readiness by **closing the `ciscoReady` channel exactly once**. On `cisco.ErrAcquired` (the GUI client holds the connect capability) it only logs and retries — killing the GUI was removed deliberately. The supervisor never bails out on connect failures: it retries forever with exponential backoff (5s doubling up to 1min, reset on success), and when the agent reports `Reconnecting` it waits instead of issuing `connect` — a connect attempt during agent-driven reconnection interferes with it and fails spuriously. The wait is bounded: if `Reconnecting` persists longer than 2 minutes (`ciscoReconnectGrace`), the supervisor forces `vpn -s disconnect` to kick the agent out of the stuck state (observed: the agent can hang in `Reconnecting` indefinitely, and only an explicit disconnect resets it) and then reconnects via the normal loop. `State.PFDisabled` is reset whenever the connection is lost (`Reconnecting`/`Disconnected`), because Cisco re-enables the macOS packet filter on every reconnect and `pfctl -d` must run again. `cisco.Connect` verifies the result via a follow-up `vpn -s state` call because the `-s connect` event stream is unordered (a successful connect can end with a stale `state: Disconnected` line). Why the LAN snapshot exists: Cisco installs `192.168.0.0/16 → utun4` on connect, which steals all of `192.168.x.x` — including the LAN — into the tunnel. Reply traffic from the SOCKS5 listener would otherwise leave via `utun4` and get dropped at the corporate gateway, making the proxy unreachable from LAN. We tried fixing this at the routing layer (adding a more-specific /24 → en0 route); Cisco's Network Extension actively deletes any such route within seconds and rewrites the interface back to utun4, so routing-layer mitigations don't survive (also tried and killed by Cisco: `-ifscope` scoped routes, static `arp -s`, pf `route-to`). The proxy instead uses `IP_BOUND_IF` on its listener socket (see point 2) — a socket option that forces reply egress via a specific interface regardless of the routing table. `IP_BOUND_IF` fixes L3 only, not L2: with the subnet routed into the tunnel the kernel has no on-link route to LAN hosts, so reply frames leave en0 with the **default gateway's MAC** and never reach the client. That is what **LAN client pinning** solves: a separate `startPinner` goroutine (started only when `lan_clients` is non-empty) checks every 1s while connected, read-only via `arp -n`, whether each client still resolves to its MAC on the LAN interface, and only on drift re-asserts a per-host `route change <ip> -interface en0` plus a static ARP entry `arp -S <ip> <mac> temp` (`route.EnsureClientPinned`). The check must stay read-only: `arp -S` deletes and re-adds the entry, and doing it unconditionally drops in-flight frames (visible stalls). Host-level route+ARP pins are the only mac-side change Cisco tolerates (empirically verified: entries survive; broader fixes get deleted). It must be `arp -S` (capital, replaces entry) — lowercase `arp -s` fails with "can only proxy" while the subnet is tunnel-routed. Pin failures are logged at debug level only; an Info line fires each time a client is actually re-pinned. On shutdown the supervisor runs `vpn -s disconnect` (only if it connected the VPN itself) with a 30s timeout so a wedged agent cannot block exit.
2. **`startProxy`** (`internal/service/proxy.go`) — blocks on `<-ciscoReady`, then opens two `tcp4` listeners on port 8080 served by `things-go/go-socks5` (no auth): `127.0.0.1:8080` (mandatory, serves localhost) and, if `State.LANInterface` is known, `0.0.0.0:8080` with `setsockopt(IPPROTO_IP, IP_BOUND_IF, <iface-index>)` set in `net.ListenConfig.Control` (`bind_darwin.go`). `IP_BOUND_IF` is inherited by accepted sockets — this guarantees reply traffic egresses via the physical NIC, bypassing the Cisco-controlled routing table. It is the v4 variant, hence `tcp4` (a dual-stack listener would need `IPV6_BOUND_IF`). The LAN listener is best-effort: if no interface was detected or the bind fails, the proxy serves localhost only. Listeners live for the whole process — a VPN reconnect does not invalidate them, so there is no restart logic. What does happen on VPN loss (`CiscoConnected` true→false, polled every 2s) is that all tracked client connections are closed (`connTracker`): their upstream legs died with the tunnel and clients would otherwise hang until TCP gives up. Transient `Accept` errors back off 500ms and continue; only an unexpectedly closed listener is fatal. Do not put absolute deadlines on client conns — they kill long transfers and idle SSH; dead LAN peers are reaped by Go's default TCP keepalive.
3. **`startDNS`** (`internal/service/dns.go`) — blocks on `<-ciscoReady`, then listens **TCP** on `127.0.0.1:53` (uses `miekg/dns`) and forwards each query over **UDP** to the configured `dns_servers` in order, returning the first successful response. TCP-in / UDP-out is intentional: macOS resolver hits localhost over TCP, the Cisco-tunneled upstream DNS only accepts UDP. Because upstream is UDP, `forwardDNS` adds an EDNS0 OPT (4096) to queries that lack one and strips it from the reply — otherwise answers >512 bytes come back with TC set, which is useless to relay over TCP. The listener is opened by us (not `ListenAndServe`) so a busy `:53` fails before `DNSStarted` is set, and shutdown closes it directly: miekg's `Shutdown` refuses a server that has not started yet, which would otherwise hang `g.Wait()`.

`ciscoReady` gating is the load-bearing primitive — proxy and DNS must not start before VPN routes exist, or traffic leaks outside the tunnel. The channel is created once in `New` and closed once in the cisco supervisor; do not re-create or re-close it.

`State` (CiscoConnected / PFDisabled / ProxyStarted / DNSStarted) is mutated only through `setStatus(func(*State))` under `sync.RWMutex`. The TUI reads via `GetState`.

### Cisco CLI wrapper (`internal/utils/cisco/cisco.go`)

Wraps `/opt/cisco/secureclient/bin/vpn -s {connect|state|disconnect}`. `parseState` reads lines prefixed `>> state:` and accepts both English (`Connected`/`Disconnected`) and Russian (`Подключено`/`Отключено`) output — the binary's language follows the system locale. `Connect` feeds `user\npassword\ny\n` to stdin. `hasAcquiredError` detects "Connect capability is unavailable" lines so the supervisor can kill the GUI app.

### TUI (`internal/utils/tui/`)

`gocui`-based four-pane layout (banner / logs / status / uptime). The TUI **redirects `os.Stdout` and `os.Stderr` to `/dev/null` before starting** — this is mandatory because the spawned `vpn` CLI writes to the parent's stdio and would corrupt the gocui frame buffer otherwise. It also rewires `slog` via `log.Setup` to feed a bounded channel that the logs pane drains. When `--no-tui` is set, `tui.CreateTUI` is not called and slog writes to `os.Stdout` directly. `CreateTUI` takes the service ctx and quits its main loop when ctx is cancelled; `main` waits for the TUI to exit (terminal restored) before re-pointing slog at stdout and printing the fatal error, then exits 1.

### Logger (`internal/utils/log/logger.go`)

Custom `slog.Handler` that emits ANSI-256-colored single-line output (`HH:MM:SS LVL message error=...`). It only surfaces the `error` attr — other attrs are dropped by design (the TUI is narrow).

## Conventions

- Go 1.24, module `github.com/merzzzl/cisco-socks-server`.
- `golangci-lint` runs with `enable-all` minus a long disable list (see `.golangci.yml`); notable enabled rules: `revive` (most rules on), `gofumpt` with `interface{} → any` rewrite, `gci` with local prefix `github.com/merzzzl`.
- Shell-outs without stdin go through `internal/utils/shell.Run` (`exec.CommandContext`, returns output even on failure) — never call `exec.Command` directly so cancellation propagates. `cisco.Connect` is the one exception (credentials via stdin) and still uses `exec.CommandContext`.
- `ciscoPath` is hard-coded to `/opt/cisco/secureclient/bin/vpn` (macOS only); Linux release artifacts build but won't run.
