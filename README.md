# Nightwire

Native macOS monitoring app, backed by the ForumWatch Go collector. The CLI and Go module retain their existing names.

First-party defensive forum monitoring in Go: collect from selected public or onion pages, persist new/changed entries, and print immediate terminal alerts with original-source links. SSE and private RSS are optional outputs, not the primary product.

**Prototype, not a deployed intelligence service.** No active target configuration or credentials are bundled. The native app includes an optional source catalog with evidence dates and explicit Ready/Needs verification versus research-only statuses (login-required, unsupported, challenged, unavailable); nothing is added or monitored automatically. Multiple category presets from one forum are scopes, not distinct working communities. Adapters cover RSS/Atom, static HTML links, and compatible XenForo/MyBB forum-index discovery, thread posts and forward pagination. The optional persistent Tor browser transport has retrieved a public Rehub onion reply; this is not coverage of its private areas or exploit trading. See [research and collection priorities](docs/RESEARCH.md).

## Why SSE rather than just RSS?

RSS readers introduce another polling interval. An SSE subscriber stays connected to `/events` and receives each detected item after it has been saved. RSS remains available for interoperability.

There is still a collection delay: source publication → next successful fetch → parsing/persistence → SSE. With a healthy source, the scheduled polling component is up to approximately the configured interval plus 20% positive jitter, **excluding** network/queue time, backoff, downtime, and any upstream feed delay. Go does not make Tor or a slow forum instantaneous. No end-to-end latency benchmark has been established.

## macOS app

See [the native Nightwire client](apps/macos/README.md) for the local `.app` build and usage. It owns a separate collector and private Application Support state, not existing terminal watchers. Tor must already be running; the initial client uses HTTP collection through Tor, not the optional Chromium worker. Choose an optional source-catalog preset in Add Source or enter a custom source; neither is added or started automatically.

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

### Terminal alerts (no HTTP server or API token needed)

```sh
./bin/forumwatch -config config.json -console-only -require-tor
```

All sources must explicitly use `transport: tor` with `-require-tor`, including ordinary public-web sources. Each detected new/updated entry prints its observation time, source, title, available excerpt, and original URL. Source content is stripped of terminal/formatting controls. Initial alerts contain the source excerpt; a separate `[SUMMARY]` follows asynchronously with a bounded **local/extractive, not AI** summary. No model API is called. Use `-summaries=false` to disable enrichment; see [summary behavior and recovery](docs/SUMMARIES.md).

The first successful fetch establishes a baseline and does not alert on old posts. Use `-show-baseline` to inspect those initial entries, clearly marked `BASELINE`. Saved items are not replayed as new on restart. Console-only mode refuses an empty source list rather than appearing to monitor nothing.

Terminal output runs separately from fetchers. If it falls behind, it reconnects and reconciles **latest saved revisions**, reporting that recovery; intermediate edits are not a durable event history. Broken terminal output stops the process. Default HTTP-server mode also prints terminal alerts; use `-terminal=false` to disable them.

Source checks report success/failure separately from entry alerts. A successful check means that source was fetched and parsed; it does not establish that every post/reply is covered.

### Persistent Tor browser and XenForo posts

Optional on macOS/Linux, tested with Python 3.14 and pinned Scrapling 0.5.1. Keep the virtual environment, browser cache, profiles, and collected content out of Git:

```sh
uv venv .venv-browser
uv pip install --python .venv-browser/bin/python -r tools/requirements-browser.txt
export PLAYWRIGHT_BROWSERS_PATH="$PWD/data/browser-cache"
.venv-browser/bin/python -I -m patchright install chromium
```

Use absolute paths for both the Python executable and worker script. Use `xenforo-index` on a verified, authorized forum listing to discover threads, or `xenforo` on a known thread to monitor its posts and subsequent pages. Example discovery configuration (replace paths and the placeholder):

```json
{
  "state_path": "data/onion-state.json",
  "tor_proxy": "127.0.0.1:9050",
  "browser_command": [
    "/absolute/path/to/forumwatch/.venv-browser/bin/python", "-I",
    "/absolute/path/to/forumwatch/tools/browser_worker.py"
  ],
  "browser_profile_dir": "data/browser-profiles",
  "sources": [{
    "id": "approved-onion-forum",
    "url": "http://verified-address.onion/forums/news.1/",
    "transport": "tor",
    "fetcher": "browser",
    "format": "xenforo-index",
    "interval_seconds": 30,
    "max_pages_per_poll": 4,
    "max_threads": 256,
    "request_gap_seconds": 2
  }]
}
```

Run with `-console-only -require-tor`; add `-show-baseline` only to inspect existing posts. A browser source keeps a dedicated Chromium process/profile alive. After DOM-ready navigation and any bounded automatic browser check, subsequent polls use same-source `fetch` **inside that Tor-proxied browser**, avoiding browser restarts and full page/asset reloads. A recurring challenge triggers one bounded rendering attempt. Source failures retain the scheduler's backoff; there is no direct fallback, IP rotation, credential guessing, automatic signup, or purchase.

The worker blocks cross-origin requests and attachment paths, preserves TLS validation and the Chromium sandbox, grants no geolocation/notification permissions, blocks service workers and non-proxied WebRTC UDP, and uses restricted DNS rules. These are safeguards, **not a Tor Browser-equivalent anonymity guarantee**. Strict first-party-only requests may break sources requiring third-party challenges/assets; those fail rather than silently relaxing routing. Browser profiles hold session secrets in plaintext-equivalent local storage: protect them and do not share them. No personal browser profiles or cookies are imported.

`format: xenforo` extracts stable post IDs, author, source-claimed publication timestamp, normalized full text and per-post links. `xenforo-index` discovers thread titles and forum-category links from supported listing layouts, then uses the same post parser. Both follow forward pagination within per-poll request budgets, persist the frontier with every page, and revisit thread tails to catch replies/page rollover. Live refreshes alternate with pending backfill. Initial backfill remains baseline across polls/restarts; later-discovered threads/replies are new. Full `body` and `post_id` are persisted and exposed through item JSON; edits beyond the excerpt still change the revision hash. See [crawl scheduling, bounds and limitations](docs/CRAWLING.md).

This supports **text posts on compatible XenForo layouts**, not arbitrary forums. Image-only posts, private-area access, authenticated-session setup and old-page edit rescans are not implemented. Ordinary login links are not failures; actual login forms, remaining challenges and unsupported layouts fail explicitly.

The 2 MiB limit bounds returned/parsed text and in-browser fetch streams. Initial browser navigation and its resources are not a complete network-download budget. Browser sources do not yet use application-managed ETag/Last-Modified validators. The worker emits private JSON lines to Go; arbitrary worker errors and stderr are not forwarded to terminal output.

### MyBB / DarkForums-style public pages

Use `format: mybb-index` on a compatible public category, or `format: mybb` on one known thread. The HTTP fetcher can collect server-rendered text through Tor without Chromium:

```json
{
  "id": "approved-mybb-onion",
  "url": "http://verified-address.onion/Forum-Access-Market",
  "transport": "tor",
  "fetcher": "http",
  "format": "mybb-index",
  "interval_seconds": 30,
  "max_pages_per_poll": 3,
  "max_threads": 256,
  "request_gap_seconds": 3
}
```

This adapter targets observed friendly `Thread-...` / `Forum-...` paths with `?page=N`, not every MyBB installation. It requires recognized post/author markup and an absolute source timestamp; it parses the matching post's inert `quick_quote` numeric time without executing JavaScript, rather than guessing a timezone from relative display dates. It shares the durable crawl scheduler, page budgets, origin restrictions, baseline/reply semantics and asynchronous local extracts. Unsupported routing/layouts fail explicitly. Known guest-link placeholders are excluded from bodies and counted as partial/restricted access, never proof of full collection. See [adapter scope](docs/CRAWLING.md).

### Alert filters (collection is retained)

Each source accepts an optional `alert_filter`. These Go regular expressions control terminal/SSE delivery, API/feed snapshots and asynchronous extracts—not which pages are fetched or saved. Exclusions win. Match text is the whitespace-normalized title plus readable body (or source excerpt for feeds); derived extracts do not change matching. Patterns are case-sensitive unless prefixed with `(?i)`.

```json
"alert_filter": {
  "include": "",
  "exclude": "",
  "exclude_title": "",
  "readable_only": true,
  "quiet_sales": true
}
```

`quiet_sales` is an optional, default-off title-only heuristic for combos/combo lists and Gmail, Outlook, Hotmail, Yahoo, Netflix, or Spotify account advertisements. It is an additional exclusion: switching it on/off never overwrites custom regexes or changes `readable_only`. It can hide relevant discussion titles too (for example, “Protecting Gmail accounts”); review results for your sources. It does not suppress body-only discussion of accounts. `include` requires a match if nonempty; `exclude` rejects a match anywhere in title/body; `exclude_title` checks only the title. `readable_only` hides restricted entries and entries without body/excerpt text, including link-only HTML discoveries. Empty settings preserve previous delivery. Invalid expressions fail startup.

Use `./bin/forumwatch -config config.json -check-config -require-tor` to validate rules with Go/RE2 without opening state, binding an API listener, requiring a token, or fetching sources. The native app runs this preflight before saving; invalid rules leave the previous configuration and running collector untouched.

Restart to apply policy changes. Saved posts/checkpoints/hashes are not deleted or rewritten, and older entries do not replay as new alerts. Snapshots filter **before** the 200-item limit; removed-source items remain stored but are not delivered. `/api/status` includes `matching_items` and `filtered_items`: saved latest-entry counts, not event/delivery totals. Inspect the private state file to audit excluded observations; there is no unfiltered API bypass.

### Optional live API stream

```sh
curl -N -H "Authorization: Bearer $FORUMWATCH_TOKEN" \
  http://127.0.0.1:8080/events
```

Events are `ready` and `item`, with JSON data. Subscribe first, then fetch `/api/items` on every connection/reconnection, merging by `id`, comparing observation times and `hash` for source revisions, and upserting same-hash `enrichment` updates. An item with `filtered: true` is a metadata-only removal: remove its ID from the client view when a previously matching revision becomes excluded. Reconnection snapshots are authoritative for the latest 200 matching items and replace older views, while events arriving during the snapshot must be merged in order. The stream is **not** a durable event log and has no Last-Event-ID replay. A slow subscriber is disconnected rather than blocking collection.

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

For a local app-owned backend, `listen: "127.0.0.1:0"` plus `-ready-file /absolute/private-run/ready.json` publishes the actual assigned address and PID after a successful bind. The immediate run directory must be private (0700); the readiness file is atomically created with mode 0600, never replaces an existing target, contains no token, and is removed on graceful exit. Readiness mode refuses non-loopback listeners. Use a unique run directory and validate the PID against the exact child you launched; keep the token in its environment, not URLs or readiness files.

## Collection semantics

- First successful fetch establishes a **baseline**; existing entries are not mislabeled as new.
- RSS/Atom identities use entry IDs/GUIDs, falling back to URLs. Title, summary, URL, published/updated metadata changes trigger an update.
- Static HTML tracks matching same-host links and their titles. **It does not fetch thread bodies, discover unchanged-URL replies, or establish author/post timestamps.** Use the compatible XenForo/MyBB adapters for text posts and bounded discovery/catch-up.
- RSS/Atom support covers common RSS 2.0 and Atom fields, not every namespace, RSS 1.0/RDF, or XHTML content variant.
- ETag/Last-Modified validators for single-page HTTP feed/HTML sources are committed only after parsing and persistence succeed. Multi-page forum crawls do not share validators between resources.
- HTML with no matching links is a collection error, not a silent "no new posts" result. This helps surface login pages, challenges, and layout changes, but is not a complete challenge detector.
- Source claims, including "zero-day" advertisements, remain **unverified**. Source publication timestamps are untrusted; `observed_at` records local detection time.

## Network and storage safeguards

- Explicit direct/Tor routing per source. Tor passes destination names through SOCKS and never falls back to direct requests. Direct sources reject redirects to onion hosts.
- Direct mode deliberately ignores environment proxy settings. If you need Tor, select it in source configuration.
- Normal TLS certificate validation; no automatic attachment downloads. HTTP fetchers do not execute scripts; opt-in browser sources execute source JavaScript in isolated profiles.
- Up to four concurrent fetches globally and one in-flight fetch per configured source hostname. Redirect aliases are not a shared host-rate limiter.
- Per-source intervals of 10 seconds to 24 hours, positive jitter, exponential error backoff, and `Retry-After` handling on unsuccessful HTTP responses. These guardrails do not replace each forum's access/rate policies.
- 45-second HTTP timeout, bounded redirects, and a 2 MiB decoded response limit.
- Atomic JSON state writes with mode 0600; state directory is created with mode 0700. Keep data and credentials off GitHub. Storage is not encrypted.

## Current limitations / next work

- No broad forum coverage, authenticated-session setup, interactive CAPTCHA solving, Telegram ingestion, webhook delivery, or AI summaries. Browser rendering does not confer membership access.
- XenForo/MyBB pagination catches forward replies, but deleted/hidden content, unsupported navigation and posts appearing/disappearing between successful checks can still be missed. Feed/static-HTML sources do not have pagination/catch-up.
- Thread/index queues and request budgets are bounded; large backlogs increase detection delay. Reaching configured thread/index limits is an explicit source failure, not silent complete coverage.
- State is single-process JSON, grows without retention, and is rewritten per successful changed/checkpointed fetch. Migrate to SQLite and add retention/indexes before large-scale use.
- Changing a source URL or meaning while keeping its ID retains the previous checkpoint for feed/static HTML sources. Forum crawl checkpoints reject seed URL/format changes explicitly. Use a new ID or deliberately migrate state.
- No full append-only history or guaranteed event delivery during disconnections. Persisted latest-state recovery is bounded by the 200-item API/feed window.
- No measured current activity ranking for underground sources; research reports are leads, not endpoint availability checks.

## Development

```sh
go vet ./...
go test -race ./...
go build ./cmd/forumwatch
python3 -I -m unittest discover -s tools -p 'test_*.py'
```

Tests use local HTTP, fake SOCKS servers and worker subprocesses, not real forums. Python unit tests use fake browser objects and require only the standard library. They cover parsing, deduplication/restart, failed-write rollback, authentication, SSE, response limits, conditional requests, Tor failure behavior, proxy-side onion-name handling, persistent-worker lifecycle, and post-body edits beyond the displayed excerpt.

Use only for authorized defensive collection; respect access controls, applicable law, and source rules. Do not automatically retrieve stolen datasets, purchase access, or execute advertised exploits.
