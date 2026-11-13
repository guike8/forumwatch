# Forum discovery and catch-up

## Source modes

- `format: xenforo-index`: start on a public forum/category listing. Supported `.structItem-title`, `.node-title` and `.node-extra-title` links discover threads/categories, not links embedded in post bodies. An index must have a recognized XenForo listing container; challenges/login/layout mismatches fail explicitly.
- `format: xenforo`: start on a known thread page. Only that thread and subsequent pages are followed. Starting at page 2 or a reply permalink does not automatically retrieve pages before it.

Both modes work with existing HTTP or Tor-only browser transports. They support friendly `/threads/slug.123/`, `/threads/123/` and `/forums/.../` paths, path `page-N` pagination and `?page=N`. XenForo query-routed `index.php?threads/...` layouts are not supported. Redirects/discovered URLs stay on the configured source origin, except the existing default-port HTTP→HTTPS upgrade allowance. Credentials, foreign hosts, downgrade/port changes and non-forward/cross-thread pagination are not followed. Session/tracking parameters are discarded from discovered links.

## MyBB source modes

- `format: mybb-index`: a compatible DarkForums-style public category/index; discovery is confined to recognized listing subjects and forum-title containers.
- `format: mybb`: one known public thread and its subsequent pages.

These support the observed case-sensitive friendly `/Thread-...` and `/Forum-...` paths and `?page=N`. Query-routed `showthread.php?tid=...` / `forumdisplay.php?fid=...`, alternate rewrite schemes and arbitrary MyBB themes are not supported. Session/action links, adjacent-thread `nextoldest`/`nextnewest` links and post-body links are not pagination. Only same-container, forward `pagination_next` navigation is followed.

Posts require a matching `post_N` / `pid_N` body, recognized author markup and source-provided absolute numeric time in a matching `quick_quote` call. That call is parsed as inert data, never executed. Its Unix timestamp is normalized to UTC; relative/unzoned display dates are not guessed. Missing or conflicting required metadata fails the page instead of silently counting it as collected. Stable per-post links include the source `pid` query and fragment; fetch/discovery URLs do not carry arbitrary queries. Source claims and timestamps remain unverified.

MyBB uses the same durable scheduler, baseline semantics, origin/redirect restrictions, page/thread bounds and partial/restricted access reporting described below. Start at a specific `/Forum-...` category: the observed DarkForums root exceeds the 64-forum discovery bound and is explicitly rejected, not silently truncated. Inline subforum descriptions and last-post widgets are not discovery sources. The HTTP transport works for public server-rendered text without executing forum scripts; challenges remain errors, not a reason to loosen routing or sign in automatically.

## Progress, baselines and publication

Each parsed page's posts and updated frontier are written atomically before its item events are published. Alerts do not wait for the remaining crawl pages or summary work. Failed fetches/parses/writes do not complete the failed task. Page-local failures retain the task with a durable retry deadline and rotate scheduling so healthy watches keep working; `failed_pages` and a partial-coverage error remain visible. Failed writes do not advance either progress or retry scheduling. HTTP 429 or a Retry-After response stops the whole poll and uses source-wide backoff; rate limits are never bypassed. No credentials or alternate identities are tried.

The frontier, per-thread current tail, pending tasks and round-robin cursor survive restart in the source checkpoint. The configured seed URL and format are bound to that checkpoint: changing either requires a new source ID or an explicit migration, not silent reuse of the old crawl scope. The initial traversal's backfill is labeled `baseline`, even if it spans many polls/restarts. Later-discovered threads and forward replies are `new`; previously stored changed posts are `updated`. Enabling crawl on an old single-page checkpoint establishes a baseline for previously unseen backfill, rather than flooding historical alerts.

After reaching a thread's last page, monitoring revisits that tail. A new next-page link creates durable catch-up work, so replies spanning multiple pages are retrieved over subsequent budgets. Historical pages are not continually rescanned for old-post edits. Source-provided stable post permalinks are preferred when present.

Initial backfill is deliberately conservative: replies arriving before that thread's initial traversal reaches its tail can still be classified as baseline. A newly discovered thread can also contain old posts; `new` means newly observed by this monitor after its initial baseline, not verified publication time.

## Bounded scheduling

Per compatible XenForo/MyBB source:

| Field | Default | Allowed |
| --- | --- | --- |
| `max_pages_per_poll` | 4 | 1–32 |
| `max_threads` | 256 | 1–4096 |
| `request_gap_seconds` | 2 | 1–60 |

These are request budgets, not concurrency controls. Requests are sequential, with the gap between pages; the existing per-host and four-global-fetch gates still apply. After a poll the normal source interval/jitter/backoff applies. Configured rates must comply with the source's rules.

The scheduler alternates available live watches and pending discovery/catch-up work. Category indices and ready thread tails rotate fairly, including across one-page poll budgets/restarts. Unchanged index refreshes do not recursively restart completed history. New thread IDs on a listing trigger deeper pagination until the known boundary; previously unvisited index pages are traversed for initial catch-up. This depends on the source's listing order and is not a guarantee of recovering deleted/reordered content. A source has at most 64 category/index watches, 4096 remembered index-page URLs, and `2*max_threads+128` pending tasks. Reaching a bound fails explicitly; narrow the seed category or increase `max_threads` rather than assuming complete coverage. Threads are not silently evicted.

`/api/status` includes `pending_pages`, `tracked_threads` and `failed_pages`; source-check logs include page counts and backlog. Partial coverage does not advance `last_success`. Successful pages before a later failure remain committed and their alerts are already delivered. Backlog, number of watches, Tor availability and page rendering all affect actual detection latency. There is no claim that every thread is checked on every source interval, nor any HFT/instantaneous latency guarantee.

## Guest-only hidden links

The observed Rehub guest placeholder `Link hidden, please Sign in or Sign up` is not post content. The parser removes that specific UI phrase, records `hidden_links`, and labels the item `access: partial` when public text remains or `access: restricted` when no readable body remains. Restricted entries retain attribution/permalinks as access observations, not collected bodies, and receive no local summary. Both initial alerts and extracts disclose partial access. General discussions mentioning sign-in are not treated as restrictions. Other restriction messages/layouts are not automatically recognized; an unlabeled post is not proof of full access.

`/api/status` and crawl logs separate `public_text_posts`, `partial_posts`, and `restricted_posts` across saved latest posts. These are observed-text counts, not completeness guarantees; an HTTP/page check can succeed while individual links remain restricted. On opening legacy state, recognized placeholder bodies are corrected atomically without changing observation times, baseline labels, or crawl progress; hashes are updated and stale extracts ignored so the correction is not replayed as newly collected content. No login or authentication bypass is attempted.

## Not provided

No private-area access, account registration, image-only post interpretation, deleted-post recovery, arbitrary forum adaptation or complete edit history. A working public onion discussion does not establish access to private exploit-market areas. Source text and publication timestamps remain unverified.
