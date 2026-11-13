# Local/extractive summaries

ForumWatch enables asynchronous local/extractive summaries by default. These are **not AI output**, do not call a model or external service, and add no dependencies. Disable them with `-summaries=false`.

The initial item alert is published first. One background worker then extracts up to two substantive sentences from the collected body (falling back to the source excerpt, then title). Only the first 32 KiB of input is considered, and output is limited to 400 characters, plus a truncation marker. This is a simple excerpting heuristic, not a factual assessment. Quoted content, author identities, dates, and other context remain **unverified source claims**. Instructions embedded in source text are treated as text, not commands. Terminal controls and formatting characters are stripped from displayed text.

## Events and revisions

- A terminal `[SUMMARY]` event follows the initial alert without repeating `[NEW]`, `[UPDATED]`, or `[BASELINE]`.
- SSE clients may receive another `event: item` for the same ID and hash, this time with an `enrichment` object containing `method: "local/extractive"`, `text`, and `source_hash`. Clients should upsert by ID and hash, not count enrichment as another source revision.
- Enrichment never changes the source `summary`, revision `hash`, `observed_at`, or `kind`. Private RSS remains a source-excerpt feed; its revision GUID does not change for enrichment.
- Baseline summaries follow `-show-baseline`. Restart recovery never repeats saved baseline alerts, even with that flag enabled. Pending extracts for previously alerted non-baseline items may still appear as summary-only terminal events.

## Bounded work, persistence, and restart

The collector only performs a nonblocking enqueue into a 64-entry queue, after publishing the initial item. It does not run sentence extraction or summary disk writes inline. There is one worker, not one goroutine per item. Queue entries contain revision IDs, not full bodies. If the queue fills, durable latest revisions remain pending and are rediscovered by periodic scans. Scans use `Store.Items(0)` and handle at most 16 pending revisions per pass; they are not a durable event history. Memory for the item snapshot and publication tracking grows with the existing stored-item set, not with the number of edits or queued tasks.

Derived results are stored separately in `<state_path>.summaries`, with private file permissions and atomic replacement, keyed by item ID plus source revision hash. Only current-revision results are retained when this file is rewritten. Source/checkpoint persistence excludes enrichment. The same store mutex serializes summary persistence with source updates: there is no inline collector summarization, but the shared store can briefly contend during an atomic sidecar write. Do not delete this sidecar unless you intend to regenerate extracts.

On startup, summaries are loaded and attached only to matching current revisions. Pending latest revisions are recovered in the background without replaying initial alerts. Publication tracking prevents a rescan from enriching a newly committed item before its initial alert. Results computed for an older edited revision are discarded before persistence or publication. Failed writes leave the item pending for retry; corrupt sidecars stop startup rather than being silently reset. With `-summaries=false`, the sidecar is neither loaded nor written.

Cancellation stops new work and the command waits for the worker. An atomic filesystem operation already in progress is allowed to finish; filesystem calls cannot be forcibly canceled. Source revision retention and snapshot sorting are otherwise unchanged. Slow terminal/SSE clients still use the existing latest-state reconciliation behavior and may miss intermediate revisions or individual events.
