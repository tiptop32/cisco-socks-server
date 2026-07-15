# cisco-socks-server

SOCKS5 proxy through Cisco AnyConnect VPN.

Fork of [merzzzl/cisco-socks-server](https://github.com/merzzzl/cisco-socks-server).

## Differences from the original

- **Reachable from LAN devices.** The SOCKS5 listener binds to `0.0.0.0:8080` and pins itself to the detected LAN interface via `IP_BOUND_IF`, so reply traffic egresses through the physical NIC even though Cisco hijacks `192.168.x.x` routes into the tunnel. Other devices on your network can use `<mac-lan-ip>:8080` as their proxy.
- **Built-in DNS server.** Listens on TCP `127.0.0.1:53` and forwards queries over UDP to the corporate DNS servers from the config (`dns_servers`) — for clients that can't resolve intranet names through SOCKS5 alone.
- **Packet filter disabled after connect.** Runs `pfctl -d` once the VPN is up, so LAN clients aren't blocked by the firewall rules Cisco installs.
- **Resilient supervisor.** The service never exits on VPN connect failures: it retries forever with exponential backoff (5s → 1min), waits instead of interfering while the Cisco agent is reconnecting on its own (network drop, laptop sleep), and verifies connect results via `vpn -s state` instead of parsing the unordered `-s connect` event stream (which reported false failures).
- **`dns_servers` is a required config key** (see below).

## Install

Build from source (Go 1.24+):

```bash
git clone https://github.com/tiptop32/cisco-socks-server.git
cd cisco-socks-server
make build
```

## Config

Create `~/.cisco-socks5.yaml`:

```yaml
user: your-vpn-username
password: your-vpn-password
profile: your-vpn-profile
dns_servers:
  - 10.0.0.1
  - 10.0.0.2
```

## Run

```bash
sudo ./cisco-socks-server
```

Proxy will be available at `localhost:8080`.

The SOCKS5 listener binds itself to the detected LAN interface
(`IP_BOUND_IF`), so reply traffic egresses via the physical NIC even though
Cisco hijacks the routing table for `192.168.x.x` into the VPN tunnel. This
keeps the proxy reachable from other devices on your local network
(e.g., `curl --socks5 <mac-lan-ip>:8080 ...`).

### Flags

- `--no-tui` — disable TUI, plain log output
- `--debug` — enable debug logging
