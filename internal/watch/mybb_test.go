package watch

import (
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

func myBBTestURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return u
}

// Synthetic fragments retain only the observed layout, never private captures.
func myBBTestPost(id, author, body, stamp string) string {
	return `<div class="post " id="post_` + id + `"><div class="post__author"><div class="post__user-profile largetext"><a>` + author + `</a></div></div><div class="post_content"><div class="post_head">Yesterday, 01:34 PM</div><div class="post_body scaleimages" id="pid_` + id + `">` + body + `</div><div id="post_meta_` + id + `"><script>$(document).ready(function(){quick_quote(` + id + `,'` + author + `',` + stamp + `);});</script><script>quick_quote(` + id + `,'` + author + `',` + stamp + `);</script></div></div></div>`
}

func myBBTestPage(posts string) string {
	return `<html><head><title>A stable topic - DarkForums</title></head><body><div id="posts">` + posts + `</div></body></html>`
}

func myBBTestListing(links string) string {
	return `<table class="tborder clear"><tr class="inline_row"><td class="trow2 forumdisplay_regular">` + links + `</td></tr></table>`
}

func myBBTestSubject(id int, href string) string {
	return fmt.Sprintf(`<span class="subject_new" id="tid_%d"><a href="%s">Topic</a></span>`, id, href)
}

func TestMyBBPosts(t *testing.T) {
	body := "Visible text " + strings.Repeat("界", 1050) + `<script>not content</script><style>not content</style><template>not content</template>`
	post := myBBTestPost("42", "Alice", body, "1794470660")
	page := myBBTestPage(post + post + myBBTestPost("43", "Bob", "Another reply about login issues", "1794470700"))
	base := myBBTestURL("https://forum.example/Thread-Mixed-Case-Topic?page=3")
	items, err := ParseMyBB(strings.NewReader(page), base)
	if err != nil || len(items) != 2 {
		t.Fatalf("posts = %d, err = %v", len(items), err)
	}
	p := items[0]
	if p.Key != "post-42" || p.PostID != "post-42" || p.Title != "A stable topic" || p.Author != "Alice" || p.Published != "2026-11-12T08:04:20Z" {
		t.Fatalf("unexpected metadata: %+v", p)
	}
	if p.URL != "https://forum.example/Thread-Mixed-Case-Topic?pid=42#pid42" || p.Body != "Visible text "+strings.Repeat("界", 1050) || p.Summary != truncate(p.Body, 1000) || p.Updated != "" {
		t.Fatalf("unexpected content/permalink: %+v", p)
	}
	if items[1].Body != "Another reply about login issues" || items[1].Access != "" {
		t.Fatalf("legitimate login discussion was filtered: %+v", items[1])
	}
	// Relative DOM dates and the page URL must not manufacture post revisions.
	later := strings.ReplaceAll(page, "Yesterday, 01:34 PM", "3 days ago")
	again, err := ParseMyBB(strings.NewReader(later), myBBTestURL("https://forum.example/Thread-Mixed-Case-Topic?page=4"))
	if err != nil || !reflect.DeepEqual(items, again) {
		t.Fatalf("volatile dates/page changed posts: %v", err)
	}
}

func TestMyBBBodyAccess(t *testing.T) {
	for _, tc := range []struct {
		name, body, wantBody, access string
		hidden                       int
	}{
		{"partial", `Public context. <a>Link hidden, please Sign in or Sign up</a>`, "Public context.", "partial", 1},
		{"restricted", `<span>Link hidden, please Sign in or Sign up</span>`, "", "restricted", 1},
		{"repeated", `Link hidden, please Sign in or Sign up Link hidden, please Sign in or Sign up`, "", "restricted", 2},
		{"discussion", `Please sign in to reproduce the login error.`, "Please sign in to reproduce the login error.", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			posts, err := ParseMyBB(strings.NewReader(myBBTestPage(myBBTestPost("42", "Alice", tc.body, "1794470660"))), myBBTestURL("https://forum.example/Thread-Topic"))
			if err != nil || len(posts) != 1 {
				t.Fatalf("posts=%v, err=%v", posts, err)
			}
			p := posts[0]
			if p.Body != tc.wantBody || p.Summary != truncate(tc.wantBody, 1000) || p.Access != tc.access || p.HiddenLinks != tc.hidden {
				t.Fatalf("unexpected restriction handling: %+v", p)
			}
		})
	}
}

func TestMyBBRejectsMalformedPostsAndGates(t *testing.T) {
	valid := myBBTestPost("42", "Alice", "Real post body", "1794470660")
	for name, page := range map[string]string{
		"no posts":                   `<title>A topic - DarkForums</title><p>Unavailable</p>`,
		"login":                      `<title>Login - DarkForums</title><form><input type="password"></form>`,
		"challenge":                  `<title>Just a moment...</title><form id="challenge-form"></form>`,
		"challenge posts":            `<title>Just a moment...</title>` + valid,
		"no title":                   valid,
		"empty title":                `<title> - DarkForums</title>` + valid,
		"bad post id":                myBBTestPage(strings.Replace(valid, `id="post_42"`, `id="post_bad"`, 1)),
		"wrong body id":              myBBTestPage(strings.Replace(valid, `id="pid_42"`, `id="pid_43"`, 1)),
		"missing author":             myBBTestPage(myBBTestPost("42", "", "Body", "1794470660")),
		"empty body":                 myBBTestPage(myBBTestPost("42", "Alice", " <script>not content</script>", "1794470660")),
		"no timestamp":               myBBTestPage(strings.ReplaceAll(valid, "quick_quote", "other_function")),
		"wrong timestamp id":         myBBTestPage(strings.ReplaceAll(valid, "quick_quote(42,", "quick_quote(43,")),
		"milliseconds":               myBBTestPage(myBBTestPost("42", "Alice", "Body", "1794470660000")),
		"overflow":                   myBBTestPage(myBBTestPost("42", "Alice", "Body", "999999999999999999999999")),
		"old timestamp":              myBBTestPage(myBBTestPost("42", "Alice", "Body", "1")),
		"future timestamp":           myBBTestPage(myBBTestPost("42", "Alice", "Body", fmt.Sprint(time.Now().Add(72*time.Hour).Unix()))),
		"conflicting time":           myBBTestPage(strings.Replace(valid, `<script>quick_quote(42,'Alice',1794470660);`, `<script>quick_quote(42,'Alice',1794470661);`, 1)),
		"script outside post":        myBBTestPage(strings.ReplaceAll(valid, "quick_quote", "other_function")) + `<script>quick_quote(42,'Alice',1794470660);</script>`,
		"timestamp inside body only": myBBTestPage(myBBTestPost("42", "Alice", `Body<script>quick_quote(42,'Alice',1794470660);</script>`, "not_a_number")),
		"inert posts":                myBBTestPage(`<template>` + valid + `</template>`),
		"nested metadata":            myBBTestPage(`<div class="post" id="post_41">` + valid + `</div>`),
	} {
		t.Run(name, func(t *testing.T) {
			if posts, err := ParseMyBB(strings.NewReader(page), myBBTestURL("https://forum.example/Thread-Topic")); err == nil {
				t.Fatalf("accepted malformed page: %+v", posts)
			}
		})
	}
	// Login widgets in navigation do not imply that the public posts are gated.
	page := myBBTestPage(valid) + `<nav><form action="member.php"><input type="password"><input name="action" value="do_login"></form></nav>`
	if _, err := ParseMyBB(strings.NewReader(page), myBBTestURL("https://forum.example/Thread-Topic")); err != nil {
		t.Fatalf("public thread rejected because of nav widget: %v", err)
	}
}

func TestMyBBFriendlyListing(t *testing.T) {
	page := `<nav><a href="/Thread-Nav">Nav</a><a href="/Forum-Nav">Nav</a></nav>` +
		myBBTestListing(myBBTestSubject(1, "/Thread-Mixed-Case-Topic?page=3")+myBBTestSubject(1, "/Thread-Mixed-Case-Topic")+`<span class="subject_old" id="tid_2"><a href="/Thread-Second">Second</a></span><a href="/Thread-Last?action=lastpost">Last</a><div class="advertisement"><a href="/Thread-Ad">Ad</a></div>`) +
		`<table class="tborder"><thead><tr><td class="thead"><div><span class="thead__icon"><a href="/Forum-Category">icon</a></span><strong><a href="/Forum-Category">Category</a></strong></div></td></tr></thead><tbody><tr><td class="trow1"><a class="forums__forum-name" href="/Forum-Announcements">Announcements</a><div class="forums__forum-description"><a href="/Thread-Description">Description</a></div></td></tr></tbody></table>` +
		`<div class="post" id="post_42"><div class="post_body"><a href="/Thread-Body">Body</a>` + myBBTestListing(myBBTestSubject(90, "/Thread-Fake-Listing-In-Body")) + `</div></div>` +
		`<div class="pagination"><a class="pagination_next" href="?page=2">Next</a></div><div class="pagination"><a class="pagination_next" href="?page=2">Next</a></div>` +
		`<a href="?action=nextoldest">Next Oldest</a><a href="?action=nextnewest">Next Newest</a><a class="pagination_next" href="/Forum-Wrong?page=9">Outside pagination</a>`
	links, err := ParseMyBBLinks(strings.NewReader(page), myBBTestURL("https://forum.example/Forum-Market"), true)
	want := XenForoLinks{Threads: []string{"https://forum.example/Thread-Mixed-Case-Topic", "https://forum.example/Thread-Second"}, Forums: []string{"https://forum.example/Forum-Category", "https://forum.example/Forum-Announcements"}, Next: "https://forum.example/Forum-Market?page=2"}
	if err != nil || !reflect.DeepEqual(links, want) {
		t.Fatalf("links=%+v want=%+v err=%v", links, want, err)
	}
}

func TestMyBBNavigationSecurity(t *testing.T) {
	badLinks := []string{
		"https://foreign.example/Thread-Topic", "//foreign.example/Thread-Topic",
		"https://user:password@forum.example/Thread-Topic", "http://forum.example/Thread-Topic",
		"https://forum.example:444/Thread-Topic", "javascript:alert(1)",
		"/showthread.php?tid=42", "/forumdisplay.php?fid=42", "/thread-Topic", "/Thread-Topic/page-2", "/Thread-Topic/extra",
		"/Thread-Topic?action=lastpost", "/Thread-Topic?tid=42", "/Forum-Topic?fid=42",
		"/Thread-Topic?page=0", "/Thread-Topic?page=-1", "/Thread-Topic?page=1000001", "/Thread-Topic?page=abc",
		"/Thread-Topic?page=2&page=3", "/Thread-Topic?page=", "/Thread-Topic?page=2&sid=secret", "/Thread-Topic?page=%zz",
	}
	for _, link := range badLinks {
		t.Run(link, func(t *testing.T) {
			links, err := ParseMyBBLinks(strings.NewReader(myBBTestListing(myBBTestSubject(1, link))), myBBTestURL("https://forum.example/Forum-Market"), true)
			if err != nil || len(links.Threads)+len(links.Forums) != 0 {
				t.Fatalf("unsafe discovery link: %+v, %v", links, err)
			}
		})
	}
	for _, next := range []string{"?page=2", "?page=1", "?page=0", "/Forum-Other?page=4", "/Thread-Other?page=4", "?action=nextoldest", "?action=nextnewest", "?page=4&tid=42", "https://foreign.example/Forum-Market?page=4", "https://user@forum.example/Forum-Market?page=4", "http://forum.example/Forum-Market?page=4"} {
		t.Run("pagination "+next, func(t *testing.T) {
			page := myBBTestListing(myBBTestSubject(1, "/Thread-Topic")) + `<div class="pagination"><a class="pagination_next" href="` + next + `">Next</a></div>`
			if _, err := ParseMyBBLinks(strings.NewReader(page), myBBTestURL("https://forum.example/Forum-Market?page=2"), true); err == nil {
				t.Fatal("accepted unsafe/non-forward pagination")
			}
		})
	}
}

func TestMyBBThreadNavigationAndGates(t *testing.T) {
	base := myBBTestURL("https://forum.example/Thread-Topic?page=2")
	page := myBBTestPage(myBBTestPost("42", "Alice", `<a href="/Thread-Body">Text</a>`, "1794470660")) + `<div class="pagination"><a class="pagination_next" href="?page=3">Next</a></div><a href="?action=nextnewest">Next Newest</a>`
	links, err := ParseMyBBLinks(strings.NewReader(page), base, false)
	if err != nil || links.Next != "https://forum.example/Thread-Topic?page=3" || len(links.Threads)+len(links.Forums) != 0 {
		t.Fatalf("thread navigation = %+v, %v", links, err)
	}
	for _, index := range []bool{false, true} {
		for _, page := range []string{`<title>Login - DarkForums</title><input type="password">`, `<title>DarkForums - Login</title><table class="tborder"><tr><td class="trow1"><input type="password"></td></tr></table>`, `<title>DarkForums</title><table class="tborder"><tr><td class="trow1"><input type="password"></td></tr></table>`, `<title>Just a moment...</title>`, `<nav><a href="/Thread-Topic">Not a listing</a></nav>`, `<div class="pagination"><a class="pagination_next" href="?page=3">Next</a></div>`} {
			if _, err := ParseMyBBLinks(strings.NewReader(page), base, index); err == nil {
				t.Fatalf("accepted no-content/gate page, index=%v", index)
			}
		}
	}
	page = myBBTestListing(myBBTestSubject(1, "/Thread-Topic")) + `<div class="pagination"><a class="pagination_next" href="?page=3">Next</a><a class="pagination_next" href="?page=4">Next</a></div>`
	if _, err := ParseMyBBLinks(strings.NewReader(page), myBBTestURL("https://forum.example/Forum-Market"), true); err == nil {
		t.Fatal("accepted conflicting next links")
	}
}

func TestMyBBDiscoveryBounds(t *testing.T) {
	for _, tc := range []struct {
		name  string
		count int
		forum bool
	}{
		{"threads max", 512, false}, {"threads overflow", 513, false},
		{"forums max", 64, true}, {"forums overflow", 65, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var page strings.Builder
			for i := 1; i <= tc.count; i++ {
				if tc.forum {
					fmt.Fprintf(&page, `<table class="tborder"><tr><td class="trow1"><a class="forums__forum-name" href="/Forum-Category-%d">Forum</a></td></tr></table>`, i)
				} else {
					page.WriteString(myBBTestListing(myBBTestSubject(i, fmt.Sprintf("/Thread-Topic-%d", i))))
				}
			}
			_, err := ParseMyBBLinks(strings.NewReader(page.String()), myBBTestURL("https://forum.example/"), true)
			if wantError := tc.count == 513 || tc.count == 65; (err != nil) != wantError {
				t.Fatalf("err=%v, want error=%v", err, wantError)
			}
		})
	}
}

func TestMyBBFriendlyIdentityAndUnsupportedRouting(t *testing.T) {
	base := myBBTestURL("https://forum.example/Thread-MiXeD--123?page=004#pid42")
	if myBBThread(base) != "/Thread-MiXeD--123" || myBBRoot(base) != "https://forum.example/Thread-MiXeD--123" {
		t.Fatal("friendly path identity was not preserved")
	}
	for _, raw := range []string{
		"https://forum.example/showthread.php?tid=42", "https://forum.example/forumdisplay.php?fid=42",
		"https://forum.example/Thread-Topic?tid=42", "https://forum.example/Thread-Topic?action=nextnewest",
		"https://forum.example/Thread-Topic?page=2&fid=42", "https://forum.example/Thread-Topic?page=2&page=3",
		"https://forum.example/thread-Topic", "https://forum.example/Thread-Topic/", "https://forum.example/Thread-Topic%2Fextra", "https://user@forum.example/Thread-Topic",
	} {
		u := myBBTestURL(raw)
		if myBBThread(u) != "" || myBBRoot(u) != "" {
			t.Fatalf("accepted unsupported identity: %s", raw)
		}
		if _, err := ParseMyBB(strings.NewReader(myBBTestPage(myBBTestPost("42", "Alice", "Body", "1794470660"))), u); err == nil {
			t.Fatalf("accepted unsupported thread routing: %s", raw)
		}
		if _, err := ParseMyBBLinks(strings.NewReader(myBBTestListing(myBBTestSubject(1, "/Thread-Topic"))), u, true); err == nil {
			t.Fatalf("accepted unsupported navigation routing: %s", raw)
		}
	}
	if myBBThread(nil) != "" || myBBRoot(nil) != "" {
		t.Fatal("accepted nil URL")
	}
}
