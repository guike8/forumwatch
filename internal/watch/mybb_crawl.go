package watch

import (
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

var myBBFriendlyPath = regexp.MustCompile(`^/(Thread|Forum)-[^/\s?#]+$`)
var myBBSubjectID = regexp.MustCompile(`^tid_[1-9][0-9]*$`)

// Validate routing before crawlURL discards query parameters. In particular,
// showthread.php?tid=N and forumdisplay.php?fid=N cannot be normalized safely by
// dropping their queries. This adapter deliberately supports neither those nor
// action=lastpost/newpost/nextoldest/nextnewest links.
func myBBNavigationURL(u *url.URL) bool {
	if !webURL(u) || u.User != nil || u.Opaque != "" {
		return false
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return false
	}
	for key, values := range query {
		if key != "page" || len(values) != 1 || values[0] == "" {
			return false
		}
		for _, r := range values[0] {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	// Reuse shared origin and numeric-page bounds, after checking that no
	// unsupported routing would be erased by its sanitizer.
	return crawlURL(u, u.String()) != nil
}

// myBBThread is a case-sensitive friendly path identity, not a numeric MyBB tid.
// A slug may contain a number but we cannot infer its numeric ID from that.
func myBBThread(u *url.URL) string {
	if !myBBNavigationURL(u) || !myBBFriendlyPath.MatchString(u.Path) || !strings.HasPrefix(u.Path, "/Thread-") {
		return ""
	}
	return u.Path
}

func myBBForum(u *url.URL) bool {
	return myBBNavigationURL(u) && myBBFriendlyPath.MatchString(u.Path) && strings.HasPrefix(u.Path, "/Forum-")
}

func myBBRoot(u *url.URL) string {
	if myBBThread(u) == "" && !myBBForum(u) {
		return ""
	}
	root := *u
	root.RawQuery, root.Fragment, root.RawFragment, root.ForceQuery = "", "", "", false
	return root.String()
}

func myBBCrawlURL(base *url.URL, raw string) *url.URL {
	relative, err := url.Parse(raw)
	if err != nil || strings.TrimSpace(raw) == "" {
		return nil
	}
	resolved := base.ResolveReference(relative)
	if !myBBNavigationURL(resolved) {
		return nil
	}
	return crawlURL(base, raw)
}

// ParseMyBBLinks discovers only the captured listing title/category containers
// and forward pagination. Post bodies, global navigation and last-post widgets
// are not discovery sources.
func ParseMyBBLinks(data io.Reader, base *url.URL, index bool) (XenForoLinks, error) {
	var result XenForoLinks
	if !myBBNavigationURL(base) || (myBBThread(base) == "" && !myBBForum(base) && (!index || (base.Path != "/" && base.Path != "/index.php" && base.Path != ""))) {
		return result, fmt.Errorf("invalid or unsupported MyBB navigation URL")
	}
	root, err := html.Parse(data)
	if err != nil {
		return result, err
	}
	if myBBGate(root) {
		return result, fmt.Errorf("MyBB login or challenge page")
	}
	seen := map[string]bool{}
	recognized := false
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if err != nil || myBBInert(n) {
			return
		}
		if n.Type == html.ElementNode {
			if hasClass(n, "post") || hasClass(n, "post_body") {
				if !index && n.Data == "div" && hasClass(n, "post") && myBBPostID.MatchString(attribute(n, "id")) {
					recognized = true
				}
				return
			}
			if index && ((n.Data == "td" && hasClass(n, "forumdisplay_regular") && myBBListingContainer(n)) || (n.Data == "a" && myBBListingTitle(n))) {
				recognized = true
			}
			if n.Data == "a" {
				if index && myBBListingTitle(n) {
					u := myBBCrawlURL(base, attribute(n, "href"))
					if u != nil {
						link := myBBRoot(u)
						if link != "" && !seen[link] {
							if myBBThread(u) != "" {
								result.Threads = append(result.Threads, link)
							} else {
								result.Forums = append(result.Forums, link)
							}
							seen[link] = true
						}
					}
					if len(result.Threads) > 512 || len(result.Forums) > 64 {
						err = fmt.Errorf("MyBB listing exceeds discovery bounds")
						return
					}
				}
				if hasClass(n, "pagination_next") && myBBAncestor(n, func(a *html.Node) bool { return a.Data == "div" && hasClass(a, "pagination") }) != nil {
					u := myBBCrawlURL(base, attribute(n, "href"))
					if u == nil || u.Path != base.Path || (myBBThread(u) == "" && !myBBForum(u)) || pageNumber(u) <= pageNumber(base) {
						err = fmt.Errorf("unsafe or non-forward MyBB pagination")
						return
					}
					if result.Next != "" && result.Next != u.String() {
						err = fmt.Errorf("conflicting MyBB pagination")
						return
					}
					result.Next = u.String()
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	if err != nil {
		return XenForoLinks{}, err
	}
	if !recognized {
		return XenForoLinks{}, fmt.Errorf("no MyBB listing or posts; check login, challenge, or layout")
	}
	return result, nil
}

func myBBAncestor(n *html.Node, match func(*html.Node) bool) *html.Node {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type == html.ElementNode && match(p) {
			return p
		}
	}
	return nil
}

func myBBListingContainer(n *html.Node) bool {
	if n.Data != "td" || !(hasClass(n, "forumdisplay_regular") || hasClass(n, "thead") || hasClass(n, "trow1") || hasClass(n, "trow2")) {
		return false
	}
	return myBBAncestor(n, func(p *html.Node) bool { return p.Data == "table" && hasClass(p, "tborder") }) != nil
}

func myBBListingTitle(a *html.Node) bool {
	// Forum names are explicit title anchors, unlike the adjacent last-post,
	// forum description, statistics, breadcrumb or advertisement links.
	if hasClass(a, "forums__forum-name") {
		td := myBBAncestor(a, func(p *html.Node) bool { return p.Data == "td" })
		return td != nil && (hasClass(td, "trow1") || hasClass(td, "trow2")) && myBBListingContainer(td)
	}
	if parent := a.Parent; parent != nil && parent.Data == "span" && myBBSubjectID.MatchString(attribute(parent, "id")) && (hasClass(parent, "subject_new") || hasClass(parent, "subject_old") || hasClass(parent, "subject_editable")) {
		td := myBBAncestor(parent, func(p *html.Node) bool { return p.Data == "td" })
		return td != nil && hasClass(td, "forumdisplay_regular") && myBBListingContainer(td)
	}
	// Captured category headers: table.tborder > thead > tr > td.thead
	// > div > strong > a. Do not expand this to arbitrary .thead anchors.
	if parent := a.Parent; parent != nil && parent.Data == "strong" && parent.Parent != nil && parent.Parent.Data == "div" {
		td := parent.Parent.Parent
		return td != nil && td.Data == "td" && hasClass(td, "thead") && myBBListingContainer(td) && myBBAncestor(td, func(p *html.Node) bool { return p.Data == "thead" }) != nil
	}
	return false
}
