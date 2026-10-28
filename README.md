# Aurum TV API

Go 1.27 · standard library `net/http` · one dependency (`golang-jwt/jwt/v5`)

**[English](#english)** · **[ภาษาไทย](#ภาษาไทย)**

---

<a id="english"></a>

## English

The backend for the **Aurum TV** Android TV app. It serves the channel list and checks every stream link by actually playing it. When a link dies, it switches the channel to a verified backup source. Every request from a TV is HMAC-signed and tied to a rotating session.

It is a drop-in replacement for the earlier Node.js backend. The endpoints, JSON shapes, error codes, headers, signature format and data files are unchanged, so existing data volumes and the current app keep working without changes.

### Features

- **Channels:** list, add, edit, delete, bulk-add and reorder. Clients receive live updates over server-sent events.
- **Self-healing sources:**
  - Each channel has a primary source and backups.
  - A background monitor plays every link: playlist → variant → a real media segment.
  - It fails over **only to a source verified as up**, and switches back once the primary recovers.
- **Resolvers:** a source can resolve its URL at runtime, e.g. `ch3plus` signed links that expire. A new kind of resolver is one file.
- **App authentication** (enforced by default):
  - HMAC-SHA256 request signing, with replay protection and server-time clock correction.
  - Rotating refresh-token sessions, with reuse detection.
- **Admin access:** an admin API key, plus an admin JWT for the log endpoints.
- **Durable storage:**
  - Atomic JSON writes (temp file → fsync → rename) with a backup copy.
  - Automatic schema migration.
  - Daily log files.
  - Every data file is owner-only (0600).
- **Operator CLI:** `aurumctl` covers source health, backup discovery, link sync, restore, smoke test and key generation.

### Quick start

```bash
cp .env.example .env
go run ./cmd/aurumctl genkey   # prints HMAC_KEYS / API_KEY for .env and the app's signing key
go run ./cmd/aurum-api         # or: make run
```

Open `http://localhost:3000/` for a list of all endpoints, and `/api/health` for status.

Requirements: Go 1.27+. Docker is optional.

### Configuration

Every setting is an environment variable. All of them are documented in [`.env.example`](.env.example). A local `.env` file is read too, but variables set by the shell, Docker or the hosting platform always win.

| Variable | Purpose |
|---|---|
| `APP_ENV` | `production` refuses to start without `HMAC_KEYS` |
| `PORT` | listen port (3000) |
| `DATA_DIR` | data folder — **must be a persistent volume** in production |
| `API_KEY` | admin key(s), comma-separated |
| `JWT_SECRET` | signs admin and device tokens; if empty, one is generated and kept in `DATA_DIR` |
| `HMAC_KEYS` | `keyId:secret` pairs shared with the app (**required in production**) |
| `HMAC_MODE` | `enforce` (default) · `log` (debugging only) · `off` |
| `TRUST_PROXY` | `true` only behind a reverse proxy |
| `HEALTH_*` | self-healing monitor tuning (interval, concurrency, thresholds) |

### Authentication

| Caller | Credential | Endpoints |
|---|---|---|
| TV app (read) | HMAC signature + session access token | `GET /api/channels`, `/api/channels/{id}`, `/api/channels/{id}/sources`, `/api/events` |
| TV app (write) | HMAC signature + session access token | `POST /api/channels/{id}/refresh`, `/sync`, `/report`, `POST /api/logs` |
| TV app (session) | HMAC signature | `POST /api/session`, `POST /api/session/refresh` |
| Admin | `X-API-Key` header | create/edit/delete channels, `probe`, `GET /api/health/sources`, `POST /api/auth/token` |
| Admin (logs) | `Authorization: Bearer <admin JWT>` | `/api/logs*` |
| Anyone | — | `GET /api/health`, logo and avatar images |

**HMAC signature.** Headers: `X-TV-Key-Id`, `X-TV-Timestamp`, `X-TV-Nonce`, `X-TV-Signature`.

```
signature = base64url( HMAC-SHA256( secret,
  "v1\n{METHOD}\n{path+query}\n{unix timestamp}\n{nonce}\n{sha256hex(body)}" ) )
```

- **Clock skew:** a timestamp outside ±`HMAC_MAX_SKEW_SEC` gets `401 clock_skew`. The response carries `serverTime`, and the app re-signs using it.
- **Replay:** a nonce is remembered only after its signature has verified. A reused nonce gets `replayed`.
- **Sessions:**
  - `POST /api/session` returns a short-lived access token and a refresh token.
  - `POST /api/session/refresh` rotates the pair.
  - The previous refresh token stays valid for `REFRESH_GRACE_SEC`, in case a response is lost.
  - Reusing an old refresh token after that window revokes the session (`refresh_reused`).
  - Only HMAC hashes of refresh tokens are stored.
- **Rollout:** a client without the key (an old APK) gets `401` as soon as this backend is deployed.

### Security model

| Threat | Defence |
|---|---|
| Unsigned or old clients | `HMAC_MODE=enforce` by default; production refuses to start without keys |
| Replayed requests | timestamp window + nonce cache (bounded; a nonce is recorded only after its signature verifies) |
| Stolen refresh token | rotation on every use; reuse after the grace window revokes the session |
| Timing attacks on keys | SHA-256 then constant-time compare, checking every configured key |
| Admin key leaking over the network | `aurumctl` refuses plain `http://` except for localhost; the key is never accepted in a query string |
| Rate-limit bypass with a forged `X-Forwarded-For` | the proxy-appended (rightmost) address is used, and only when `TRUST_PROXY=true` |
| Oversized bodies, slow clients | global and per-route body limits; read/write/idle/header timeouts; header size cap |
| Too many live connections | concurrent SSE streams are capped (`503 too_many_streams`) |
| Disk filling from app logs | field length caps, an 8 KB `details` cap, a batch cap and a per-day entry cap |
| Path tricks on log dates | strict `YYYY-MM-DD` validation before any file access |
| Link injection via `sync` | HTTPS `.m3u8` only, from `SYNC_ALLOWED_HOSTS` (subdomain match), for resolver channels only |
| Secret leakage in probe errors | network failures are reported as short codes (`network:ECONNREFUSED`), never the signed URL |
| Leaking internals | panics return a generic `500`; validation errors list fields, not internals |
| Browser-side abuse | strict CSP (`default-src 'none'`), `nosniff`, frame blocking, no referrer, HSTS |
| Local file access | data files 0600, data directory 0700; the container runs as a non-root user with no capabilities |

The CI pipeline runs `go vet`, `staticcheck`, `govulncheck` and the race-detector tests on every push.

### Self-healing sources

```json
{
  "name": "One 31",
  "number": 31,
  "sources": [
    { "id": "mirror-3", "label": "mirror 576p", "url": "https://…/playlist.m3u8", "resolver": "static", "priority": 0 },
    { "id": "mirror-2", "label": "mirror 576p", "url": "https://…/playlist.m3u8", "resolver": "static", "priority": 1 }
  ],
  "activeSourceId": "mirror-3",
  "url": "<URL of the active source>"
}
```

1. **Probe.** The monitor runs every `HEALTH_INTERVAL_MS`:
   - Active sources are checked every cycle.
   - Backups are checked every `HEALTH_BACKUP_EVERY_CYCLES` cycles.
   - Sources that are down back off exponentially.
   - At most `HEALTH_CONCURRENCY` probes run in parallel.
2. **Mark down.** A source is marked down after `HEALTH_FAIL_THRESHOLD` consecutive failures. Before any switch, an immediate confirmation probe runs.
3. **Fail over.** The channel switches to the first backup **verified as up**. If no backup is verified, it keeps the current source.
4. **Switch back.** The primary returns after `HEALTH_RECOVER_THRESHOLD` consecutive successes.
5. **App reports.** A report (`POST /api/channels/{id}/report`) only makes a real check happen sooner, and it is rate-limited per channel.
6. **Propagation.** A switch changes the channel's `url` and `updatedAt`, so every TV picks up the new link. Routine probes never touch `channels.json`; health is kept in `source-health.json`.

To add backups, use `PATCH /api/channels/{id}` with `{ "sources": [...] }`, or run `aurumctl discover-sources --apply`.

### API reference

| Method & path | Description |
|---|---|
| `GET /api/health` | status + `serverTime` |
| `GET /api/channels` | channel list (`?q=`, `?group=`, `?enabled=`) |
| `GET /api/channels/{id}` | one channel (`?refresh=true` renews resolver sources) |
| `GET /api/channels/{id}/sources` | sources with health |
| `POST /api/channels/{id}/report` | the app reports a playback failure |
| `POST /api/channels/{id}/refresh` | renew resolver sources now |
| `POST /api/channels/{id}/sync` | the app pushes a fresh resolver link (allowed hosts only) |
| `POST /api/channels/{id}/probe` | probe every source now (admin) |
| `GET /api/health/sources` | health overview of every channel (admin) |
| `POST /api/channels` · `PUT/PATCH/DELETE /api/channels/{id}` | manage channels (admin) |
| `POST /api/channels/bulk` · `POST /api/channels/reorder` | bulk add · reorder (admin) |
| `GET /api/events` | server-sent events on channel changes |
| `POST /api/session` · `POST /api/session/refresh` | device sessions |
| `POST /api/auth/token` | admin JWT from the API key |
| `POST /api/logs` · `GET /api/logs` · `GET /api/logs/dates` · `GET /api/logs/config` · `POST /api/logs/toggle` · `DELETE /api/logs/{date}` | app logs (one JSON file per day) |
| `GET /api/channels/{id}/logo` · `GET /api/avatar?name=` | logo, or a generated avatar |

Every error body has the shape `{ "error": "<message>", "code": "<machine code>" }`. Validation errors also include `issues: [{ path, message }]`.

### Operator CLI — `aurumctl`

`aurumctl` runs from an admin machine against a deployed server. Its settings come from `ops.env` in the working directory (copy [`ops.env.example`](ops.env.example)) or from the shell. Admin commands use the admin key, so they keep working under `HMAC_MODE=enforce`.

| Command | Purpose |
|---|---|
| `aurumctl sources-status [--probe <id>]` | health of every channel's sources |
| `aurumctl discover-sources [--apply] [--channel <id>] [--rules <file>]` | find backup sources from editable rules; only candidates that play end to end are reported or added, and existing sources are never removed or reordered |
| `aurumctl sync-ch3 [--dry-run] [--force] [--loop <minutes>]` | fetch a fresh link for the `ch3plus` resolver source from a network that can reach the site, then push it with the admin key |
| `aurumctl restore-channels [--dry-run]` | re-add seed channels that are missing (idempotent) |
| `aurumctl smoke` | end-to-end check against a running server, signed exactly like the TV app (`SMOKE_URL`, `SMOKE_KEY_ID`, `SMOKE_SECRET`) |
| `aurumctl genkey [--key-id <id>]` | print fresh HMAC and admin keys |

Exit codes: `0` success · `1` failure · `2` missing or invalid configuration. Example cron entry:

```
0 */2 * * * cd <backend folder> && ./bin/aurumctl sync-ch3 >> /tmp/aurum-sync-ch3.log 2>&1
```

### Data and deployment

- **Storage.** Everything lives in `DATA_DIR`: `channels.json` (+ `channels.bak.json`), `sessions.json`, `source-health.json`, `logs/` and `.secret-jwt_secret`.
- **Persistent volume.** Point `DATA_DIR` at a persistent volume, or the data is lost on redeploy.
- **Docker** (`Dockerfile`, `docker-compose.yml`):
  - Static binaries run as the non-root `app` user (uid 1000, the same as earlier releases, so existing volumes keep working).
  - The compose file mounts the `tv-channel-data` volume at `/data`, sets a read-only root filesystem, drops all capabilities, and enables `no-new-privileges`.
  - The healthcheck is `aurum-api healthcheck`.
- **Coolify.** Deploy with Docker Compose and set `API_KEY` and `HMAC_KEYS` in the environment.
- **Migration.** Older `channels.json` files (schema v1) are migrated automatically on start; the previous file is kept as the backup.
- **Release order with the app:**
  1. Set `HMAC_KEYS`, `API_KEY` and (optionally) `JWT_SECRET` on the host.
  2. Build the app with the same HMAC key.
  3. Deploy the backend.
  4. Install the new app.
- **Shutdown.** `SIGTERM` stops accepting requests, finishes in-flight ones, and flushes sessions, health and logs to disk.

### Project structure

```
cmd/
  aurum-api/           server entry point (+ `healthcheck` subcommand)
  aurumctl/            operator CLI
internal/
  config/              environment → typed Config; .env loader; generated JWT secret
  api/                 routes, handlers per area, request validation, response shaping
  auth/                API keys, JWTs · hmacsig/ (signing, nonce cache) · appauth/ (route guards)
  session/             refresh-token sessions (rotation, grace, reuse detection) + store
  channel/             model, normalisation, merge rules, v1→v2 migration, repository, seed
  source/              self-healing: health, selection policy, monitor, refresher · resolver/
  hls/                 HLS playlist parser and end-to-end probe
  stream/              ch3plus link extraction and expiry; sync URL policy
  logs/                daily JSON log files
  middleware/          recover, access log, security headers, CORS, rate limit, body limit
  ops/                 aurumctl commands (+ built-in discovery rules)
  avatar/ · httpx/     generated avatars · JSON/error helpers
  platform/            clock, atomic file writes, bounded worker pool
  apitest/             boots the full API in-process for tests
```

### Development

```bash
make test     # unit + HTTP tests, no network needed
make race     # with the race detector
make cover    # total coverage
make check    # vet + staticcheck + govulncheck + race (what CI runs)
make build    # static binaries in bin/
```

The tests boot the whole API in-process with a fake stream probe. They cover the signature rules, session rotation and theft detection, fail-over and switch-back, migration, persistence, the `aurumctl` commands against the real API, and the hardening limits. All commands run at reduced priority (`nice`, `-p 6`).

---

<a id="ภาษาไทย"></a>

## ภาษาไทย

หลังบ้านของแอป **Aurum TV** บน Android TV ทำหน้าที่ส่งรายการช่อง และตรวจทุกลิงก์สตรีมด้วยการเล่นจริง ถ้าลิงก์ไหนใช้ไม่ได้จะสลับช่องนั้นไปใช้แหล่งสำรองที่ตรวจแล้วว่าใช้ได้ให้เอง ทุก request จากทีวีต้องเซ็นด้วย HMAC และผูกกับ session ที่หมุน token อยู่ตลอด

ใช้แทน backend เดิมที่เป็น Node.js ได้ทันที เพราะ endpoint, รูปแบบ JSON, รหัส error, header, รูปแบบลายเซ็น และไฟล์ข้อมูลเหมือนเดิมทั้งหมด volume ข้อมูลเดิมและแอปตัวปัจจุบันจึงใช้ต่อได้โดยไม่ต้องแก้อะไร

### ความสามารถ

- **ช่อง:** ดูรายการ เพิ่ม แก้ ลบ เพิ่มทีละหลายช่อง และเรียงลำดับ ฝั่ง client ได้รับการเปลี่ยนแปลงแบบสดผ่าน server-sent events
- **แหล่งสตรีมที่ซ่อมตัวเองได้:**
  - แต่ละช่องมีแหล่งหลักและแหล่งสำรอง
  - ตัวตรวจเบื้องหลังเล่นทุกลิงก์จริง: playlist → variant → segment วิดีโอจริง
  - สลับไปเฉพาะแหล่งที่ **ตรวจแล้วว่าใช้ได้** และสลับกลับเมื่อแหล่งหลักกลับมา
- **Resolver:** แหล่งสามารถหา URL ตอนใช้งานได้ เช่นลิงก์แบบเซ็นของ `ch3plus` ที่มีวันหมดอายุ เพิ่ม resolver ชนิดใหม่ได้ด้วยไฟล์เดียว
- **การยืนยันตัวแอป** (เปิดบังคับเป็นค่าเริ่มต้น):
  - เซ็น request ด้วย HMAC-SHA256 กันการยิงซ้ำ และปรับเวลาตามนาฬิกาเซิร์ฟเวอร์
  - session แบบหมุน refresh token พร้อมตรวจจับการนำ token เก่ามาใช้ซ้ำ
- **สิทธิ์แอดมิน:** ใช้ API key ของแอดมิน และใช้ admin JWT สำหรับ endpoint ของ log
- **การเก็บข้อมูลที่ทนทาน:**
  - เขียน JSON แบบ atomic (ไฟล์ชั่วคราว → fsync → rename) พร้อมไฟล์สำรอง
  - ย้าย schema ให้อัตโนมัติ
  - เก็บ log เป็นไฟล์รายวัน
  - ไฟล์ข้อมูลทุกไฟล์อ่านได้เฉพาะเจ้าของ (0600)
- **CLI สำหรับผู้ดูแล:** `aurumctl` ดูสุขภาพแหล่ง หาแหล่งสำรอง ซิงก์ลิงก์ กู้ช่อง smoke test และสร้างคีย์

### เริ่มใช้งาน

```bash
cp .env.example .env
go run ./cmd/aurumctl genkey   # พิมพ์ HMAC_KEYS / API_KEY สำหรับ .env และคีย์เซ็นของแอป
go run ./cmd/aurum-api         # หรือ: make run
```

เปิด `http://localhost:3000/` เพื่อดูรายการ endpoint ทั้งหมด และ `/api/health` เพื่อดูสถานะ

ต้องมี Go 1.27 ขึ้นไป ส่วน Docker ไม่บังคับ

### การตั้งค่า

ทุกค่าตั้งผ่าน environment variable มีคำอธิบายครบใน [`.env.example`](.env.example) ระบบอ่านไฟล์ `.env` ในเครื่องด้วย แต่ค่าที่ตั้งจาก shell, Docker หรือแพลตฟอร์มที่ deploy จะชนะเสมอ

| ตัวแปร | หน้าที่ |
|---|---|
| `APP_ENV` | ถ้าเป็น `production` จะไม่ยอมเริ่มทำงานถ้าไม่มี `HMAC_KEYS` |
| `PORT` | พอร์ต (3000) |
| `DATA_DIR` | โฟลเดอร์ข้อมูล — production **ต้องเป็น persistent volume** |
| `API_KEY` | คีย์แอดมิน คั่นด้วย comma ได้หลายคีย์ |
| `JWT_SECRET` | ใช้เซ็น token ของแอดมินและอุปกรณ์ ถ้าเว้นว่างระบบจะสุ่มให้แล้วเก็บไว้ใน `DATA_DIR` |
| `HMAC_KEYS` | คู่ `keyId:secret` ที่ใช้ร่วมกับแอป (**production ต้องตั้ง**) |
| `HMAC_MODE` | `enforce` (ค่าเริ่มต้น) · `log` (ใช้ตอนดีบักเท่านั้น) · `off` |
| `TRUST_PROXY` | ตั้งเป็น `true` เฉพาะตอนอยู่หลัง reverse proxy |
| `HEALTH_*` | ปรับตัวตรวจแหล่ง (รอบเวลา จำนวนที่ตรวจพร้อมกัน เกณฑ์) |

### การยืนยันตัว

| ผู้เรียก | สิ่งที่ใช้ยืนยัน | Endpoint |
|---|---|---|
| แอปทีวี (อ่าน) | ลายเซ็น HMAC + access token ของ session | `GET /api/channels`, `/api/channels/{id}`, `/api/channels/{id}/sources`, `/api/events` |
| แอปทีวี (เขียน) | ลายเซ็น HMAC + access token ของ session | `POST /api/channels/{id}/refresh`, `/sync`, `/report`, `POST /api/logs` |
| แอปทีวี (session) | ลายเซ็น HMAC | `POST /api/session`, `POST /api/session/refresh` |
| แอดมิน | header `X-API-Key` | เพิ่ม/แก้/ลบช่อง, `probe`, `GET /api/health/sources`, `POST /api/auth/token` |
| แอดมิน (log) | `Authorization: Bearer <admin JWT>` | `/api/logs*` |
| ทุกคน | — | `GET /api/health`, รูปโลโก้และ avatar |

**ลายเซ็น HMAC** ใช้ header `X-TV-Key-Id`, `X-TV-Timestamp`, `X-TV-Nonce`, `X-TV-Signature`

```
signature = base64url( HMAC-SHA256( secret,
  "v1\n{METHOD}\n{path+query}\n{unix timestamp}\n{nonce}\n{sha256hex(body)}" ) )
```

- **เวลาไม่ตรง:** ถ้า timestamp ห่างเกิน ±`HMAC_MAX_SKEW_SEC` จะได้ `401 clock_skew` พร้อม `serverTime` แล้วแอปจะเซ็นใหม่ตามเวลานั้น
- **ยิงซ้ำ:** nonce ถูกจำไว้หลังลายเซ็นผ่านแล้วเท่านั้น ถ้าใช้ nonce ซ้ำจะได้ `replayed`
- **Session:**
  - `POST /api/session` ให้ access token อายุสั้นกับ refresh token
  - `POST /api/session/refresh` หมุนให้ทั้งคู่ใหม่
  - refresh token ตัวก่อนหน้ายังใช้ได้อีก `REFRESH_GRACE_SEC` เผื่อ response หายระหว่างทาง
  - ถ้านำ refresh token เก่ามาใช้หลังพ้นช่วงนั้น session จะถูกยกเลิก (`refresh_reused`)
  - ระบบเก็บเฉพาะค่า hash (HMAC) ของ refresh token
- **ตอน deploy:** client ที่ไม่มีคีย์ (APK เก่า) จะได้ `401` ทันทีที่ backend นี้ขึ้น

### โมเดลความปลอดภัย

| ภัย | วิธีป้องกัน |
|---|---|
| client ที่ไม่เซ็นหรือ APK เก่า | ค่าเริ่มต้น `HMAC_MODE=enforce` และ production ไม่ยอมเริ่มถ้าไม่มีคีย์ |
| ยิง request ซ้ำ | ช่วงเวลาของ timestamp + แคช nonce (จำกัดขนาด และบันทึก nonce หลังลายเซ็นผ่านแล้วเท่านั้น) |
| refresh token ถูกขโมย | หมุนทุกครั้งที่ใช้ ถ้านำของเก่ามาใช้หลังช่วงผ่อนผันจะยกเลิก session |
| timing attack กับคีย์ | แปลงเป็น SHA-256 ก่อน แล้วเทียบแบบเวลาคงที่กับทุกคีย์ที่ตั้งไว้ |
| คีย์แอดมินรั่วระหว่างทาง | `aurumctl` ไม่ยอมใช้ `http://` (ยกเว้น localhost) และไม่รับคีย์ผ่าน query string |
| ปลอม `X-Forwarded-For` เพื่อเลี่ยง rate limit | ใช้ที่อยู่ที่ proxy ต่อท้าย (ตัวขวาสุด) และใช้เฉพาะเมื่อ `TRUST_PROXY=true` |
| body ใหญ่เกิน / client ช้า | จำกัดขนาด body ทั้งระบบและรายเส้นทาง มี timeout ตอนอ่าน/เขียน/idle/header และจำกัดขนาด header |
| เปิดการเชื่อมต่อสดมากเกินไป | จำกัดจำนวน SSE พร้อมกัน (`503 too_many_streams`) |
| log ของแอปทำดิสก์เต็ม | จำกัดความยาวแต่ละฟิลด์, `details` ไม่เกิน 8 KB, จำกัดขนาด batch และจำนวนรายการต่อวัน |
| ใช้วันที่ของ log แทรก path | ตรวจรูปแบบ `YYYY-MM-DD` อย่างเข้มก่อนแตะไฟล์ |
| ยัดลิงก์ผ่าน `sync` | รับเฉพาะ HTTPS `.m3u8` จาก `SYNC_ALLOWED_HOSTS` (รวม subdomain) และเฉพาะช่องที่มี resolver |
| ความลับรั่วผ่าน error ตอนตรวจลิงก์ | ปัญหาเครือข่ายรายงานเป็นรหัสสั้น (`network:ECONNREFUSED`) ไม่ใส่ URL ที่มีลายเซ็น |
| เผยข้อมูลภายใน | panic ตอบเป็น `500` ทั่วไป ส่วน error ของ validation บอกแค่ฟิลด์ ไม่เผยข้อมูลภายใน |
| การโจมตีฝั่งเบราว์เซอร์ | CSP เข้ม (`default-src 'none'`), `nosniff`, กันการฝังใน frame, ไม่ส่ง referrer, HSTS |
| การเข้าถึงไฟล์ในเครื่อง | ไฟล์ข้อมูล 0600 โฟลเดอร์ 0700 และ container รันด้วยผู้ใช้ที่ไม่ใช่ root โดยไม่มี capability |

CI รัน `go vet`, `staticcheck`, `govulncheck` และเทสพร้อม race detector ทุกครั้งที่ push

### แหล่งสตรีมที่ซ่อมตัวเองได้

```json
{
  "name": "One 31",
  "number": 31,
  "sources": [
    { "id": "mirror-3", "label": "mirror 576p", "url": "https://…/playlist.m3u8", "resolver": "static", "priority": 0 },
    { "id": "mirror-2", "label": "mirror 576p", "url": "https://…/playlist.m3u8", "resolver": "static", "priority": 1 }
  ],
  "activeSourceId": "mirror-3",
  "url": "<URL ของแหล่งที่กำลังใช้>"
}
```

1. **ตรวจ** ตัวตรวจทำงานทุก `HEALTH_INTERVAL_MS`:
   - แหล่งที่กำลังใช้ตรวจทุกรอบ
   - แหล่งสำรองตรวจทุก `HEALTH_BACKUP_EVERY_CYCLES` รอบ
   - แหล่งที่ล่มจะเว้นระยะการตรวจห่างขึ้นแบบทวีคูณ
   - ตรวจพร้อมกันไม่เกิน `HEALTH_CONCURRENCY` ตัว
2. **ถือว่าล่ม** เมื่อพลาดติดกัน `HEALTH_FAIL_THRESHOLD` ครั้ง และก่อนสลับทุกครั้งจะตรวจยืนยันซ้ำทันที
3. **สลับ** ไปแหล่งสำรองตัวแรกที่ **ตรวจแล้วว่าใช้ได้** ถ้าไม่มีตัวไหนผ่าน จะใช้แหล่งเดิมต่อ
4. **สลับกลับ** ไปแหล่งหลักเมื่อผ่านติดกัน `HEALTH_RECOVER_THRESHOLD` ครั้ง
5. **แอปแจ้งปัญหา** (`POST /api/channels/{id}/report`) แค่ทำให้การตรวจจริงเกิดเร็วขึ้น และจำกัดความถี่ต่อช่อง
6. **กระจายไปทุกเครื่อง** การสลับจะเปลี่ยน `url` และ `updatedAt` ของช่อง ทีวีทุกเครื่องจึงได้ลิงก์ใหม่ ส่วนการตรวจตามรอบปกติไม่แตะ `channels.json` เลย (ผลตรวจเก็บใน `source-health.json`)

เพิ่มแหล่งสำรองได้ด้วย `PATCH /api/channels/{id}` ส่ง `{ "sources": [...] }` หรือรัน `aurumctl discover-sources --apply`

### รายการ API

| Method และ path | รายละเอียด |
|---|---|
| `GET /api/health` | สถานะ + `serverTime` |
| `GET /api/channels` | รายการช่อง (`?q=`, `?group=`, `?enabled=`) |
| `GET /api/channels/{id}` | ช่องเดียว (`?refresh=true` ต่ออายุแหล่งแบบ resolver) |
| `GET /api/channels/{id}/sources` | แหล่งของช่องพร้อมสถานะ |
| `POST /api/channels/{id}/report` | แอปแจ้งว่าเล่นไม่ได้ |
| `POST /api/channels/{id}/refresh` | ต่ออายุแหล่งแบบ resolver ทันที |
| `POST /api/channels/{id}/sync` | แอปส่งลิงก์ resolver ตัวใหม่ขึ้นมา (เฉพาะโฮสต์ที่อนุญาต) |
| `POST /api/channels/{id}/probe` | ตรวจทุกแหล่งทันที (แอดมิน) |
| `GET /api/health/sources` | ภาพรวมสถานะทุกช่อง (แอดมิน) |
| `POST /api/channels` · `PUT/PATCH/DELETE /api/channels/{id}` | จัดการช่อง (แอดมิน) |
| `POST /api/channels/bulk` · `POST /api/channels/reorder` | เพิ่มหลายช่อง · เรียงลำดับ (แอดมิน) |
| `GET /api/events` | server-sent events เมื่อช่องเปลี่ยน |
| `POST /api/session` · `POST /api/session/refresh` | session ของอุปกรณ์ |
| `POST /api/auth/token` | ขอ admin JWT ด้วย API key |
| `POST /api/logs` · `GET /api/logs` · `GET /api/logs/dates` · `GET /api/logs/config` · `POST /api/logs/toggle` · `DELETE /api/logs/{date}` | log ของแอป (ไฟล์ JSON วันละไฟล์) |
| `GET /api/channels/{id}/logo` · `GET /api/avatar?name=` | โลโก้ หรือ avatar ที่สร้างให้ |

error ทุกตัวมีรูปแบบ `{ "error": "<ข้อความ>", "code": "<รหัส>" }` ถ้าเป็น error จาก validation จะมี `issues: [{ path, message }]` เพิ่มมาด้วย

### CLI สำหรับผู้ดูแล — `aurumctl`

`aurumctl` รันจากเครื่องแอดมินไปยังเซิร์ฟเวอร์ที่ deploy แล้ว อ่านค่าจากไฟล์ `ops.env` ในโฟลเดอร์ที่รัน (คัดลอกจาก [`ops.env.example`](ops.env.example)) หรือจาก shell คำสั่งฝั่งแอดมินใช้คีย์แอดมิน จึงใช้ได้แม้ตั้ง `HMAC_MODE=enforce`

| คำสั่ง | หน้าที่ |
|---|---|
| `aurumctl sources-status [--probe <id>]` | สุขภาพแหล่งของทุกช่อง |
| `aurumctl discover-sources [--apply] [--channel <id>] [--rules <file>]` | หาแหล่งสำรองจากกฎที่แก้ได้ รายงานหรือเพิ่มเฉพาะตัวที่เล่นได้จริงตลอดทาง และไม่ลบหรือเรียงแหล่งเดิมใหม่ |
| `aurumctl sync-ch3 [--dry-run] [--force] [--loop <นาที>]` | ดึงลิงก์ใหม่ของแหล่ง resolver `ch3plus` จากเครือข่ายที่เข้าเว็บต้นทางได้ แล้วส่งขึ้น backend ด้วยคีย์แอดมิน |
| `aurumctl restore-channels [--dry-run]` | เติมช่องตั้งต้นที่หายไป (รันซ้ำได้ ไม่สร้างช่องซ้ำ) |
| `aurumctl smoke` | ทดสอบครบวงจรกับเซิร์ฟเวอร์ที่รันอยู่ เซ็นแบบเดียวกับแอปทีวี (`SMOKE_URL`, `SMOKE_KEY_ID`, `SMOKE_SECRET`) |
| `aurumctl genkey [--key-id <id>]` | พิมพ์คีย์ HMAC และคีย์แอดมินชุดใหม่ |

exit code: `0` สำเร็จ · `1` ล้มเหลว · `2` ตั้งค่าไม่ครบหรือไม่ถูกต้อง ตัวอย่าง cron:

```
0 */2 * * * cd <โฟลเดอร์ backend> && ./bin/aurumctl sync-ch3 >> /tmp/aurum-sync-ch3.log 2>&1
```

### ข้อมูลและการ deploy

- **การเก็บข้อมูล** ทุกอย่างอยู่ใน `DATA_DIR`: `channels.json` (+ `channels.bak.json`), `sessions.json`, `source-health.json`, `logs/` และ `.secret-jwt_secret`
- **Persistent volume** ต้องให้ `DATA_DIR` ชี้ไปที่ persistent volume ไม่อย่างนั้นข้อมูลจะหายตอน redeploy
- **Docker** (`Dockerfile`, `docker-compose.yml`):
  - binary แบบ static รันด้วยผู้ใช้ `app` ที่ไม่ใช่ root (uid 1000 เท่ากับรุ่นก่อน volume เดิมจึงใช้ต่อได้)
  - ไฟล์ compose ผูก volume `tv-channel-data` ไว้ที่ `/data` ตั้ง root filesystem เป็นอ่านอย่างเดียว ตัด capability ทั้งหมด และเปิด `no-new-privileges`
  - healthcheck ใช้ `aurum-api healthcheck`
- **Coolify** deploy แบบ Docker Compose แล้วตั้ง `API_KEY` และ `HMAC_KEYS` ใน environment
- **การย้ายข้อมูล** ไฟล์ `channels.json` รุ่นเก่า (schema v1) จะถูกย้ายให้อัตโนมัติตอนเริ่ม และเก็บไฟล์เดิมไว้เป็นไฟล์สำรอง
- **ลำดับการปล่อยคู่กับแอป:**
  1. ตั้ง `HMAC_KEYS`, `API_KEY` และ `JWT_SECRET` (ถ้าต้องการ) บนเซิร์ฟเวอร์
  2. build แอปด้วยคีย์ HMAC ตัวเดียวกัน
  3. deploy backend
  4. ติดตั้งแอปตัวใหม่
- **การปิดเครื่อง** เมื่อได้ `SIGTERM` จะหยุดรับ request ใหม่ ทำ request ที่ค้างให้เสร็จ แล้วเขียน session, ผลตรวจ และ log ลงดิสก์ให้ครบ

### โครงสร้างโปรเจกต์

```
cmd/
  aurum-api/           จุดเริ่มเซิร์ฟเวอร์ (+ คำสั่งย่อย healthcheck)
  aurumctl/            CLI สำหรับผู้ดูแล
internal/
  config/              environment → Config แบบมี type · ตัวอ่าน .env · JWT secret ที่สร้างให้
  api/                 เส้นทาง, handler แยกตามหมวด, ตรวจ request, จัดรูป response
  auth/                API key, JWT · hmacsig/ (ลายเซ็น, แคช nonce) · appauth/ (ตัวกั้นเส้นทาง)
  session/             session แบบ refresh token (หมุน, ช่วงผ่อนผัน, ตรวจการใช้ซ้ำ) + ที่เก็บ
  channel/             model, การ normalize, กฎการ merge, ย้าย v1→v2, repository, seed
  source/              ระบบซ่อมตัวเอง: สุขภาพ, นโยบายเลือกแหล่ง, ตัวตรวจ, ตัวต่ออายุ · resolver/
  hls/                 ตัวอ่าน playlist HLS และการตรวจเล่นจริง
  stream/              ดึงลิงก์ ch3plus และอ่านวันหมดอายุ · นโยบาย URL ของ sync
  logs/                ไฟล์ log JSON รายวัน
  middleware/          recover, access log, security header, CORS, rate limit, body limit
  ops/                 คำสั่งของ aurumctl (+ กฎหาแหล่งสำรองในตัว)
  avatar/ · httpx/     avatar ที่สร้างให้ · ตัวช่วย JSON/error
  platform/            นาฬิกา, การเขียนไฟล์แบบ atomic, worker pool แบบจำกัดจำนวน
  apitest/             บูต API ทั้งระบบในโปรเซสเดียวสำหรับเทส
```

### การพัฒนา

```bash
make test     # เทส unit + HTTP ไม่ต้องใช้เน็ต
make race     # เทสพร้อม race detector
make cover    # coverage รวม
make check    # vet + staticcheck + govulncheck + race (ชุดเดียวกับ CI)
make build    # binary แบบ static ใน bin/
```

เทสบูต API ทั้งระบบในโปรเซสเดียวโดยใช้ตัวตรวจลิงก์จำลอง ครอบคลุมกฎลายเซ็น การหมุน session และการตรวจจับ token ที่ถูกขโมย การสลับแหล่งและสลับกลับ การย้ายข้อมูล การเก็บลงดิสก์ คำสั่ง `aurumctl` ที่รันกับ API จริง และขีดจำกัดด้านความปลอดภัย ทุกคำสั่งรันด้วยลำดับความสำคัญต่ำ (`nice`, `-p 6`) เพื่อไม่ให้เครื่องอืด
