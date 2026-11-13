package watch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func crawlPost(id int, body string) string {
	return fmt.Sprintf(`<h1 class="p-title-value">Public discussion</h1><article data-content="post-%d" data-author="tester"><a href="/posts/%d/"><time itemprop="datePublished" datetime="2026-11-13T12:00:00Z"></time></a><div class="bbWrapper">%s</div></article>`, id, id, body)
}

func crawlIndex(ids ...int) string {
	var body strings.Builder
	body.WriteString(`<h1 class="p-title-value">Threads</h1><div class="structItemContainer">`)
	for _, id := range ids {
		fmt.Fprintf(&body, `<div class="structItem-title"><a href="/threads/topic.%d/">Thread</a></div>`, id)
	}
	body.WriteString(`</div>`)
	return body.String()
}

func TestXenForoDiscoveryScopeAndPagination(t *testing.T) {
	base, _ := url.Parse("http://forum.onion/forums/news.2/")
	data := crawlIndex(1, 1, 2) + `<div class="structItem-title"><a href="https://other.onion/threads/3/">foreign</a><a href="http://user:secret@forum.onion/threads/4/">credentials</a></div><div class="bbWrapper"><a href="/threads/9/">post link is not discovery</a></div><a class="pageNav-jump--next" href="/forums/news.2/page-2/?token=secret">Next</a>`
	links, err := ParseXenForoLinks(strings.NewReader(data), base, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(links.Threads) != 2 || links.Next != "http://forum.onion/forums/news.2/page-2/" {
		t.Fatalf("bad navigation: %+v", links)
	}
	for _, data := range []string{`<h1>Sign in</h1>`, crawlIndex(1) + `<input type="password">`, crawlIndex(1) + `<a class="pageNav-jump--next" href="/forums/news.2/">loop</a>`, crawlIndex(1) + `<a class="pageNav-jump--next" href="/forums/other.3/page-2/">wrong forum</a>`} {
		if _, err := ParseXenForoLinks(strings.NewReader(data), base, true); err == nil {
			t.Fatalf("accepted unsafe/unsupported page: %s", data)
		}
	}
}

func TestCrawlBaselineRestartNewThreadsAndReplyCatchup(t *testing.T) {
	var stage atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/forums/news.1/":
			if stage.Load() > 0 {
				fmt.Fprint(w, crawlIndex(1, 2))
			} else {
				fmt.Fprint(w, crawlIndex(1))
			}
		case "/threads/topic.1/":
			fmt.Fprint(w, crawlPost(11, "first historical page")+`<a class="pageNav-jump--next" href="/threads/topic.1/page-2/">Next</a>`)
		case "/threads/topic.1/page-2/":
			fmt.Fprint(w, crawlPost(12, "historical reply"))
			if stage.Load() > 1 {
				fmt.Fprint(w, `<a class="pageNav-jump--next" href="/threads/topic.1/page-3/">Next</a>`)
			}
		case "/threads/topic.1/page-3/":
			fmt.Fprint(w, crawlPost(13, "new reply after page rollover"))
		case "/threads/topic.2/":
			fmt.Fprint(w, crawlPost(21, "brand new thread"))
		default:
			t.Errorf("unexpected URL %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer origin.Close()
	c := testCollector(t)
	source := Source{ID: "forum", URL: origin.URL + "/forums/news.1/", Transport: "direct", Format: "xenforo-index", MaxPagesPerPoll: 1}
	client, _ := newClient(source, "")
	defer client.CloseIdleConnections()
	poll := func() {
		t.Helper()
		if _, err := c.poll(context.Background(), source, client); err != nil {
			t.Fatal(err)
		}
	}
	poll() // Index is durable before any post fetch.
	if len(c.Store.Items(0)) != 0 || len(c.Store.Checkpoint(source.ID).Crawl.Pending) != 1 {
		t.Fatal("index frontier not persisted")
	}
	reopened, err := OpenStore(c.Store.path)
	if err != nil {
		t.Fatal(err)
	}
	c.Store = reopened
	for range 10 {
		poll()
	}
	if len(c.Store.Items(0)) != 2 {
		t.Fatalf("initial catchup: %+v", c.Store.Items(0))
	}
	for _, item := range c.Store.Items(0) {
		if item.Kind != "baseline" {
			t.Fatal("historical post mislabeled", item.Kind)
		}
	}
	stage.Store(1)
	for range 12 {
		poll()
	}
	items := c.Store.Items(0)
	if len(items) != 3 {
		t.Fatalf("new thread missing: %+v", items)
	}
	for _, item := range items {
		if item.PostID == "post-21" && item.Kind != "new" {
			t.Fatalf("new thread marked %s", item.Kind)
		}
	}
	stage.Store(2)
	for range 12 {
		poll()
	}
	items = c.Store.Items(0)
	if len(items) != 4 {
		t.Fatalf("reply rollover missing: %+v", items)
	}
	for _, item := range items {
		if item.PostID == "post-13" && item.Kind != "new" {
			t.Fatalf("new reply marked %s", item.Kind)
		}
	}
	// Canonical source post links are stable across the pages used to discover them.
	if !strings.HasSuffix(items[0].URL, "/posts/13/") {
		t.Fatalf("unstable permalink: %s", items[0].URL)
	}
}

func TestCrawlFailureDoesNotAdvanceAndHonorsRetryAfter(t *testing.T) {
	var fail atomic.Bool
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/forums/1/" {
			fmt.Fprint(w, crawlIndex(1))
			return
		}
		if fail.Load() {
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(429)
			return
		}
		fmt.Fprint(w, crawlPost(1, "post"))
	}))
	defer origin.Close()
	c := testCollector(t)
	source := Source{ID: "forum", URL: origin.URL + "/forums/1/", Transport: "direct", Format: "xenforo-index", MaxPagesPerPoll: 1}
	client, _ := newClient(source, "")
	defer client.CloseIdleConnections()
	if _, err := c.poll(context.Background(), source, client); err != nil {
		t.Fatal(err)
	}
	before := c.Store.Checkpoint(source.ID)
	fail.Store(true)
	retry, err := c.poll(context.Background(), source, client)
	if err == nil || retry.Seconds() != 120 {
		t.Fatalf("retry=%s error=%v", retry, err)
	}
	if !reflect.DeepEqual(before, c.Store.Checkpoint(source.ID)) {
		t.Fatal("failed fetch advanced frontier")
	}
	fail.Store(false)
	original := c.Store.path
	c.Store.path = t.TempDir() // Rename over a directory fails.
	if _, err := c.poll(context.Background(), source, client); err == nil {
		t.Fatal("write unexpectedly succeeded")
	}
	c.Store.path = original
	if !reflect.DeepEqual(before, c.Store.Checkpoint(source.ID)) || len(c.Store.Items(0)) != 0 {
		t.Fatal("failed persistence advanced crawl")
	}
}

func TestCrawlRequestBoundAndForeignRedirect(t *testing.T) {
	var requests atomic.Int32
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer foreign.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, foreign.URL+"/threads/1/", 302) }))
	defer origin.Close()
	c := testCollector(t)
	source := Source{ID: "forum", URL: origin.URL + "/threads/1/", Transport: "direct", Format: "xenforo", MaxPagesPerPoll: 1}
	client, _ := newClient(source, "")
	defer client.CloseIdleConnections()
	if _, err := c.poll(context.Background(), source, client); err == nil || requests.Load() != 0 {
		t.Fatalf("foreign redirect followed: %v", err)
	}
}

func TestCrawlForumBaselineAndThreadLimit(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			fmt.Fprint(w, `<div class="node--forum"><h3 class="node-title"><a href="/forums/1/">News</a></h3></div>`)
		case "/forums/1/":
			fmt.Fprint(w, crawlIndex(1))
		default:
			fmt.Fprint(w, crawlPost(1, "historical category post"))
		}
	}))
	defer origin.Close()
	c := testCollector(t)
	source := Source{ID: "forum", URL: origin.URL + "/", Transport: "direct", Format: "xenforo-index", MaxPagesPerPoll: 1, MaxThreads: 1}
	client, _ := newClient(source, "")
	defer client.CloseIdleConnections()
	for range 12 {
		if _, err := c.poll(context.Background(), source, client); err != nil {
			t.Fatal(err)
		}
	}
	items := c.Store.Items(0)
	if len(items) != 1 || items[0].Kind != "baseline" {
		t.Fatalf("category backfill mislabeled: %+v", items)
	}
	state := &CrawlState{}
	for i := range 130 {
		if err := state.enqueue(CrawlTask{URL: fmt.Sprintf("http://forum.onion/threads/%d/", i), Kind: "thread"}, 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := state.enqueue(CrawlTask{URL: "http://forum.onion/threads/999/", Kind: "thread"}, 1); err == nil {
		t.Fatal("queue bound not enforced")
	}
}

func TestCrawlFailedPagesRotateAndPersist(t *testing.T) {
	for _, watched := range []bool{false, true} {
		for _, failure := range []string{"404", "403", "parse"} {
			t.Run(fmt.Sprintf("watched=%v/%s", watched, failure), func(t *testing.T) {
				var badRequests, goodRequests atomic.Int32
				var recovered atomic.Bool
				origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/threads/topic.1/" {
						badRequests.Add(1)
						if !recovered.Load() {
							switch failure {
							case "404":
								w.WriteHeader(http.StatusNotFound)
							case "403":
								w.WriteHeader(http.StatusForbidden)
							default:
								fmt.Fprint(w, "<h1>Not a thread</h1>")
							}
							return
						}
						fmt.Fprint(w, crawlPost(1, "recovered"))
						return
					}
					goodRequests.Add(1)
					fmt.Fprint(w, crawlPost(2, "healthy"))
				}))
				defer origin.Close()
				c := testCollector(t)
				source := Source{ID: "forum", URL: origin.URL + "/threads/topic.1/", Transport: "direct", Format: "xenforo", MaxPagesPerPoll: 1}
				client, _ := newClient(source, "")
				defer client.CloseIdleConnections()
				state := &CrawlState{SeedURL: source.URL, Format: source.Format, Threads: map[string]CrawlThread{}}
				for _, id := range []string{"1", "2"} {
					link := origin.URL + "/threads/topic." + id + "/"
					state.Threads[id] = CrawlThread{URL: link, Ready: watched}
					if !watched {
						state.Pending = append(state.Pending, CrawlTask{URL: link, Kind: "thread", Baseline: true})
					}
				}
				state.NextPending = !watched
				if err := c.Store.saveCrawlSchedule(source.ID, state); err != nil {
					t.Fatal(err)
				}
				pollPartial := func() {
					t.Helper()
					if _, err := c.poll(context.Background(), source, client); err == nil {
						t.Fatal("failed page advertised complete coverage")
					} else if _, partial := err.(*crawlPartialError); !partial {
						t.Fatal(err)
					}
				}
				pollPartial()
				checkpoint := c.Store.Checkpoint(source.ID)
				if checkpoint.Initialized || len(c.Store.Items(0)) != 0 {
					t.Fatal("failed initial request initialized source or published items")
				}
				deferred := checkpoint.Crawl.Failures[source.URL]
				if deferred.Attempts != 1 || !deferred.RetryAt.After(time.Now()) {
					t.Fatalf("missing retry deadline: %+v", deferred)
				}
				if !watched && (len(checkpoint.Crawl.Pending) != 2 || checkpoint.Crawl.Pending[1].URL != source.URL || !checkpoint.Crawl.Pending[1].Baseline) {
					t.Fatal("failed pending task was not retained/rotated with baseline")
				}
				reopened, err := OpenStore(c.Store.path)
				if err != nil {
					t.Fatal(err)
				}
				c.Store = reopened
				for range 4 {
					pollPartial()
				}
				if badRequests.Load() != 1 || goodRequests.Load() == 0 || len(c.Store.Items(0)) != 1 {
					t.Fatalf("retry backoff blocked healthy work: bad=%d good=%d", badRequests.Load(), goodRequests.Load())
				}
				if c.status[source.ID].FailedPages != 1 {
					t.Fatal("failed page missing from status")
				}
				if !watched && c.Store.Items(0)[0].Kind != "baseline" {
					t.Fatal("healthy pending baseline lost across restart")
				}
				// Make the deferred task eligible without waiting on wall-clock backoff.
				state = c.Store.Checkpoint(source.ID).Crawl
				deferred.RetryAt = time.Now().Add(-time.Second)
				state.Failures[source.URL] = deferred
				if err := c.Store.saveCrawlSchedule(source.ID, state); err != nil {
					t.Fatal(err)
				}
				recovered.Store(true)
				for range 4 {
					if _, err := c.poll(context.Background(), source, client); err != nil {
						if _, partial := err.(*crawlPartialError); !partial {
							t.Fatal(err)
						}
					}
				}
				if badRequests.Load() < 2 || len(c.Store.Items(0)) != 2 || c.status[source.ID].FailedPages != 0 {
					t.Fatal("recovered task did not resume and clear failure")
				}
			})
		}
	}
}

func TestCrawlCompletedIndexHistoryAndShiftedDiscovery(t *testing.T) {
	var stage, pageTwo, pageThree, requests atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Path {
		case "/forums/1/":
			if stage.Load() == 0 {
				fmt.Fprint(w, crawlIndex(1))
			} else {
				fmt.Fprint(w, crawlIndex(4, 1))
			}
			fmt.Fprint(w, `<a class="pageNav-jump--next" href="/forums/1/page-2/">Next</a>`)
		case "/forums/1/page-2/":
			pageTwo.Add(1)
			if stage.Load() == 0 {
				fmt.Fprint(w, crawlIndex(2))
			} else {
				fmt.Fprint(w, crawlIndex(5, 2))
			}
			fmt.Fprint(w, `<a class="pageNav-jump--next" href="/forums/1/page-3/">Next</a>`)
		case "/forums/1/page-3/":
			pageThree.Add(1)
			fmt.Fprint(w, crawlIndex(3))
		default:
			id, _ := strconv.Atoi(xenForoThread(r.URL))
			fmt.Fprint(w, crawlPost(id, "post"))
		}
	}))
	defer origin.Close()
	c := testCollector(t)
	source := Source{ID: "forum", URL: origin.URL + "/forums/1/", Transport: "direct", Format: "xenforo-index", MaxPagesPerPoll: 1}
	client, _ := newClient(source, "")
	defer client.CloseIdleConnections()
	poll := func(n int) {
		t.Helper()
		for range n {
			before := requests.Load()
			if _, err := c.poll(context.Background(), source, client); err != nil {
				t.Fatal(err)
			}
			if requests.Load()-before > 1 {
				t.Fatal("poll exceeded request budget")
			}
		}
	}
	poll(30)
	if pageTwo.Load() != 1 || pageThree.Load() != 1 || len(c.Store.Items(0)) != 3 {
		t.Fatalf("initial history repeated or incomplete: page2=%d page3=%d items=%d", pageTwo.Load(), pageThree.Load(), len(c.Store.Items(0)))
	}
	if len(c.Store.Checkpoint(source.ID).Crawl.SeenIndices) != 3 {
		t.Fatal("index traversal not persisted")
	}
	reopened, err := OpenStore(c.Store.path)
	if err != nil {
		t.Fatal(err)
	}
	c.Store = reopened
	poll(20)
	if pageTwo.Load() != 1 || pageThree.Load() != 1 {
		t.Fatal("restart resumed completed index history")
	}
	stage.Store(1)
	poll(40)
	if pageTwo.Load() != 2 || pageThree.Load() != 2 || len(c.Store.Items(0)) != 5 {
		t.Fatalf("shifted discovery failed/repeated: page2=%d page3=%d items=%d", pageTwo.Load(), pageThree.Load(), len(c.Store.Items(0)))
	}
	for _, item := range c.Store.Items(0) {
		if (item.PostID == "post-4" || item.PostID == "post-5") && item.Kind != "new" {
			t.Fatal("new thread on refreshed listing mislabeled", item)
		}
	}
}

func TestCrawlRejectsCheckpointScopeMismatch(t *testing.T) {
	var requests atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		fmt.Fprint(w, crawlPost(1, "original scope"))
	}))
	defer origin.Close()
	c := testCollector(t)
	source := Source{ID: "forum", URL: origin.URL + "/threads/topic.1/", Transport: "direct", Format: "xenforo", MaxPagesPerPoll: 1}
	client, _ := newClient(source, "")
	defer client.CloseIdleConnections()
	if _, err := c.poll(context.Background(), source, client); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(c.Store.path)
	if err != nil {
		t.Fatal(err)
	}
	c.Store = reopened
	original := c.Store.Checkpoint(source.ID)
	for _, change := range []string{"url", "format", "missing-fields"} {
		t.Run(change, func(t *testing.T) {
			changed := source
			switch change {
			case "url":
				changed.URL = origin.URL + "/threads/topic.2/"
			case "format":
				changed.Format = "xenforo-index"
			default:
				state := cloneCrawl(original.Crawl)
				state.SeedURL, state.Format = "", ""
				if err := c.Store.saveCrawlSchedule(source.ID, state); err != nil {
					t.Fatal(err)
				}
			}
			before := c.Store.Checkpoint(source.ID)
			count := requests.Load()
			_, err := c.poll(context.Background(), changed, client)
			if err == nil || !strings.Contains(err.Error(), "new source ID") || !strings.Contains(err.Error(), "migrate") {
				t.Fatalf("missing explicit migration guidance: %v", err)
			}
			if requests.Load() != count || !reflect.DeepEqual(before, c.Store.Checkpoint(source.ID)) {
				t.Fatal("mismatched source fetched or modified saved scope")
			}
		})
	}
}
