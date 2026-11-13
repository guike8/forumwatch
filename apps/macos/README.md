# Nightwire for macOS

Native SwiftUI client, macOS 14+. Build with installed Swift CLI tools and the Go version required by the repository's `go.mod`; no new third-party client dependencies or Xcode project.

From the repository root:

```sh
apps/macos/test.sh
apps/macos/build-app.sh
```

The build script creates and ad-hoc signs `apps/macos/build/Nightwire.app`, bundling the current Go backend in `Contents/MacOS/forumwatch`. Pass an optional absolute output directory to build a separate bundle without overwriting a running development app. It does **not** launch anything. Open the bundle yourself when ready. Swift Testing requires a Swift 6-era toolchain; `test.sh` handles the standalone Command Line Tools framework search paths.

## Use

- The Nightwire **line-art moth** in the macOS menu bar offers Show Nightwire, Start/Stop monitoring, and Quit.
- Closing the main window leaves the menu-bar app running; use **Quit Nightwire** (or `Cmd-Q`) to stop its owned collector and exit.
- Half-screen/narrow windows use two columns; **Choose source** in the toolbar replaces the sources sidebar below 1000 points. Wide windows restore the sidebar. The minimum window size is 600 × 500 points.
- File → Add Source (`Cmd-N`): choose a **Source catalog** preset or enter your own URL, format, poll interval, bounded crawl options, and alert filters. The empty feed also offers **Browse source catalog**.
- Monitoring → Start/Stop (`Cmd-R`): controls only this app's bundled backend child.
- **Filters** in the toolbar or Monitoring → Filters (`Shift-Cmd-F`): per-source quiet-sales/readable-only toggles and advanced regexes. Right-click a source → Filters opens that source directly.
- Select a source for poll errors, saved matching/filtered revision counts, and crawl backlog; select an item to read plain text.
- Copy source links into a separately operated Tor-aware browser. Nightwire never opens links in the default browser.

Nothing is monitored by default, and opening the app never starts monitoring automatically. Supported formats: `feed`, `html`, `xenforo`, `xenforo-index`, `mybb`, `mybb-index`. The HTTP fetcher and Tor transport are the only options. Tor must already expose a local SOCKS listener (default `127.0.0.1:9050`); Nightwire does not launch Tor or fall back to direct requests.

The bundled catalog is plain JSON at `Sources/Nightwire/Resources/source-catalog.json`, so another interface (including a future Telegram bot) can consume the same entries. Each record includes a `status`, `checked_at` evidence date, category, evidence, access limitations, and an optional collector `format`. Evidence dates describe completed checks, not live health or verified operator identities. The catalog is bundled, not downloaded or checked against remote sites at startup.

- **Ready**: a public listing and sampled post text passed the current HTTP parser through Tor. This is bounded sampling, not complete coverage.
- **Needs verification**: a supported format is identified but current collection still needs verification. Review the limitations before configuring it.
- **Login required**, **Unsupported**, **Challenge**, **Unavailable**: research-only entries. Their details and **Copy link** remain available, but they have no editable source draft and cannot be saved from the catalog. Unknown adapters have no format; they never fall back to a guessed parser. Unavailable means a bounded check failed, not that the site is permanently offline.

Only Ready/Needs verification records with a supported format can fill an Add Source draft. Review it and click **Save** to opt in. Already-configured URL/format presets are omitted from the picker. Every addition gets a fresh source ID and the existing bounded Tor-only defaults; presets do not enable filters. Nothing is automatically imported, enabled, signed into, or sent to Telegram. Browsing never starts monitoring, and links are only copied, not opened.

The catalog currently has **14 entries across nine researched forum communities**, not 14 working forums:

- **Five Ready DarkForums scopes** (`mybb-index`): Access Market, Web application vulnerabilities, Software vulnerabilities / exploitation, Malware, and Exploit & POCs. Each listing and one thread page passed current parser checks over Tor. This is one community, sampled within category scopes rather than its oversized root.
- **Nine research-only entries**: DarkForums Hacking Tools' sampled thread failed content parsing; BreachForums is login-required; Rehub's latest native HTTP check returned 403 despite older browser-backed public observations; T1erOne and Banished need adapters (Banished also requires approval); PwnForums and XSS.MX remained challenged; Spear and ZeroForums failed bounded availability checks.

Research-only entries do **not** increase working collection coverage. Familiar names and directory-listed onion addresses do not authenticate operator continuity.

Open **Filters**, choose a source, switch on **Quiet account/combo sales** and optionally **Readable posts only**, then click **Apply**. Changes to multiple selected sources are saved together; **Cancel** discards the draft. The toolbar filter icon is filled when any source has active rules. Filters are also available when adding/editing a source.

`quiet_sales` is a separate, default-off boolean. Its title-only heuristic covers combos and Gmail/Outlook/Hotmail/Yahoo/Netflix/Spotify account offers, not every kind of spam. It never overwrites the custom `include`, `exclude`, or `exclude_title` single-string regexes, and does not automatically enable `readable_only`. It can hide relevant discussion titles; body-only account mentions are not affected. Exclusions win. Advanced fields use Go/RE2 syntax, with `(?i)` for case-insensitive matching. The app checks the candidate configuration with the bundled Go executable **before** saving or stopping the collector, so invalid regexes leave working monitoring and the last valid configuration intact. The preflight never fetches sources, opens state, or binds an API listener.

Filters affect delivered items/extracts, not crawling or saved raw posts. Applying valid changes briefly restarts the owned collector only if it was running; stopped monitoring stays stopped. Source IDs and crawl progress are preserved. Filter counts describe currently applied rules, not a live preview of unsaved changes.

Changing a source URL or format creates a new source ID and fresh crawl baseline. Other edits preserve its ID. Saving while running first terminates the owned child, then restarts it with the new configuration. Deleting a source stops monitoring it but retains backend state.

The build generates the ivory line-art moth on a charcoal app tile at all standard `.icns` resolutions using `Tools/GenerateIcons.swift` and the system `iconutil`. The app icon and menu-bar template share original vector paths in `Sources/Nightwire/MothArtwork.swift`. Small sizes use fewer details and heavier outlines; the wings remain unfilled. The menu-bar version is a resolution-independent template, so macOS adapts it to light/dark backgrounds.

## Ownership and privacy

Normal state lives under `~/Library/Application Support/Nightwire/`: private configuration, backend state/enrichment, and unique private run directories. Profile directories are mode `0700`; configuration/run artifacts are private. A profile lock prevents two client instances sharing state.

Each child gets a fresh random 32-byte hex API token in `NIGHTWIRE_TOKEN`, never in arguments or configuration. The API binds `127.0.0.1:0`; readiness must match the owned child PID and numeric loopback address/port. Local requests use bearer authentication, no persistent cookies/cache/system proxy, and no redirects. Normal quit awaits owned-child termination. Abrupt OS termination cannot guarantee application cleanup.

The live stream subscribes before the snapshot, reconnects/resyncs automatically, merges revisions by observation time including same-hash enrichment, and removes filtered tombstones. A connected stream means the local API is connected—not that remote sources are healthy. Source errors/backlog are separate. Baseline, access restrictions, unverified claims, and **local extract · not AI** remain explicit.

## Isolated GUI checks

Only run these intentionally; both use a separate absolute private directory, never the normal profile:

```sh
# Normal interactive UI, isolated profile; still does not auto-start.
apps/macos/build/Nightwire.app/Contents/MacOS/Nightwire --data-dir /absolute/private-profile

# New/empty directory only: two empty-source backend start/auth/SSE/snapshot/stop
# cycles, open the real Add Source sheet, write smoke-result.json, then quit.
apps/macos/build/Nightwire.app/Contents/MacOS/Nightwire --smoke-test /absolute/new-empty-smoke-dir
```

The smoke mode also verifies invalid RE2 rules preserve a running child/configuration. While stopped, it saves a synthetic `example.invalid` source **only in its isolated profile**, checks filter preference persistence/custom-rule preservation, captures `filter-settings.png`, and removes the fixture. That source is never collected. It then drives the real Add Source draft-selection handler through Ready, login-required research-only, and Custom entries, checking draft/Save readiness and unchanged configuration with zero sources; captures are `catalog-ready.png`, `catalog-research.png`, and `catalog-custom.png`. It never saves a catalog source. The smoke mode checks actual window widths of 600, 756, and 1250 points and saves matching `window-*.png` images, plus `empty-window.png` and `source-editor.png`, using only app-owned `NSView` bitmap capture. **These are partial content captures:** native/layer-backed sidebar material, controls, toolbar, or titlebar may be missing. They do not establish complete on-screen visual correctness and do not capture the desktop or other applications. No screen-recording/accessibility permission bypass is used.

## Current limits

Local/ad-hoc development build only: no notarization, installer, notifications, browser automation, or bundled Tor. Resync uses the backend's latest-200-item API; there is no archive pagination. Search covers received text only. The app does not import existing real sources, and filtered raw state remains retained by the backend rather than deleted. The CLI test suite covers wire contracts, revision/tombstone/enrichment handling, source binding, SSE byte framing, private storage, and profile isolation; GUI smoke must be run separately.
