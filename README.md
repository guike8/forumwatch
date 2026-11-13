# ForumWatch

First-party defensive forum monitoring in Go: collect from selected public or onion pages, persist new/changed entries, publish private RSS, and deliver updates over server-sent events (SSE) immediately after a successful collection.

**Prototype, not a deployed intelligence service.** No live forums or credentials are bundled. Current adapters are generic RSS/Atom and static HTML links; they are not validated against particular underground forums. See [research and collection priorities](docs/RESEARCH.md).

## Why SSE rather than just RSS?

RSS readers introduce another polling interval. An SSE subscriber stays connected to `/events` and receives each detected item after it has been saved. RSS remains available for interoperability.

There is still a collection delay: source publication → next successful fetch → parsing/persistence → SSE. With a healthy source, the scheduled polling component is up to approximately the configured interval plus 20% positive jitter, **excluding** network/queue time, backoff, downtime, and any upstream feed delay. Go does not make Tor or a slow forum instantaneous. No end-to-end latency benchmark has been established.

## Quick start

Requires the Go version in `go.mod` or newer. Tor is required separately only for sources configured with `transport: tor`.

```sh
cp config.example.json config.json
export FORUMWATCH_TOKEN="$(openssl rand -hex 32)"
go build -o bin/forumwatch ./cmd/forumwatch
./bin/forumwatch -config config.json
```

The empty example configuration intentionally collects nothing. Add sources only after verifying their addresses, access permissions, and allowed request rates. Paths are relative to the working directory. Keep one process per state file.

### Source examples (placeholders, not real forum addresses)

Prefer a forum's native RSS/Atom feed where available:

```json
{
  "id": "approved-public-forum",
  "url": "https://forum.example.invalid/latest.rss",
  "transport": "direct",
  "format": "feed",
  "interval_seconds": 60
}
```

For static HTML indexes, specify a regex matching only thread/post URLs:

```json
{
  "id": "approved-onion-forum",
  "url": "http://verified-address.onion/latest",
  "transport": "tor",
  "format": "html",
  "include_url": "/(?:threads|posts)/",
  "interval_seconds": 60
}
```

Replace the placeholder address with a verified v3 onion address. The program does not validate onion identity, discover mirrors, launch Tor, or bypass access controls. For regular domains, select `tor` explicitly if you want the requests routed through Tor too. The default SOCKS endpoint is `127.0.0.1:9050`.

### Live stream

```sh
curl -N -H "Authorization: Bearer $FORUMWATCH_TOKEN" \
  http://127.0.0.1:8080/events
```

Events are `ready` and `item`, with JSON data. Subscribe first, then fetch `/api/items` on every connection/reconnection, merging by `id` and comparing `hash`. The stream is **not** a durable event log and has no Last-Event-ID replay. A slow subscriber is disconnected rather than blocking collection. The snapshot contains the latest 200 items, not all intermediate revisions.

```sh
curl -H "Authorization: Bearer $FORUMWATCH_TOKEN" \
  http://127.0.0.1:8080/api/items
```

### RSS and health

- `/feed.xml`: latest 200 entries, with revision-specific GUIDs.
- `/api/status`: last attempt/success, fetch duration, next check, and failures per source.
- `/api/items`: latest 200 items ordered by observation time.

All endpoints require a Bearer token or HTTP Basic authentication (any username; token as password). For RSS readers, configure Basic authentication in the reader rather than putting credentials in the URL. The reader still controls how often it refreshes. Never submit a private feed to a hosted reader unless you intend to disclose its contents to that service.

Bind to loopback by default. Use a trusted HTTPS reverse proxy and disable proxy buffering for SSE if exposing it remotely. Do not send the token over plaintext public HTTP. Do not log authentication headers.

## Collection semantics

- First successful fetch establishes a **baseline**; existing entries are not mislabeled as new.
- RSS/Atom identities use entry IDs/GUIDs, falling back to URLs. Title, summary, URL, published/updated metadata changes trigger an update.
- Static HTML tracks matching same-host links and their titles. **It does not fetch thread bodies, discover unchanged-URL replies, or establish author/post timestamps.** A forum-specific adapter is needed for reliable reply tracking.
- RSS/Atom support covers common RSS 2.0 and Atom fields, not every namespace, RSS 1.0/RDF, or XHTML content variant.
- ETag/Last-Modified validators are committed only after parsing and persistence succeed.
- HTML with no matching links is a collection error, not a silent "no new posts" result. This helps surface login pages, challenges, and layout changes, but is not a complete challenge detector.
- Source claims, including "zero-day" advertisements, remain **unverified**. Source publication timestamps are untrusted; `observed_at` records local detection time.

## Network and storage safeguards

- Explicit direct/Tor routing per source. Tor passes destination names through SOCKS and never falls back to direct requests. Direct sources reject redirects to onion hosts.
- Direct mode deliberately ignores environment proxy settings. If you need Tor, select it in source configuration.
- Normal TLS certificate validation; no automatic attachment downloads or script execution.
- Up to four concurrent fetches globally and one in-flight fetch per configured source hostname. Redirect aliases are not a shared host-rate limiter.
- Per-source intervals of 10 seconds to 24 hours, positive jitter, exponential error backoff, and `Retry-After` handling on unsuccessful HTTP responses. These guardrails do not replace each forum's access/rate policies.
- 45-second HTTP timeout, bounded redirects, and a 2 MiB decoded response limit.
- Atomic JSON state writes with mode 0600; state directory is created with mode 0700. Keep data and credentials off GitHub. Storage is not encrypted.

## Current limitations / next work

- No preconfigured or live-verified forum adapters, authenticated source sessions, JavaScript rendering, CAPTCHA handling, Telegram ingestion, or webhook delivery.
- No pagination/catch-up: posts falling out of a watched page/feed between polls can be missed.
- State is single-process JSON, grows without retention, and is rewritten per successful changed/checkpointed fetch. Migrate to SQLite and add retention/indexes before large-scale use.
- Changing a source URL or meaning while keeping its ID retains the previous checkpoint; use a new ID or deliberately migrate state.
- No full append-only history or guaranteed event delivery during disconnections. Persisted latest-state recovery is bounded by the 200-item API/feed window.
- No measured current activity ranking for underground sources; research reports are leads, not endpoint availability checks.

## Development

```sh
go vet ./...
go test -race ./...
go build ./cmd/forumwatch
```

Tests use local HTTP and fake SOCKS servers, not real forums. They cover parsing, deduplication/restart, failed-write rollback, authentication, SSE, response limits, conditional requests, Tor failure behavior, and proxy-side onion-name handling.

Use only for authorized defensive collection; respect access controls, applicable law, and source rules. Do not automatically retrieve stolen datasets, purchase access, or execute advertised exploits.
