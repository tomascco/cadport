# cadport — Dynamic Port-to-Subdomain Forwarder

## Purpose

A Go daemon that monitors localhost ports and automatically creates Caddy reverse proxy routes, making ports accessible via `{port}.local.tomascco.dev` on a Tailscale network.

## Architecture

```
cadport run
├── detector/ports.go   — reads /proc/net/tcp, resolves inodes to PIDs
├── caddy/client.go     — calls Caddy admin API (localhost:2019)
└── server/debug.go     — :9090 debug endpoints
```

## Port Detection

- Parse `/proc/net/tcp` only (IPv4 only; `::1` ignored)
- Filter: state `0A` (LISTENING), address `127.0.0.1` only
- Resolve inode → PID: walk `/proc/*/fd/*`, readlink, match socket inodes (VSCode approach)
  - Read `/proc/net/tcp` → extract local_address, state, inode
  - Execute `ls -l /proc/[0-9]*/fd/[0-9]* | grep socket:` → build inode→PID map
  - Read `/proc/<pid>/cmdline` for each PID → process name
- Excluded: ports < 1024, 80, 443, 2019, 3000, 8080; processes `caddy` and self
- Self-PID: `os.Getpid()` called once at startup
- Debounce: port must be stable for 2 consecutive poll intervals (6s) before add/remove. Track candidate state per port.

## Caddy Integration

- Inject routes inside existing wildcard server (e.g. `srv0`) — TLS inherited, no separate server blocks.
- Discovery: `GET /config/apps/http/servers` → find server with `*.local.tomascco.dev` host matcher.
- New port → `POST /config/apps/http/servers/SRV0/routes/-` (append):
  ```json
  {
    "match": [{"host": ["PORT.local.tomascco.dev"]}],
    "handle": [{"handler": "reverse_proxy", "upstreams": [{"dial": "127.0.0.1:PORT"}]}],
    "@id": "cadport-PORT"
  }
  ```
- Port gone → `DELETE /config/apps/http/servers/SRV0/routes/cadport-PORT` (by `@id`)
- API endpoint: `http://localhost:2019`
- Startup: reconcile — list `@id`-tagged routes in `srv0`, diff against detected ports. Remove stale, add missing.
- Graceful shutdown: SIGTERM/SIGINT → delete all routes tagged `cadport-*`, then exit.

## Debug Server

- `:9090`
- `GET /ports` — list detected ports and forward status
- `GET /health` — 200 OK

## Rules

- Does not manage Caddy lifecycle (expects Caddy running)
- Runs as foreground process
- Ports < 1024 excluded
- Excludes ports 80, 443, 2019, 3000, 8080
- Excludes source processes `caddy` and self
- IPv6 (`::1`) ignored; IPv4 only
- No Caddy API error retry — lifecycle managed externally

## File Layout

```
cadport/
├── main.go
├── detector/ports.go
├── caddy/client.go
└── server/debug.go
```

## Commands

- `cadport run` — start the daemon
