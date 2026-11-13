package watch

import (
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func TestConfigRejectsUnsafeOrAmbiguousSources(t *testing.T) {
	cases := []Source{
		{ID: "x", URL: "http://hidden.onion/", Transport: "direct", Format: "feed"},
		{ID: "x", URL: "http://hidden。onion/", Transport: "direct", Format: "feed"},
		{ID: "x", URL: "https://example.com/", Format: "feed"},
		{ID: "x", URL: "https://example.com/", Transport: "direct", Format: "html"},
		{ID: "x", URL: "file:///etc/passwd", Transport: "direct", Format: "feed"},
		{ID: "x", URL: "https://example.com/", Transport: "direct", Format: "feed", IntervalSeconds: 1},
	}
	for _, source := range cases {
		c := Config{Sources: []Source{source}}
		if err := c.Validate(); err == nil {
			t.Errorf("accepted unsafe/ambiguous source: %+v", source)
		}
	}
	c := Config{Sources: []Source{{ID: "x", URL: "http://hidden.onion/", Transport: "tor", Format: "feed"}}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.Listen != "127.0.0.1:8080" || c.Sources[0].IntervalSeconds != 60 {
		t.Fatal("unexpected defaults")
	}
}

func TestParseRSSAndAtom(t *testing.T) {
	base, _ := url.Parse("https://example.com/latest")
	cases := []struct {
		name, body, key, title, link, summary string
	}{
		{"rss", `<rss version="2.0"><channel><item><guid>post-1</guid><title>A &amp; B</title><link>/threads/1</link><description>&lt;b&gt;details&lt;/b&gt;&lt;script&gt;bad()&lt;/script&gt;</description></item></channel></rss>`, "post-1", "A & B", "https://example.com/threads/1", "details"},
		{"atom", `<feed xmlns="http://www.w3.org/2005/Atom"><entry><id>urn:post:2</id><title>New post</title><link rel="self" href="/api/2"/><link rel="alternate" href="/threads/2"/><summary>Some details</summary><updated>2026-11-13T00:00:00Z</updated></entry></feed>`, "urn:post:2", "New post", "https://example.com/threads/2", "Some details"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items, err := ParseFeed(strings.NewReader(tc.body), base)
			if err != nil {
				t.Fatal(err)
			}
			if len(items) != 1 {
				t.Fatalf("got %d items", len(items))
			}
			item := items[0]
			if item.Key != tc.key || item.Title != tc.title || item.URL != tc.link || item.Summary != tc.summary {
				t.Fatalf("unexpected entry: %+v", item)
			}
		})
	}
}

func TestFeedRejectsLoginPagesAndUnsafeLinks(t *testing.T) {
	base, _ := url.Parse("https://example.com/")
	if _, err := ParseFeed(strings.NewReader(`<html><body>Login</body></html>`), base); err == nil {
		t.Fatal("accepted login HTML as a feed")
	}
	items, err := ParseFeed(strings.NewReader(`<rss><channel><item><title>X</title><link>javascript:alert(1)</link></item></channel></rss>`), base)
	if err != nil || len(items) != 0 {
		t.Fatalf("unsafe link accepted: %+v, %v", items, err)
	}
}

func TestAtomBaseAndTextConstructs(t *testing.T) {
	base, _ := url.Parse("https://example.com/feed.xml")
	body := `<feed xmlns="http://www.w3.org/2005/Atom" xml:base="/forum/">
	<entry xml:base="topics/"><id>1</id><title type="xhtml"><div xmlns="http://www.w3.org/1999/xhtml">Hello <b>world</b></div></title><link xml:base="../posts/" href="1"/><summary type="text">Use &lt;endpoint&gt; literally</summary></entry>
	<entry><id>2</id><title type="text">Use &lt;tag&gt;</title><link href="2"/></entry></feed>`
	items, err := ParseFeed(strings.NewReader(body), base)
	if err != nil || len(items) != 2 {
		t.Fatalf("parse: %+v %v", items, err)
	}
	if items[0].Title != "Hello world" || items[0].URL != "https://example.com/forum/posts/1" || items[0].Summary != "Use <endpoint> literally" {
		t.Fatalf("lost Atom semantics: %+v", items[0])
	}
	if items[1].Title != "Use <tag>" || items[1].URL != "https://example.com/forum/2" {
		t.Fatalf("lost Atom text/base: %+v", items[1])
	}
}

func TestDigestDistinguishesPartsAndValues(t *testing.T) {
	if digest("a", "bc") == digest("ab", "c") || digest("a") == digest("b") || digest("a") != digest("a") {
		t.Fatal("digest collision or instability")
	}
}

func TestHTMLLinks(t *testing.T) {
	base, _ := url.Parse("https://example.com/latest")
	body := `<a href="/threads/1">First <b>thread</b></a><a href="/threads/1">Duplicate</a><a href="/login">Login</a><a href="https://evil.example/threads/2">External</a><a href="javascript:alert(1)">Bad</a>`
	items, err := ParseHTML(strings.NewReader(body), base, regexp.MustCompile(`/threads/`))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Title != "First thread" {
		t.Fatalf("unexpected HTML links: %+v", items)
	}
	if _, err := ParseHTML(strings.NewReader(`<html>Challenge</html>`), base, regexp.MustCompile(`/threads/`)); err == nil {
		t.Fatal("silent success on a challenge page")
	}
}
