package watch

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"time"
)

// CrawlState commits page progress and retry scheduling durably. Baseline labels
// survive failures/restarts; completed index history is revisited only for discovery.
type CrawlState struct {
	SeedURL     string                  `json:"seed_url"`
	Format      string                  `json:"format"`
	Pending     []CrawlTask             `json:"pending,omitempty"`
	Indices     []string                `json:"indices,omitempty"`
	SeenIndices map[string]bool         `json:"seen_indices,omitempty"`
	Threads     map[string]CrawlThread  `json:"threads"`
	Failures    map[string]CrawlFailure `json:"failures,omitempty"`
	NextWatch   int                     `json:"next_watch,omitempty"`
	NextPending bool                    `json:"next_pending,omitempty"`
}

type CrawlFailure struct {
	Attempts int       `json:"attempts"`
	RetryAt  time.Time `json:"retry_at"`
}

type crawlHTTPError struct {
	status     int
	retryAfter bool
}

func (e *crawlHTTPError) Error() string { return fmt.Sprintf("HTTP %d", e.status) }

// A partial check must be visible without applying source-wide backoff: only the
// affected pages are deferred. Explicit rate limits still use ordinary errors.
type crawlPartialError struct{ pages int }

func (e *crawlPartialError) Error() string {
	return fmt.Sprintf("incomplete crawl coverage: %d failed pages awaiting retry", e.pages)
}

type CrawlTask struct {
	URL      string `json:"url"`
	Kind     string `json:"kind"`
	Baseline bool   `json:"baseline,omitempty"`
}

type CrawlThread struct {
	URL   string `json:"url"`
	Ready bool   `json:"ready"`
}

func cloneCrawl(state *CrawlState) *CrawlState {
	if state == nil {
		return nil
	}
	copy := *state
	copy.Pending = slices.Clone(state.Pending)
	copy.Indices = slices.Clone(state.Indices)
	copy.Threads = maps.Clone(state.Threads)
	copy.SeenIndices = maps.Clone(state.SeenIndices)
	copy.Failures = maps.Clone(state.Failures)
	return &copy
}

func (c *Collector) publish(item Item) {
	c.Hub.Publish(item)
	if c.OnItem != nil {
		c.OnItem(item)
	}
}

func (state *CrawlState) enqueue(task CrawlTask, limit int) error {
	for _, old := range state.Pending {
		if old.URL == task.URL && old.Kind == task.Kind {
			return nil
		}
	}
	if len(state.Pending) >= 2*limit+128 {
		return fmt.Errorf("crawl backlog exceeds configured bounds")
	}
	state.Pending = append(state.Pending, task)
	return nil
}

func (state *CrawlState) watch(visited map[string]bool, now time.Time) (CrawlTask, bool) {
	watches := make([]CrawlTask, 0, len(state.Indices)+len(state.Threads))
	for _, link := range state.Indices {
		watches = append(watches, CrawlTask{URL: link, Kind: "index"})
	}
	ids := make([]string, 0, len(state.Threads))
	for id, thread := range state.Threads {
		if thread.Ready {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		watches = append(watches, CrawlTask{URL: state.Threads[id].URL, Kind: "thread"})
	}
	for range watches {
		index := state.NextWatch % len(watches)
		state.NextWatch = (index + 1) % len(watches)
		if !visited[watches[index].URL] && !now.Before(state.Failures[watches[index].URL].RetryAt) {
			queued := false
			for _, task := range state.Pending {
				if task.URL == watches[index].URL {
					queued = true
					break
				}
			}
			if !queued {
				return watches[index], true
			}
		}
	}
	return CrawlTask{}, false
}

func (c *Collector) fetchCrawlPage(ctx context.Context, s Source, client *http.Client, link string) ([]byte, *url.URL, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("invalid crawl request")
	}
	original, _ := url.Parse(s.URL)
	if !browserFinalURL(original, req.URL) {
		return nil, nil, 0, fmt.Errorf("crawl request left the configured source")
	}
	if s.Fetcher != "browser" {
		req.Header.Set("User-Agent", s.UserAgent)
	}
	res, err := client.Do(req)
	if err != nil {
		if wrapped, ok := err.(*url.Error); ok {
			err = wrapped.Err
		}
		return nil, nil, 0, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, nil, retryAfter(res.Header.Get("Retry-After"), time.Now()), &crawlHTTPError{status: res.StatusCode, retryAfter: res.Header.Get("Retry-After") != ""}
	}
	if res.Request == nil || !browserFinalURL(original, res.Request.URL) {
		return nil, nil, 0, fmt.Errorf("crawl response left the configured source")
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxBody+1))
	if err != nil {
		return nil, nil, 0, fmt.Errorf("could not read crawl response")
	}
	if len(body) > maxBody {
		return nil, nil, 0, fmt.Errorf("crawl response exceeds size limit")
	}
	return body, res.Request.URL, 0, nil
}

func (c *Collector) pollCrawl(ctx context.Context, s Source, client *http.Client) (time.Duration, error) {
	pages, threads, gap := s.MaxPagesPerPoll, s.MaxThreads, s.RequestGapSeconds
	if pages == 0 {
		pages = 4
	}
	if threads == 0 {
		threads = 256
	}
	if gap == 0 {
		gap = 2
	}
	checkpoint := c.Store.Checkpoint(s.ID)
	if checkpoint.Crawl == nil {
		kind := "thread"
		if indexFormat(s.Format) {
			kind = "index"
		}
		checkpoint.Crawl = &CrawlState{SeedURL: s.URL, Format: s.Format, Threads: map[string]CrawlThread{}, Pending: []CrawlTask{{URL: s.URL, Kind: kind, Baseline: true}}}
	}
	state := checkpoint.Crawl
	if state.SeedURL != s.URL || state.Format != s.Format {
		return 0, fmt.Errorf("crawl checkpoint source URL/format changed or missing; use a new source ID or explicitly migrate the checkpoint")
	}
	if state.Threads == nil {
		return 0, fmt.Errorf("incomplete crawl checkpoint")
	}
	c.crawlStatus(s.ID, state)
	visited := map[string]bool{}
	checked, changed := 0, 0
	for checked < pages {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		// Alternate live-tail refreshes and durable catch-up work; deferred pages
		// remain queued but cannot monopolize either side of the scheduler.
		now := time.Now().UTC()
		var task CrawlTask
		var ok bool
		pending := -1
		if !state.NextPending {
			task, ok = state.watch(visited, now)
		}
		if !ok {
			for i, candidate := range state.Pending {
				if !visited[candidate.URL] && !now.Before(state.Failures[candidate.URL].RetryAt) {
					task, ok, pending = candidate, true, i
					break
				}
			}
		}
		if !ok {
			task, ok = state.watch(visited, now)
		}
		if !ok {
			break
		}
		if checked > 0 && !wait(ctx, time.Duration(gap)*time.Second) {
			return 0, ctx.Err()
		}
		body, final, retry, err := c.fetchCrawlPage(ctx, s, client, task.URL)
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		if httpErr, ok := err.(*crawlHTTPError); ok && (httpErr.status == http.StatusTooManyRequests || httpErr.retryAfter) {
			return retry, err // Never bypass an explicit source-wide rate limit.
		}
		var next *CrawlState
		var candidates []Candidate
		if err == nil {
			next, candidates, err = state.crawlPage(task, pending, final, body, threads)
		}
		if err != nil {
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}
			next = cloneCrawl(state)
			next.NextPending = !state.NextPending
			if pending >= 0 {
				next.Pending = append(slices.Delete(next.Pending, pending, pending+1), task)
			}
			if next.Failures == nil {
				next.Failures = map[string]CrawlFailure{}
			}
			failure := next.Failures[task.URL]
			failure.Attempts = min(failure.Attempts+1, 20)
			base := time.Duration(s.IntervalSeconds) * time.Second
			if base <= 0 {
				base = time.Minute
			}
			failure.RetryAt = time.Now().UTC().Add(failureDelay(base, failure.Attempts))
			next.Failures[task.URL] = failure
			// Entries can only belong to bounded pending tasks or current watches.
			if len(next.Failures) > 3*threads+192 {
				return 0, fmt.Errorf("crawl failure schedule exceeds configured bounds")
			}
			if persistErr := c.Store.saveCrawlSchedule(s.ID, next); persistErr != nil {
				return 0, fmt.Errorf("persist crawl retry: %w", persistErr)
			}
			state, checkpoint.Crawl = next, next
			checked++
			visited[task.URL] = true
			c.crawlStatus(s.ID, state)
			slog.Warn("crawl page deferred", "source", s.ID, "kind", task.Kind, "error", err, "retry_at", failure.RetryAt, "failed_pages", len(state.Failures))
			continue
		}
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		checkpoint.Crawl, checkpoint.ETag, checkpoint.LastModified = next, "", ""
		items, err := c.Store.ApplyPage(s.ID, candidates, checkpoint, time.Now().UTC(), task.Baseline)
		if err != nil {
			return 0, fmt.Errorf("persist crawl page: %w", err)
		}
		for _, item := range items {
			c.publish(item)
		}
		checked++
		changed += len(items)
		visited[task.URL], visited[final.String()] = true, true
		state = next
		c.crawlStatus(s.ID, state)
	}
	coverage := c.Store.contentCoverage(s.ID)
	slog.Info("source checked", "source", s.ID, "transport", s.Transport, "pages", checked, "changed", changed, "pending_pages", len(state.Pending), "tracked_threads", len(state.Threads), "failed_pages", len(state.Failures),
		"public_text_posts", coverage.PublicTextPosts, "partial_posts", coverage.PartialPosts, "restricted_posts", coverage.RestrictedPosts)
	if len(state.Failures) > 0 {
		return 0, &crawlPartialError{pages: len(state.Failures)}
	}
	return 0, nil
}

func (c *Collector) crawlStatus(id string, state *CrawlState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	status := c.status[id]
	status.PendingPages, status.TrackedThreads, status.FailedPages = len(state.Pending), len(state.Threads), len(state.Failures)
	c.status[id] = status
}

// Build privately so neither parse/discovery errors nor failed writes advance a page.
func (state *CrawlState) crawlPage(task CrawlTask, pending int, final *url.URL, body []byte, threads int) (*CrawlState, []Candidate, error) {
	if task.Kind != "index" && task.Kind != "thread" {
		return nil, nil, fmt.Errorf("invalid crawl task")
	}
	index := task.Kind == "index"
	var candidates []Candidate
	if !index {
		requested, _ := url.Parse(task.URL)
		if expected := forumThread(requested, state.Format); expected != "" && expected != forumThread(final, state.Format) {
			return nil, nil, fmt.Errorf("thread redirect changed thread identity")
		}
		if forumThread(final, state.Format) == "" {
			return nil, nil, fmt.Errorf("unsupported forum thread URL")
		}
		var err error
		candidates, err = parseForumPosts(bytes.NewReader(body), final, state.Format)
		if err != nil {
			return nil, nil, fmt.Errorf("could not parse forum posts; check access or layout")
		}
	}
	links, err := parseForumLinks(bytes.NewReader(body), final, index, state.Format)
	if err != nil {
		return nil, nil, fmt.Errorf("could not parse forum navigation; check access or layout")
	}
	next := cloneCrawl(state)
	next.NextPending = !state.NextPending
	delete(next.Failures, task.URL)
	if pending >= 0 {
		next.Pending = slices.Delete(next.Pending, pending, pending+1)
	}
	follow := !index
	if index {
		current := crawlURL(final, final.String()).String()
		// Compare before marking this page. New threads can push other new threads
		// onto later pages, but an unchanged listing must not restart old history.
		follow = !state.SeenIndices[current]
		if next.SeenIndices == nil {
			next.SeenIndices = map[string]bool{}
		}
		if !next.SeenIndices[current] && len(next.SeenIndices) >= 4096 {
			return nil, nil, fmt.Errorf("crawl exceeds 4096 index pages; narrow the source")
		}
		next.SeenIndices[current] = true
		first := crawlURL(final, final.String())
		first.Path = xenForoPagePath.ReplaceAllString(first.Path, "/")
		first.RawQuery = ""
		if !slices.Contains(next.Indices, first.String()) {
			if len(next.Indices) >= 64 {
				return nil, nil, fmt.Errorf("crawl exceeds 64 forum indices")
			}
			next.Indices = append(next.Indices, first.String())
		}
		for _, link := range links.Forums {
			if slices.Contains(next.Indices, link) {
				continue
			}
			if len(next.Indices) >= 64 {
				return nil, nil, fmt.Errorf("crawl exceeds 64 forum indices")
			}
			next.Indices = append(next.Indices, link)
			if err := next.enqueue(CrawlTask{URL: link, Kind: "index", Baseline: task.Baseline}, threads); err != nil {
				return nil, nil, err
			}
		}
		for _, link := range links.Threads {
			u, _ := url.Parse(link)
			id := forumThread(u, state.Format)
			if _, exists := next.Threads[id]; exists {
				continue
			}
			if len(next.Threads) >= threads {
				return nil, nil, fmt.Errorf("tracked-thread limit reached; increase max_threads or narrow the source")
			}
			follow = true
			next.Threads[id] = CrawlThread{URL: link}
			if err := next.enqueue(CrawlTask{URL: link, Kind: "thread", Baseline: task.Baseline}, threads); err != nil {
				return nil, nil, err
			}
		}
		if links.Next != "" && !state.SeenIndices[links.Next] {
			follow = true
		}
	} else {
		id := forumThread(final, state.Format)
		if _, exists := next.Threads[id]; !exists && len(next.Threads) >= threads {
			return nil, nil, fmt.Errorf("tracked-thread limit reached")
		}
		next.Threads[id] = CrawlThread{URL: final.String(), Ready: links.Next == ""}
	}
	if links.Next != "" && follow {
		if err := next.enqueue(CrawlTask{URL: links.Next, Kind: task.Kind, Baseline: task.Baseline}, threads); err != nil {
			return nil, nil, err
		}
	}
	return next, candidates, nil
}
