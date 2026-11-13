package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAlertFilterRules(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec AlertFilterSpec
		item Item
		want bool
	}{
		{"default", AlertFilterSpec{}, Item{Title: "Anything"}, true},
		{"include title", AlertFilterSpec{Include: `(?i)zero.day`}, Item{Title: "New ZERO DAY"}, true},
		{"include body normalized", AlertFilterSpec{Include: `initial access`}, Item{Body: "initial\n access"}, true},
		{"include missing", AlertFilterSpec{Include: `CVE-`}, Item{Title: "Account sales"}, false},
		{"exclude wins", AlertFilterSpec{Include: `CVE-`, Exclude: `(?i)combo`}, Item{Title: "CVE-123", Body: "Combo list"}, false},
		{"exclude title", AlertFilterSpec{ExcludeTitle: `(?i)gmail.*accounts`}, Item{Title: "Gmail premium accounts"}, false},
		{"body discussion kept", AlertFilterSpec{ExcludeTitle: `(?i)gmail.*accounts`}, Item{Title: "Initial access", Body: "Gmail premium accounts mentioned"}, true},
		{"feed fallback", AlertFilterSpec{Include: `ransomware`}, Item{Summary: "ransomware report"}, true},
		{"not derived extract", AlertFilterSpec{Include: `ransomware`}, Item{Enrichment: &Enrichment{Text: "ransomware"}}, false},
		{"restricted", AlertFilterSpec{ReadableOnly: true}, Item{Access: "restricted", Title: "Exploit", Summary: "gate"}, false},
		{"empty body", AlertFilterSpec{ReadableOnly: true}, Item{Title: "Exploit", Body: " \n\t"}, false},
		{"partial readable", AlertFilterSpec{ReadableOnly: true}, Item{Access: "partial", Body: "Public text"}, true},
		{"quiet sales preset", AlertFilterSpec{ExcludeTitle: `(?i)\b(combos?|combo lists?|(?:gmail|outlook|hotmail|yahoo|netflix|spotify).{0,40}accounts?)\b`}, Item{Title: "Selling Gmail premium accounts"}, false},
		{"quiet preset preserves access", AlertFilterSpec{ExcludeTitle: `(?i)\b(combos?|combo lists?|(?:gmail|outlook|hotmail|yahoo|netflix|spotify).{0,40}accounts?)\b`}, Item{Title: "Corporate initial access", Body: "Account login mentioned"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := compileAlertFilter(tc.spec)
			if err != nil || f.matches(tc.item) != tc.want {
				t.Fatalf("matches=%v want=%v err=%v", f.matches(tc.item), tc.want, err)
			}
		})
	}
	for _, spec := range []AlertFilterSpec{{Include: "["}, {Exclude: "["}, {ExcludeTitle: "["}} {
		c := Config{Sources: []Source{{ID: "source", URL: "https://example.com/", Format: "feed", Transport: "tor", AlertFilter: spec}}}
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "alert_filter.") {
			t.Fatalf("invalid filter accepted: %v", err)
		}
	}
}

func TestQuietSalesRules(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec AlertFilterSpec
		item Item
		want bool
	}{
		{"default preserves sales", AlertFilterSpec{}, Item{Title: "Gmail accounts and combo lists"}, true},
		{"explicit false preserves sales", AlertFilterSpec{QuietSales: false}, Item{Title: "Gmail accounts and combo lists"}, true},
		{"combo", AlertFilterSpec{QuietSales: true}, Item{Title: "Selling COMBO"}, false},
		{"combos", AlertFilterSpec{QuietSales: true}, Item{Title: "Fresh combos"}, false},
		{"combo list", AlertFilterSpec{QuietSales: true}, Item{Title: "Combo list sale"}, false},
		{"combo lists", AlertFilterSpec{QuietSales: true}, Item{Title: "Combo lists sale"}, false},
		{"gmail", AlertFilterSpec{QuietSales: true}, Item{Title: "Selling Gmail premium accounts"}, false},
		{"outlook", AlertFilterSpec{QuietSales: true}, Item{Title: "OUTLOOK account"}, false},
		{"hotmail", AlertFilterSpec{QuietSales: true}, Item{Title: "Hotmail accounts"}, false},
		{"yahoo", AlertFilterSpec{QuietSales: true}, Item{Title: "Yahoo accounts"}, false},
		{"netflix", AlertFilterSpec{QuietSales: true}, Item{Title: "Netflix accounts"}, false},
		{"spotify", AlertFilterSpec{QuietSales: true}, Item{Title: "Spotify account"}, false},
		{"normalized title", AlertFilterSpec{QuietSales: true}, Item{Title: "Gmail\n premium\t accounts"}, false},
		{"forty character gap", AlertFilterSpec{QuietSales: true}, Item{Title: "gmail" + strings.Repeat("-", 40) + "accounts"}, false},
		{"longer gap preserved", AlertFilterSpec{QuietSales: true}, Item{Title: "gmail" + strings.Repeat("-", 41) + "accounts"}, true},
		{"word boundaries", AlertFilterSpec{QuietSales: true}, Item{Title: "Combolists and mygmail accounts archive"}, true},
		{"body mentions preserved", AlertFilterSpec{QuietSales: true}, Item{Title: "Incident report", Body: "Combo lists and Gmail accounts"}, true},
		{"summary mentions preserved", AlertFilterSpec{QuietSales: true}, Item{Title: "Incident report", Summary: "Combo lists and Gmail accounts"}, true},
		{"title body not joined", AlertFilterSpec{QuietSales: true}, Item{Title: "Gmail investigation", Body: "accounts"}, true},
		{"corporate initial access", AlertFilterSpec{QuietSales: true}, Item{Title: "Corporate initial access", Body: "Gmail accounts mentioned"}, true},
		{"corporate RDP", AlertFilterSpec{QuietSales: true}, Item{Title: "Corporate RDP access accounts", Body: "Combo lists mentioned"}, true},
		{"corporate email access", AlertFilterSpec{QuietSales: true}, Item{Title: "Corporate email-access accounts", Body: "Outlook accounts mentioned"}, true},
		// The exact preset is a heuristic, not a sales classifier: no research exemption.
		{"combo research false positive", AlertFilterSpec{QuietSales: true}, Item{Title: "Research on combo list detection"}, false},
		{"account defense false positive", AlertFilterSpec{QuietSales: true}, Item{Title: "Protecting Gmail accounts"}, false},
		{"corporate title matching preset", AlertFilterSpec{QuietSales: true}, Item{Title: "Corporate Outlook accounts access"}, false},
		{"quiet exclusion beats include", AlertFilterSpec{QuietSales: true, Include: `Gmail`}, Item{Title: "Gmail accounts"}, false},
		{"custom title exclusion coexists", AlertFilterSpec{QuietSales: true, ExcludeTitle: `(?i)giveaway`}, Item{Title: "Giveaway announcement"}, false},
		{"quiet exclusion coexists", AlertFilterSpec{QuietSales: true, ExcludeTitle: `(?i)giveaway`}, Item{Title: "Combo sale"}, false},
		{"both exclusions allow signal", AlertFilterSpec{QuietSales: true, ExcludeTitle: `(?i)giveaway`}, Item{Title: "Corporate RDP access", Body: "Giveaway and combos discussed"}, true},
		{"custom body exclusion coexists", AlertFilterSpec{QuietSales: true, Include: `access`, Exclude: `giveaway`}, Item{Title: "Corporate access", Body: "giveaway"}, false},
		{"include still required", AlertFilterSpec{QuietSales: true, Include: `CVE-`}, Item{Title: "Corporate access"}, false},
		{"readable only still required", AlertFilterSpec{QuietSales: true, ReadableOnly: true}, Item{Title: "Corporate access", Access: "restricted", Body: "login required"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := compileAlertFilter(tc.spec)
			if err != nil {
				t.Fatal(err)
			}
			if got := f.matches(tc.item); got != tc.want {
				t.Fatalf("matches=%v want=%v", got, tc.want)
			}
		})
	}
	for _, spec := range []AlertFilterSpec{
		{QuietSales: true, Include: "["},
		{QuietSales: true, Exclude: "["},
		{QuietSales: true, ExcludeTitle: "["},
	} {
		if _, err := compileAlertFilter(spec); err == nil {
			t.Fatalf("quiet_sales hid invalid custom regex: %+v", spec)
		}
	}
}

func TestQuietSalesSparseJSON(t *testing.T) {
	for _, tc := range []struct {
		name, filterJSON, wantJSON string
		quietSales                 bool
	}{
		{"omitted", "", `{}`, false},
		{"empty", `,"alert_filter":{}`, `{}`, false},
		{"false", `,"alert_filter":{"quiet_sales":false}`, `{}`, false},
		{"true", `,"alert_filter":{"quiet_sales":true}`, `{"quiet_sales":true}`, true},
		{"legacy custom title", `,"alert_filter":{"exclude_title":"giveaway"}`, `{"exclude_title":"giveaway"}`, false},
		{"true with custom title", `,"alert_filter":{"quiet_sales":true,"exclude_title":"giveaway"}`, `{"exclude_title":"giveaway","quiet_sales":true}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var config Config
			data := `{"sources":[{"id":"forum","url":"https://example.com/","transport":"direct","format":"feed"` + tc.filterJSON + `}]}`
			if err := json.Unmarshal([]byte(data), &config); err != nil {
				t.Fatal(err)
			}
			if err := config.Validate(); err != nil {
				t.Fatal(err)
			}
			spec := config.Sources[0].AlertFilter
			if spec.QuietSales != tc.quietSales {
				t.Fatalf("quiet_sales=%v want=%v", spec.QuietSales, tc.quietSales)
			}
			encoded, err := json.Marshal(spec)
			if err != nil || string(encoded) != tc.wantJSON {
				t.Fatalf("marshal=%s want=%s err=%v", encoded, tc.wantJSON, err)
			}
			h, err := NewFilteredHub(config.Sources)
			if err != nil {
				t.Fatal(err)
			}
			if got := h.Matches(Item{Source: "forum", Title: "Combo sale"}); got == tc.quietSales {
				t.Fatalf("matches=%v quiet_sales=%v", got, tc.quietSales)
			}
		})
	}
	var spec AlertFilterSpec
	if err := json.Unmarshal([]byte(`{"quiet_sales":"true"}`), &spec); err == nil {
		t.Fatal("accepted non-boolean quiet_sales")
	}
}

func filteredCollector(t *testing.T) *Collector {
	t.Helper()
	c := testCollector(t)
	c.Config.Sources = []Source{{ID: "forum", Transport: "tor", AlertFilter: AlertFilterSpec{ExcludeTitle: `(?i)combo|accounts`}}}
	h, err := NewFilteredHub(c.Config.Sources)
	if err != nil {
		t.Fatal(err)
	}
	c.Hub = h
	return c
}

func TestQuietSalesSharedHubPolicy(t *testing.T) {
	c := filteredCollector(t)
	c.Config.Sources = []Source{
		{ID: "quiet", AlertFilter: AlertFilterSpec{QuietSales: true, ExcludeTitle: `giveaway`}},
		{ID: "legacy"},
	}
	var err error
	c.Hub, err = NewFilteredHub(c.Config.Sources)
	if err != nil {
		t.Fatal(err)
	}
	ch, cancel := c.Hub.Subscribe()
	defer cancel()
	for _, source := range c.Config.Sources {
		items, err := c.Store.Apply(source.ID, []Candidate{
			{Key: "sale", Title: "Gmail accounts " + source.ID, Body: "Sale details"},
			{Key: "custom", Title: "giveaway " + source.ID, Body: "Custom noise"},
			{Key: "signal", Title: "Corporate RDP email-access " + source.ID, Body: "Combo lists and Outlook accounts discussed"},
		}, Checkpoint{}, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range items {
			c.Hub.Publish(item)
		}
	}
	if got := len(c.Store.Items(0)); got != 6 {
		t.Fatalf("raw store has %d items, want 6", got)
	}
	if got := len(c.Hub.Items(c.Store, 0)); got != 4 {
		t.Fatalf("filtered snapshot has %d items, want 4", got)
	}
	// SSE subscribers and summary recovery use the same per-source policy.
	c.Hub.RestoreSnapshot(c.Store)
	for _, item := range c.Store.Items(0) {
		want := item.Source == "legacy" || strings.HasPrefix(item.Title, "Corporate")
		if c.Hub.Matches(item) != want || c.Hub.HasPublished(item.ID, item.Hash) != want {
			t.Fatalf("inconsistent delivery/recovery policy: %+v", item)
		}
	}
	for range 4 {
		select {
		case item := <-ch:
			if item.Source == "quiet" && !strings.HasPrefix(item.Title, "Corporate") {
				t.Fatalf("excluded item reached subscriber: %+v", item)
			}
		default:
			t.Fatal("missing matching item from subscriber")
		}
	}
	select {
	case item := <-ch:
		t.Fatalf("extra item reached subscriber: %+v", item)
	default:
	}
	handler := Handler(testToken, c.Store, c.Hub, c)
	for _, path := range []string{"/api/items", "/feed.xml"} {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+testToken)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		body := w.Body.String()
		if w.Code != 200 || strings.Contains(body, "Gmail accounts quiet") || strings.Contains(body, "giveaway quiet") || !strings.Contains(body, "Gmail accounts legacy") || !strings.Contains(body, "Corporate RDP email-access quiet") {
			t.Fatalf("bad %s snapshot: %s", path, body)
		}
	}
}

func TestFiltersRetainStateAndApplyBeforeSnapshotLimit(t *testing.T) {
	c := filteredCollector(t)
	now := time.Now().UTC()
	var candidates []Candidate
	for i := 0; i < 205; i++ {
		candidates = append(candidates, Candidate{Key: fmt.Sprint(i), Title: "Combo sale", Body: "noise"})
	}
	if _, err := c.Store.Apply("forum", []Candidate{{Key: "keep", Title: "Initial access claim", Body: "readable"}}, Checkpoint{}, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Store.Apply("forum", candidates, Checkpoint{Initialized: true}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Store.Apply("removed-source", []Candidate{{Key: "old", Title: "Old source"}}, Checkpoint{}, now); err != nil {
		t.Fatal(err)
	}
	handler := Handler(testToken, c.Store, c.Hub, c)
	for _, path := range []string{"/api/items", "/feed.xml"} {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+testToken)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "Initial access claim") || strings.Contains(w.Body.String(), "Combo sale") || strings.Contains(w.Body.String(), "Old source") {
			t.Fatalf("bad %s snapshot: %s", path, w.Body.String())
		}
	}
	if len(c.Store.Items(0)) != 207 {
		t.Fatal("filter discarded collected state")
	}
	status := c.Statuses()[0]
	if status.MatchingItems != 1 || status.FilteredItems != 205 {
		t.Fatalf("wrong saved counts: %+v", status)
	}
}

func TestFilteredEditSendsMetadataOnlyRemoval(t *testing.T) {
	c := filteredCollector(t)
	items, err := c.Store.Apply("forum", []Candidate{{Key: "post", Title: "Initial access claim", Body: "readable"}}, Checkpoint{}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	c.Hub.RestoreSnapshot(c.Store) // Also works with summaries disabled.
	ch, cancel := c.Hub.Subscribe()
	defer cancel()
	edit := items[0]
	edit.Title, edit.Body, edit.Summary, edit.Hash = "Combo sale", "private-noise", "private-excerpt", "edited"
	edit.Observed = edit.Observed.Add(time.Second)
	c.Hub.Publish(edit)
	select {
	case got := <-ch:
		if !got.Filtered || got.ID != edit.ID || got.Hash != edit.Hash || got.Kind != "filtered" || got.Title != "" || got.Body != "" || got.Summary != "" || got.URL != "" || got.Enrichment != nil {
			t.Fatalf("bad removal: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("no removal for previously visible item")
	}
	if c.Hub.HasPublished(edit.ID, edit.Hash) {
		t.Fatal("excluded edit eligible for summary recovery")
	}
	c.Hub.Publish(edit)
	select {
	case got := <-ch:
		t.Fatalf("repeated excluded revision emitted: %+v", got)
	default:
	}
	edit.Title = "Vulnerability research"
	c.Hub.Publish(edit)
	select {
	case got := <-ch:
		if got.Filtered || got.Title != edit.Title {
			t.Fatal("matching revision did not return")
		}
	default:
		t.Fatal("matching revision suppressed")
	}
}

type filterBlockedOutput struct {
	once    sync.Once
	started chan struct{}
	release chan struct{}
	capture *terminalCapture
}

func (w *filterBlockedOutput) Write(p []byte) (int, error) {
	w.once.Do(func() {
		close(w.started)
		<-w.release
	})
	return w.capture.Write(p)
}

func TestTerminalSlowClientRecoveryStillFiltersSavedNoise(t *testing.T) {
	c := filteredCollector(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	out := &filterBlockedOutput{started: make(chan struct{}), release: make(chan struct{}), capture: &terminalCapture{notify: make(chan struct{}, 64)}}
	var unblock sync.Once
	defer unblock.Do(func() { close(out.release) })
	done := RunTerminal(ctx, c.Store, c.Hub, out, true)
	now := time.Now().UTC()
	c.Hub.Publish(Item{ID: "first", Source: "forum", Title: "First alert", Hash: "1", Kind: "new", Observed: now})
	select {
	case <-out.started:
	case <-time.After(time.Second):
		t.Fatal("terminal did not begin writing")
	}
	var candidates []Candidate
	for i := 0; i < 40; i++ {
		candidates = append(candidates, Candidate{Key: fmt.Sprint(i), Title: fmt.Sprintf("Research %d", i)})
	}
	candidates = append(candidates, Candidate{Key: "noise", Title: "Combo accounts", Body: "excluded persisted text"}, Candidate{Key: "last", Title: "Last recovered alert"})
	items, err := c.Store.Apply("forum", candidates, Checkpoint{}, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		c.Hub.Publish(item)
	}
	unblock.Do(func() { close(out.release) })
	awaitTerminalText(t, out.capture, func(text string) bool {
		return strings.Contains(text, "[RECOVERY]") && strings.Contains(text, "Last recovered alert")
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.capture.String(), "Combo") || strings.Contains(out.capture.String(), "excluded persisted text") {
		t.Fatalf("noise leaked through durable reconciliation: %s", out.capture.String())
	}
}

func TestFilteredSummaryAndTerminalRecovery(t *testing.T) {
	c := filteredCollector(t)
	now := time.Now().UTC()
	if _, err := c.Store.Apply("forum", nil, Checkpoint{}, now); err != nil {
		t.Fatal(err)
	}
	items, err := c.Store.Apply("forum", []Candidate{
		{Key: "noise", Title: "Combo accounts", Body: "excluded text"},
		{Key: "signal", Title: "Initial access claim", Body: "Some useful public source text."},
	}, Checkpoint{Initialized: true}, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSummarizer(c.Store, c.Hub)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	out := &terminalCapture{notify: make(chan struct{}, 16)}
	done := RunTerminal(ctx, c.Store, c.Hub, out, true)
	for _, item := range items {
		s.Enqueue(item)
	}
	s.recover(ctx)
	c.Hub.Publish(Item{ID: "barrier", Source: "forum", Kind: "new", Title: "Barrier", Observed: now.Add(2 * time.Second)})
	awaitTerminalText(t, out, func(text string) bool { return strings.Contains(text, "Barrier") })
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "Combo") || strings.Contains(out.String(), "excluded text") || !strings.Contains(out.String(), "Some useful public source text.") {
		t.Fatalf("wrong recovery output: %s", out.String())
	}
	for _, item := range c.Store.Items(0) {
		if item.Title == "Combo accounts" && hasSummary(item) {
			t.Fatal("recovery summarized excluded noise")
		}
	}
	// Filter changes do not mutate source hashes, baselines, or checkpoints.
	before, _ := json.Marshal(c.Store.Items(0))
	unfiltered := NewHub()
	unfiltered.RestoreSnapshot(c.Store)
	after, _ := json.Marshal(c.Store.Items(0))
	if string(before) != string(after) {
		t.Fatal("delivery policy changed durable source observations")
	}
}
