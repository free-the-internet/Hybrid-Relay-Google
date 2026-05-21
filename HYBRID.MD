# HYBRID MODE GUIDE (SaffronBridge)

This guide explains how to run:
- Request path: Apps Script relay
- Response path: Google Drive (FlowDriver engine)

Hybrid note:
- Client now does short request coalescing (~150ms window, up to 16 uploads per relay call) before sending to Apps Script.
- This reduces Apps Script invocation count during bursty traffic.

It is written for two machines:
- Iran client machine
- EU VPS exit machine

## 1. Which binary runs where

- Iran client machine: `bin/client`
- EU VPS exit machine: `bin/hybrid-exit`

Optional:
- `bin/server` is for normal Drive-only server mode. It is not required for this hybrid mode.

## 2. Google Cloud setup (Drive API + OAuth)

1. Open Google Cloud Console and create/select a project.
2. Enable **Google Drive API**.
3. Go to **OAuth consent screen** and configure app name/email.
4. Create **OAuth Client ID** of type **Desktop app**.
5. Download the JSON credentials file (use as `credentials.json`).
6. In OAuth consent screen, click **Publish app** (Production).

Why publish:
- If app stays in Testing, refresh tokens often expire after 7 days.
- Published app avoids the frequent 7-day re-auth problem for normal use.

## 3. Generate OAuth token once

Do this once on a machine where browser login is possible.

Config naming note:
- Templates in `hybrid/SaffronBridge/starter` are `.jsonc` example files.
- For running binaries, copy them to plain runtime files like `client_config.json` and `server_config.json`.

1. Build binaries:
   ```bash
   go build -o bin/client ./cmd/client
   go build -o bin/hybrid-exit ./cmd/hybrid-exit
   ```
2. Create client config from [hybrid/SaffronBridge/starter/client_config.example.jsonc](hybrid/SaffronBridge/starter/client_config.example.jsonc).
   - Save runtime file as `client_config.json`.
3. Run client once:
   ```bash
   ./bin/client -c client_config.json -gc credentials.json
   ```
4. Complete browser auth and paste redirected URL/code in terminal.
5. Confirm token file is created near credentials:
   - `credentials.json.token`
6. Copy both files to EU VPS:
   - `credentials.json`
   - `credentials.json.token`

## 4. Deploy Apps Script relay

Use [hybrid/SaffronBridge/starter/appscript_relay.gs](hybrid/SaffronBridge/starter/appscript_relay.gs).

1. Open https://script.google.com and create a new project.
2. Replace default code with `appscript_relay.gs` content.
   - Important: this script must return JSON fields `ok`, `relay_status`, `relay_body`, `error`.
   - Client now validates these fields strictly before treating a relay upload as success.
3. Edit these values:
   - `RELAY_URL`
   - `SHARED_TOKEN`

`RELAY_URL` options:
- Direct to VPS port:
  - `http://YOUR_VPS_IP:8099/relay`
- Or reverse proxy on 443:
  - `https://your-domain/relay`

4. Deploy as **Web app**:
   - Execute as: **Me**
   - Who has access: **Anyone** (or allowed equivalent)
5. Copy the **Deployment ID**.

In client config, `hybrid_relay.appscript_url` can be either:
- Deployment ID only (`AKfycb...`), or
- Full URL (`https://script.google.com/macros/s/.../exec`)

## 5. Configure EU VPS (exit node)

1. Create config from [hybrid/SaffronBridge/starter/exit_config.example.jsonc](hybrid/SaffronBridge/starter/exit_config.example.jsonc).
   - Save runtime file as `server_config.json`.
2. Set:
   - `google_folder_id` (same folder used by client)
   - `hybrid_relay.shared_token` (must match Apps Script token)
   - `hybrid_relay.exit_listen_addr`
3. For direct mode use:
   - `"exit_listen_addr": "0.0.0.0:8099"`
4. Open firewall/security-group inbound TCP 8099.
5. Run on VPS:
   ```bash
   ./bin/hybrid-exit -c server_config.json -gc credentials.json
   ```

## 6. Configure Iran client

1. Create config from [hybrid/SaffronBridge/starter/client_config.example.jsonc](hybrid/SaffronBridge/starter/client_config.example.jsonc).
   - Save runtime file as `client_config.json`.
2. Set:
   - `storage_type: "saffronbridge"`
   - `google_folder_id` (same as VPS)
   - `hybrid_relay.appscript_url` (Deployment ID or full /exec URL)
   - `hybrid_relay.shared_token` (same as VPS)
3. Transport notes for hybrid:
   - Keep `TargetIP` and `SNI` for fronting.
   - Prefer `SNI: "www.google.com"` for relay stability.
   - Prefer `HostHeader` empty for hybrid relay.
   - In hybrid mode, code now routes hosts safely:
     - Relay calls use script host
     - Drive/token calls are forced to `www.googleapis.com`
   - If config uses `SNI: "google.com"`, client auto-normalizes relay SNI to `www.google.com`.
4. Run client:
   ```bash
   ./bin/client -c client_config.json -gc credentials.json
   ```
5. Use SOCKS5 at `127.0.0.1:1080` (or your configured `listen_addr`).

## 7. Quick checks

- If Apps Script cannot reach VPS:
  - verify `RELAY_URL` includes correct host/port/path (`/relay`)
  - verify VPS firewall allows port
  - verify `hybrid-exit` is running
- If relay returns unauthorized:
  - `SHARED_TOKEN` in Apps Script must equal `shared_token` in configs
- If client still uses Drive uplink:
  - check `storage_type` is `saffronbridge`
- If auth expires unexpectedly:
  - ensure OAuth app is Published, then re-create token once

## 8. Minimum required files on each machine

Iran client machine:
- `bin/client`
- `client_config.json`
- `credentials.json`
- `credentials.json.token`

EU VPS machine:
- `bin/hybrid-exit`
- `server_config.json`
- `credentials.json`
- `credentials.json.token`
