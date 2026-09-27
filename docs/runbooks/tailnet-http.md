---
title: "Tailnet HTTP Services"
status: draft
purpose: "Install, expose, restrict, verify, and remove the loopback HTTP gateways that serve tailnet clients such as Muse."
covers:
  - scripts/run-obsidian-http.sh
  - scripts/run-ynab-http.sh
---

# Tailnet HTTP Services

Behavior and access model: [gateway.md](../gateway.md#tailnet-http-services).

## Prerequisites

- The installed gateway binary includes `--allowed-host` (release a commit
  containing it first: [local release](local-release.md)).
- Add the Mac's tailnet name to both `.env.local` and `.env.ynab.local`:

  ```
  MCP_GATEWAY_ALLOWED_HOST=<mac>.<tailnet>.ts.net
  ```

  `tailscale status --json` reports it as `Self.DNSName` (drop the trailing dot).

## Restrict the tailnet first

Tailscale policy is the only access control. Before exposing anything, make
sure the policy admits only your own devices to this Mac on tcp:443. Tagged
devices (anything that is not logged in as you) must get nothing. Add policy
`tests` so Tailscale refuses to save a policy that breaks this, for example:

```json
"tests": [
  {"src": "<your-login>", "accept": ["<mac-tailscale-ip>:443"]},
  {"src": "tag:<other-tag>", "deny": ["<mac-tailscale-ip>:443"]}
]
```

## Install

```bash
make install-launchagent SERVER=obsidian-http
make install-launchagent SERVER=ynab-http
make verify-live SERVER=obsidian-http
make verify-live SERVER=ynab-http
```

Once loaded, `make release` captures, restarts, and rolls back both services
with the rest of the supervised set.

## Expose on the tailnet

```bash
tailscale serve --bg --set-path /obsidian http://127.0.0.1:8765
tailscale serve --bg --set-path /ynab     http://127.0.0.1:8768
tailscale serve status
```

Use `serve`, never `funnel`. The entries appear as `(tailnet only)`.

Client endpoints:

- `https://<mac>.<tailnet>.ts.net/obsidian/mcp`
- `https://<mac>.<tailnet>.ts.net/ynab/mcp`

## Verify

From any admitted tailnet device, send an MCP `initialize` and `tools/list` to
each endpoint. Expect HTTP 200 with `serverInfo.name` equal to `obsidian` or
`ynab`. A request carrying a different `Host` header, or a cross-site browser
`Origin`, returns 403.

## Remove

```bash
tailscale serve --set-path /obsidian off
tailscale serve --set-path /ynab off
make uninstall-launchagent SERVER=obsidian-http
make uninstall-launchagent SERVER=ynab-http
```
