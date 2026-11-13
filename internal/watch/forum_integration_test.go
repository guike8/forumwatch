package watch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func myBBCrawlPost(id int, body string) string {
	return fmt.Sprintf(`<title>Public discussion - DarkForums</title><div class="post" id="post_%d"><div class="post__user-profile"><a href="/User-tester">tester</a></div><span class="post_date">A relative date</span><div class="post_body" id="pid_%d">%s</div><div id="post_meta_%d"><script>quick_quote(%d,'tester',1794534289);</script></div></div>`, id, id, body, id, id)
}

func myBBCrawlIndex(ids ...int) string {
	var text strings.Builder
	text.WriteString(`<title>Public category | DarkForums</title><table class="tborder"><tbody>`)
	for _, id := range ids {
		fmt.Fprintf(&text, `<tr><td class="forumdisplay_regular"><span class="subject_new" id="tid_%d"><a href="/Thread-Topic-%d">Thread</a></span></td></tr>`, id, id)
	}
	text.WriteString(`</tbody></table>`)
	return text.String()
}

func TestMyBBCrawlConfigBounds(t *testing.T) {
	for _, format := range []string{"mybb", "mybb-index"} {
		config := Config{Sources: []Source{{ID: "mybb", URL: "http://forum.onion/Thread-Topic-1", Format: format, Transport: "tor"}}}
		if err := config.Validate(); err != nil {
			t.Fatal(err)
		}
		if config.Sources[0].MaxPagesPerPoll != 4 || config.Sources[0].MaxThreads != 256 || config.Sources[0].RequestGapSeconds != 2 {
			t.Fatal("MyBB did not receive bounded crawl defaults")
		}
		config.Sources[0].MaxPagesPerPoll = 33
		if err := config.Validate(); err == nil {
			t.Fatal("unbounded MyBB page budget accepted")
		}
	}
}

func TestMyBBCrawlDiscoveryRestartAndReplyCatchup(t *testing.T) {
	var stage, requests atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Path {
		case "/Forum-Access-Market":
			if stage.Load() > 0 {
				fmt.Fprint(w, myBBCrawlIndex(1, 2))
			} else {
				fmt.Fprint(w, myBBCrawlIndex(1))
			}
		case "/Thread-Topic-1":
			switch r.URL.Query().Get("page") {
			case "3":
				fmt.Fprint(w, myBBCrawlPost(13, "New reply after rollover."))
			case "2":
				fmt.Fprint(w, myBBCrawlPost(12, "Historical reply."))
				if stage.Load() > 1 {
					fmt.Fprint(w, `<div class="pagination"><a class="pagination_next" href="/Thread-Topic-1?page=3">Next</a></div>`)
				}
			default:
				fmt.Fprint(w, myBBCrawlPost(11, "Historical thread body.")+`<div class="pagination"><a class="pagination_next" href="/Thread-Topic-1?page=2">Next</a></div>`)
			}
		case "/Thread-Topic-2":
			fmt.Fprint(w, myBBCrawlPost(21, "Newly discovered thread."))
		default:
			t.Errorf("unexpected request: %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer origin.Close()
	collector := testCollector(t)
	source := Source{ID: "mybb", URL: origin.URL + "/Forum-Access-Market", Format: "mybb-index", Transport: "direct", MaxPagesPerPoll: 1}
	client, err := newClient(source, "")
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	poll := func(count int) {
		t.Helper()
		for range count {
			before := requests.Load()
			if _, err := collector.poll(context.Background(), source, client); err != nil {
				t.Fatal(err)
			}
			if requests.Load()-before > 1 {
				t.Fatal("request budget exceeded")
			}
		}
	}
	poll(1)
	if len(collector.Store.Items(0)) != 0 || len(collector.Store.Checkpoint(source.ID).Crawl.Threads) != 1 {
		t.Fatal("listing frontier was not committed before fetching posts")
	}
	reopened, err := OpenStore(collector.Store.path)
	if err != nil {
		t.Fatal(err)
	}
	collector.Store = reopened
	poll(10)
	if len(collector.Store.Items(0)) != 2 {
		t.Fatalf("backfill failed: %+v", collector.Store.Items(0))
	}
	for _, item := range collector.Store.Items(0) {
		if item.Kind != "baseline" {
			t.Fatalf("historical MyBB post not baseline: %+v", item)
		}
	}
	stage.Store(1)
	poll(12)
	stage.Store(2)
	poll(12)
	items := collector.Store.Items(0)
	if len(items) != 4 {
		t.Fatalf("thread/reply catchup failed: %+v", items)
	}
	for _, item := range items {
		if (item.PostID == "post-21" || item.PostID == "post-13") && item.Kind != "new" {
			t.Fatalf("new MyBB post mislabeled: %+v", item)
		}
	}
}

func TestMyBBCrawlRejectsForeignRedirect(t *testing.T) {
	var contacted atomic.Bool
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { contacted.Store(true) }))
	defer foreign.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, foreign.URL+"/Thread-Topic-1", 302) }))
	defer origin.Close()
	collector := testCollector(t)
	source := Source{ID: "mybb", URL: origin.URL + "/Thread-Topic-1", Format: "mybb", Transport: "direct", MaxPagesPerPoll: 1}
	client, err := newClient(source, "")
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	if _, err := collector.poll(context.Background(), source, client); err == nil || contacted.Load() {
		t.Fatalf("foreign MyBB redirect followed: %v", err)
	}
}
