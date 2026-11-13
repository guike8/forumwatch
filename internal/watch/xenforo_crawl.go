package watch

import (
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

var xenForoThreadPath = regexp.MustCompile(`^/threads/([^/]+?\.)?([0-9]+)(/|$)`)
var xenForoForumPath = regexp.MustCompile(`^/forums/([^/]+?\.)?([0-9]+)(/|$)`)
var xenForoPagePath = regexp.MustCompile(`/page-([0-9]+)/?$`)

// XenForoLinks contains only source-scoped navigation, not arbitrary post links.
type XenForoLinks struct {
	Threads []string
	Forums  []string
	Next    string
}

func xenForoThread(u *url.URL) string {
	match := xenForoThreadPath.FindStringSubmatch(u.Path)
	if len(match) == 0 {
		return ""
	}
	return match[2]
}

func crawlURL(base *url.URL, raw string) *url.URL {
	relative, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	u := base.ResolveReference(relative)
	if !browserFinalURL(base, u) {
		return nil
	}
	u.Fragment, u.RawFragment = "", ""
	// Keep only XenForo's page number. Never propagate session/tracking queries.
	query := u.Query()
	u.RawQuery = ""
	if page := query.Get("page"); page != "" {
		n, err := strconv.Atoi(page)
		if err != nil || n < 1 || n > 1000000 {
			return nil
		}
		u.RawQuery = "page=" + strconv.Itoa(n)
	}
	return u
}

func threadRoot(u *url.URL) string {
	match := xenForoThreadPath.FindStringIndex(u.Path)
	if match == nil {
		return ""
	}
	root := *u
	root.Path, root.RawPath = strings.TrimRight(u.Path[:match[1]], "/")+"/", ""
	root.RawQuery, root.Fragment = "", ""
	return root.String()
}

func pageNumber(u *url.URL) int {
	if match := xenForoPagePath.FindStringSubmatch(u.Path); len(match) > 0 {
		n, _ := strconv.Atoi(match[1])
		return n
	}
	n, _ := strconv.Atoi(u.Query().Get("page"))
	return max(1, n)
}

func navigationRoot(u *url.URL) string {
	path := xenForoPagePath.ReplaceAllString(u.Path, "/")
	return strings.TrimRight(path, "/")
}

// ParseXenForoLinks recognizes forum/category listings and forward pagination.
// Thread discovery is confined to listing titles, never links inside post text.
func ParseXenForoLinks(data io.Reader, base *url.URL, index bool) (XenForoLinks, error) {
	var result XenForoLinks
	if !webURL(base) || base.User != nil {
		return result, fmt.Errorf("invalid XenForo navigation URL")
	}
	root, err := html.Parse(data)
	if err != nil {
		return result, err
	}
	if gate := findElement(root, func(n *html.Node) bool {
		return n.Data == "input" && strings.EqualFold(attribute(n, "type"), "password")
	}); gate != nil {
		return result, fmt.Errorf("XenForo page requires authentication")
	}
	recognized := !index
	seen := map[string]bool{}
	var walk func(*html.Node, bool, bool)
	walk = func(n *html.Node, listing, navigation bool) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "noscript", "template":
				return
			}
			if hasClass(n, "node--forum") || hasClass(n, "structItemContainer") {
				recognized = true
			}
			listing = listing || hasClass(n, "structItem-title") || hasClass(n, "node-title") || hasClass(n, "node-extra-title")
			navigation = navigation || hasClass(n, "pageNav") || hasClass(n, "pageNavSimple")
			if n.Data == "a" {
				u := crawlURL(base, attribute(n, "href"))
				if u != nil && index && listing {
					if id := xenForoThread(u); id != "" {
						link := threadRoot(u)
						if !seen["t"+id] {
							result.Threads = append(result.Threads, link)
							seen["t"+id] = true
						}
					} else if xenForoForumPath.MatchString(u.Path) {
						u.Path = xenForoPagePath.ReplaceAllString(u.Path, "/")
						u.RawQuery = ""
						if !seen[u.String()] {
							result.Forums = append(result.Forums, u.String())
							seen[u.String()] = true
						}
					}
				}
				next := hasClass(n, "pageNav-jump--next") || hasClass(n, "pageNavSimple-el--next") || (navigation && strings.EqualFold(attribute(n, "rel"), "next"))
				if next && u != nil {
					if navigationRoot(u) != navigationRoot(base) || pageNumber(u) <= pageNumber(base) {
						err = fmt.Errorf("unsafe or non-forward XenForo pagination")
					} else {
						result.Next = u.String()
					}
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child, listing, navigation)
		}
	}
	walk(root, false, false)
	if err != nil {
		return XenForoLinks{}, err
	}
	if !recognized {
		return result, fmt.Errorf("no XenForo listing; check login, challenge, or layout")
	}
	if len(result.Threads) > 512 || len(result.Forums) > 64 {
		return XenForoLinks{}, fmt.Errorf("XenForo listing exceeds discovery bounds")
	}
	return result, nil
}
