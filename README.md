# cadport

Dynamic port-to-subdomain forwarder for Caddy. Automatically detects local listening ports and creates reverse proxy routes to make them accessible via `{port}.example.com` on a Tailscale network. Currently the setup is hardcoded for my domain, but if you are interested, you may send PRs to make things configurable.

## Prerequisites

- Go 1.26+
- Caddy running with:
  1. Wildcard server configured for or your subdomain;
  2. TLS for your subdomain.
- Caddy admin API accessible at `http://localhost:2019`
- Port forwarding, VPN, Tailscale. Make sure the subdomain is private and accessible.

Example caddy configuration:

```Caddyfile
*.subdomain.domain.com {
	tls {
	    dns cloudflare cfut_TOKEN
        resolvers 1.1.1.1
    }
}
```

## Installation

```bash
make build
```

## Usage

```bash
# Start the daemon (runs in background)
make run

# Start in foreground (for debugging)
make run-fg

# Stop the daemon
make stop
```

## How It Works

This strategy is used by VSCode's port tunneling.

1. **Port Detection**: Reads `/proc/net/tcp` and `/proc/net/tcp6` to find listening ports on `127.0.0.1` and `::1`, then resolves inodes to PIDs by walking `/proc/*/fd/*`

2. **Caddy Integration**: Uses Caddy's admin API to discover the server handling `.local.tomascco.dev` subdomains, then dynamically adds/removes reverse proxy routes

3. **Debouncing**: Ports must be stable for 2 consecutive poll intervals (6 seconds) before being added or removed to avoid flapping

## Excluded Ports

Ports below 1024 and the following are excluded: `80`, `443`, `2019`, `3000`, `8080`

## Debug Endpoints

- `GET http://localhost:9090/ports` — List active port forwards
- `GET http://localhost:9090/health` — Health check

## Architecture

```
cadport/
├── main.go              — Daemon lifecycle, command handling
├── detector/ports.go    — Port detection via /proc/net/tcp
├── caddy/client.go      — Caddy admin API client
└── server/debug.go      — Debug HTTP server
```
