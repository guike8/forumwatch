package watch

import (
	"bytes"
	"context"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const hiddenLinkFixture = `<span class="hidden-link">Link hidden, please <a href="/login/">Sign in</a> or <a href="/register/">Sign up</a></span>`

func TestXenForoPostAccess(t *testing.T) {
	base, _ := url.Parse("http://forum.onion/threads/42/")
	for _, tc := range []struct {
		name, body, wantBody, access string
		hidden                       int
	}{
		{"mixed", "Public discussion. " + hiddenLinkFixture + " More public text.", "Public discussion. More public text.", "partial", 1},
		{"blocked", hiddenLinkFixture + " / " + hiddenLinkFixture, "", "restricted", 2},
		{"short public text", "See here: " + hiddenLinkFixture, "See here:", "partial", 1},
		{"ordinary login discussion", "Please sign in to test the authentication fix. Sign up is optional.", "Please sign in to test the authentication fix. Sign up is optional.", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			document := strings.Replace(xenForoFixture, "First post &amp; text.", tc.body, 1)
			posts, err := ParseXenForo(strings.NewReader(document), base)
			if err != nil || len(posts) != 2 {
				t.Fatalf("parse: %+v, %v", posts, err)
			}
			post := posts[0]
			if post.Body != tc.wantBody || post.Access != tc.access || post.HiddenLinks != tc.hidden || post.Summary != tc.wantBody {
				t.Fatalf("post: %+v", post)
			}
			store, err := OpenStore(filepath.Join(t.TempDir(), "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			items, err := store.Apply("forum", posts, Checkpoint{}, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			hub := NewHub()
			summarizer, err := NewSummarizer(store, hub)
			if err != nil {
				t.Fatal(err)
			}
			summarizer.Enqueue(items[0])
			if tc.access == "restricted" && len(summarizer.queue) != 0 {
				t.Fatal("restricted post queued for summarization")
			}
			summarizer.recover(context.Background())
			var saved Item
			for _, item := range store.Items(0) {
				if item.PostID == "post-11" {
					saved = item
				}
			}
			var output bytes.Buffer
			if err := WriteTerminalItem(&output, saved); err != nil {
				t.Fatal(err)
			}
			if err := WriteTerminalSummary(&output, saved); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(output.String(), "Link hidden, please") {
				t.Fatal("placeholder leaked into terminal/extract")
			}
			if tc.access == "restricted" {
				if hasSummary(saved) || !strings.Contains(output.String(), "[RESTRICTED/BASELINE]") || strings.Contains(output.String(), "[SUMMARY]") {
					t.Fatalf("blocked body treated as content: %s", output.String())
				}
				if _, ok := store.summaryInput(saved.ID, saved.Hash); ok {
					t.Fatal("restricted post fell back to title for summary")
				}
			} else if !hasSummary(saved) {
				t.Fatal("readable body not summarized")
			}
			if tc.access == "partial" && strings.Count(output.String(), "Access: PARTIAL") != 2 {
				t.Fatalf("missing access notice on initial alert/extract: %s", output.String())
			}
			counts := store.contentCoverage("forum")
			if counts.PublicTextPosts+counts.PartialPosts+counts.RestrictedPosts != 2 || (tc.access == "restricted" && counts.RestrictedPosts != 1) || (tc.access == "partial" && counts.PartialPosts != 1) {
				t.Fatalf("coverage: %+v", counts)
			}
		})
	}
}

func TestLegacyAccessCorrectionPreservesCheckpointAndNoReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	legacy := Candidate{Key: "post-11", PostID: "post-11", Author: "author", Title: "Thread", URL: "http://forum.onion/threads/42/#post-11", Published: "2026-11-13T12:00:00Z",
		Body: "Link hidden, please Sign in or Sign up", Summary: "Link hidden, please Sign in or Sign up"}
	checkpoint := Checkpoint{Crawl: &CrawlState{SeedURL: "http://forum.onion/threads/42/", Format: "xenforo", Threads: map[string]CrawlThread{"42": {URL: "http://forum.onion/threads/42/", Ready: true}}}}
	now := time.Now().UTC()
	items, err := store.Apply("forum", []Candidate{legacy}, checkpoint, now)
	if err != nil {
		t.Fatal(err)
	}
	hub := NewHub()
	hub.Publish(items[0])
	if err := store.saveEnrichment(context.Background(), items[0].ID, Enrichment{Method: "local/extractive", Text: legacy.Body, SourceHash: items[0].Hash}, hub); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	hub = NewHub()
	summarizer, err := NewSummarizer(store, hub)
	if err != nil {
		t.Fatal(err)
	}
	summarizer.recover(context.Background())
	fixed := store.Items(0)[0]
	if fixed.Access != "restricted" || fixed.Body != "" || fixed.Summary != "" || fixed.Hash == items[0].Hash || fixed.Enrichment != nil || fixed.Kind != items[0].Kind || !fixed.Observed.Equal(now) || !fixed.FirstSeen.Equal(now) {
		t.Fatalf("incorrect repair: %+v", fixed)
	}
	cp := store.Checkpoint("forum")
	if !cp.Initialized || cp.Crawl.SeedURL != checkpoint.Crawl.SeedURL || !cp.Crawl.Threads["42"].Ready {
		t.Fatal("repair reset crawl/baseline")
	}
	legacy.Body, legacy.Summary, legacy.Access, legacy.HiddenLinks = "", "", "restricted", 1
	changed, err := store.ApplyPage("forum", []Candidate{legacy}, cp, now.Add(time.Second), false)
	if err != nil || len(changed) != 0 {
		t.Fatalf("repair replayed as update: %+v, %v", changed, err)
	}
	reopened, err := OpenStore(path)
	if err != nil || reopened.Items(0)[0].Hash != fixed.Hash {
		t.Fatalf("repair not restart-stable: %v", err)
	}
	legacy.Body, legacy.Summary, legacy.Access, legacy.HiddenLinks = "Now public.", "Now public.", "", 0
	changed, err = store.ApplyPage("forum", []Candidate{legacy}, cp, now.Add(2*time.Second), false)
	if err != nil || len(changed) != 1 || changed[0].Kind != "updated" || changed[0].Access != "" {
		t.Fatalf("public recovery missed: %+v, %v", changed, err)
	}
}

func TestAccessMetadataChangesRevision(t *testing.T) {
	post := Candidate{Key: "post-1", Body: "Public text", Author: "author", Access: "partial", HiddenLinks: 1}
	original := candidateHash(post)
	post.HiddenLinks = 2
	if candidateHash(post) == original {
		t.Fatal("hidden-link count change ignored")
	}
	post.HiddenLinks, post.Access = 0, ""
	if candidateHash(post) == original {
		t.Fatal("access change ignored")
	}
}
