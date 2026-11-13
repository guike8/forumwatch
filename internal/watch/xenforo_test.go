package watch

import (
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const xenForoFixture = `<html><title>Thread</title>
<h1 class="p-title-value"><span>News</span> Example thread</h1>
<article class="message" data-content="post-11" data-author="analyst">
<time itemprop="datePublished" datetime="2026-11-13T12:00:00+0000"></time>
<div class="bbWrapper"><p>First post &amp; text.</p><script>not evidence</script>
<noscript>fallback noise</noscript><template>hidden</template></div></article>
<article data-content="post-12" data-author="reply-author">
<time itemprop="datePublished" datetime="2026-11-13T12:01:00Z"></time>
<div class="bbWrapper">A reply.<br>Additional text.</div></article></html>`

func TestParseXenForoPosts(t *testing.T) {
	base, _ := url.Parse("http://forum.onion/threads/42/")
	posts, err := ParseXenForo(strings.NewReader(xenForoFixture), base)
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) != 2 {
		t.Fatalf("got %d posts", len(posts))
	}
	first := posts[0]
	if first.Key != "post-11" || first.Author != "analyst" || first.Body != "First post & text." || first.Summary != first.Body || first.Title != "News Example thread" {
		t.Fatalf("incorrect post: %+v", first)
	}
	if first.URL != "http://forum.onion/threads/42/#post-11" || first.Published != "2026-11-13T12:00:00+0000" {
		t.Fatalf("incorrect attribution: %+v", first)
	}
	if posts[1].Key != "post-12" || posts[1].Body != "A reply. Additional text." {
		t.Fatalf("incorrect reply: %+v", posts[1])
	}
}

func TestParseXenForoRejectsGatesAndIncompletePosts(t *testing.T) {
	base, _ := url.Parse("http://forum.onion/threads/42/")
	cases := []string{
		`<title>Sign In</title><input type="password">`,
		`<title>Browser check</title>Checking...`,
		`<h1 class="p-title-value">Empty index</h1>`,
		strings.Replace(xenForoFixture, `data-author="analyst"`, "", 1),
		strings.Replace(xenForoFixture, `datetime="2026-11-13T12:00:00+0000"`, `datetime="not-a-date"`, 1),
		strings.Replace(xenForoFixture, `class="bbWrapper"`, `class="changed-layout"`, 1),
	}
	for _, document := range cases {
		if posts, err := ParseXenForo(strings.NewReader(document), base); err == nil || posts != nil {
			t.Fatalf("accepted incomplete/gated page: %+v, %v", posts, err)
		}
	}
	if _, err := ParseXenForo(strings.NewReader(xenForoFixture), nil); err == nil {
		t.Fatal("accepted missing base URL")
	}
}

func TestXenForoKeepsFullBodyForLateEditDetection(t *testing.T) {
	base, _ := url.Parse("http://forum.onion/threads/42/")
	body := strings.Repeat("a", 1100)
	document := strings.Replace(xenForoFixture, "First post &amp; text.", body+"old ending", 1)
	posts, err := ParseXenForo(strings.NewReader(document), base)
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(posts[0].Summary)) != 1001 || !strings.HasSuffix(posts[0].Body, "old ending") {
		t.Fatal("full body lost or excerpt not truncated")
	}
	store, err := OpenStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	initial, err := store.Apply("forum", posts, Checkpoint{}, now)
	if err != nil || len(initial) != 2 || initial[0].Kind != "baseline" {
		t.Fatalf("baseline: %+v, %v", initial, err)
	}
	posts[0].Body = body + "new ending"
	changed, err := store.Apply("forum", posts, Checkpoint{}, now.Add(time.Second))
	if err != nil || len(changed) != 1 || changed[0].Kind != "updated" || changed[0].Author != "analyst" {
		t.Fatalf("missed late edit: %+v, %v", changed, err)
	}
	if changed[0].Summary != initial[0].Summary || changed[0].Hash == initial[0].Hash || !changed[0].FirstSeen.Equal(initial[0].FirstSeen) {
		t.Fatal("late edit identity/revision semantics are incorrect")
	}
	reopened, err := OpenStore(store.path)
	if err != nil || reopened.Items(0)[0].Body != posts[0].Body {
		t.Fatalf("body not persisted: %v", err)
	}
}

func TestStorePreservesLegacyHashesWithoutPostFields(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	candidate := Candidate{Key: "old", Title: "Old title", URL: "https://example.com/1", Summary: "old text"}
	items, err := store.Apply("legacy", []Candidate{candidate}, Checkpoint{}, time.Now().UTC())
	if err != nil || items[0].Hash != digest(candidate.Title, candidate.URL, candidate.Summary, candidate.Published, candidate.Updated) {
		t.Fatalf("legacy hash changed: %+v, %v", items, err)
	}
}
