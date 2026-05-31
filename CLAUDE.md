# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
make build               # build for host platform → ./cisco-socks-server
make build-darwin-arm64  # cross-build for macOS arm64
go build -o cisco-socks-server cmd/*   # raw build (cmd has multiple files, glob is required)
golangci-lint run        # lint with rules from .golangci.yml
go mod tidy              # sync deps
```

Run requires root (binds to `:53` and invokes `/opt/cisco/secureclient/bin/vpn` + `pfctl`):

```bash
sudo ./cisco-socks-server [--no-tui] [--debug]
```

Config is read from `$SUDO_USER`'s home as `~/.cisco-socks5.yaml`. The binary refuses to start if `SUDO_USER` is unset, so `sudo` (not `su -`) is required. Required keys: `user`, `password`, `profile`, `dns_servers` (list).

Releases are tag-driven via `.github/workflows/release.yaml` (matrix darwin/linux × amd64/arm64) — pushing `vX.Y.Z` builds binaries and creates a GitHub release.

There are no tests in this repo.

## Architecture

Three concurrent subsystems coordinated through `service.Service` (`internal/service/service.go`), launched by `Service.Start` via `errgroup` — if any goroutine returns an error, the group cancels the context and all three shut down together.

1. **`startCisco`** (`internal/service/cisco.go`) — supervisor loop that polls `cisco.IsConnected` every 5s, calls `cisco.Connect` when disconnected, runs `pfctl -d` to disable the macOS packet filter after connect, **snapshots the LAN interface + subnet at the start of each tick (before Cisco hijacks the default route) and stores it in `State.LANInterface` so the proxy can bind its listener to that interface**, and signals readiness by **closing the `ciscoReady` channel exactly once**. Handles `cisco.ErrAcquired` by `killall "Cisco Secure Client"` to evict the GUI client. Max 3 connect retries before bailing out (resets to 3 on success). Why the LAN snapshot exists: Cisco installs `192.168.0.0/16 → utun4` on connect, which steals all of `192.168.x.x` — including the LAN — into the tunnel. Reply traffic from the SOCKS5 listener would otherwise leave via `utun4` and get dropped at the corporate gateway, making the proxy unreachable from LAN. We tried fixing this at the routing layer (adding a more-specific /24 → en0 route); Cisco's Network Extension actively deletes any such route within seconds and rewrites the interface back to utun4, so routing-layer mitigations don't survive. The proxy instead uses `IP_BOUND_IF` on its listener socket (see point 2) — a socket option that forces reply egress via a specific interface regardless of the routing table.
2. **`startProxy`** (`internal/service/proxy.go`) — blocks on `<-ciscoReady`, reads `State.LANInterface`, and starts `things-go/go-socks5` on `0.0.0.0:8080` using a `net.ListenConfig.Control` that calls `setsockopt(IPPROTO_IP, IP_BOUND_IF, <iface-index>)` on the listener socket. `IP_BOUND_IF` is inherited by accepted sockets — this guarantees reply traffic egresses via the physical NIC, bypassing the Cisco-controlled routing table. The listener is opened on `tcp4` (not `tcp`) to force a pure AF_INET socket, since `IP_BOUND_IF` is the v4 variant; `IPV6_BOUND_IF` would be needed for a dual-stack listener. If no LAN interface was detected (e.g. utility started before any NIC was up), the listener is created without `IP_BOUND_IF` and reply egress follows whatever routing is in effect — proxy still serves localhost but won't reach LAN clients. No auth.
3. **`startDNS`** (`internal/service/dns.go`) — blocks on `<-ciscoReady`, then listens **TCP** on `127.0.0.1:53` (uses `miekg/dns`) and forwards each query over **UDP** to the configured `dns_servers` in order, returning the first successful response. TCP-in / UDP-out is intentional: macOS resolver hits localhost over TCP, the Cisco-tunneled upstream DNS only accepts UDP.

`ciscoReady` gating is the load-bearing primitive — proxy and DNS must not start before VPN routes exist, or traffic leaks outside the tunnel. The channel is created once in `New` and closed once in the cisco supervisor; do not re-create or re-close it.

`State` (CiscoConnected / PFDisabled / ProxyStarted / DNSStarted) is mutated only through `setStatus(func(*State))` under `sync.RWMutex`. The TUI reads via `GetState`.

### Cisco CLI wrapper (`internal/utils/cisco/cisco.go`)

Wraps `/opt/cisco/secureclient/bin/vpn -s {connect|state|disconnect}`. `parseState` reads lines prefixed `>> state:` and accepts both English (`Connected`/`Disconnected`) and Russian (`Подключено`/`Отключено`) output — the binary's language follows the system locale. `Connect` feeds `user\npassword\ny\n` to stdin. `hasAcquiredError` detects "Connect capability is unavailable" lines so the supervisor can kill the GUI app.

### TUI (`internal/utils/tui/`)

`gocui`-based four-pane layout (banner / logs / status / uptime). The TUI **redirects `os.Stdout` and `os.Stderr` to `/dev/null` before starting** — this is mandatory because the spawned `vpn` CLI writes to the parent's stdio and would corrupt the gocui frame buffer otherwise. It also rewires `slog` via `log.Setup` to feed a bounded channel that the logs pane drains. When `--no-tui` is set, `tui.CreateTUI` is not called and slog writes to `os.Stdout` directly.

### Logger (`internal/utils/log/logger.go`)

Custom `slog.Handler` that emits ANSI-256-colored single-line output (`HH:MM:SS LVL message error=...`). It only surfaces the `error` attr — other attrs are dropped by design (the TUI is narrow).

## Conventions

- Go 1.24, module `github.com/merzzzl/cisco-socks-server`.
- `golangci-lint` runs with `enable-all` minus a long disable list (see `.golangci.yml`); notable enabled rules: `revive` (most rules on), `gofumpt` with `interface{} → any` rewrite, `gci` with local prefix `github.com/merzzzl`.
- All shell-outs go through `internal/utils/cisco` `run()` helper using `exec.CommandContext` — never call `exec.Command` directly so cancellation propagates.
- `ciscoPath` is hard-coded to `/opt/cisco/secureclient/bin/vpn` (macOS only); Linux release artifacts build but won't run.
