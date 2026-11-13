package watch

import (
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"

	"golang.org/x/net/html"
)

var xenForoPostID = regexp.MustCompile(`^post-[0-9]+$`)

// ParseXenForo reads the posts on one thread page, not its index or history.
func ParseXenForo(data io.Reader, base *url.URL) ([]Candidate, error) {
	if !webURL(base) || base.User != nil {
		return nil, fmt.Errorf("invalid thread URL")
	}
	root, err := html.Parse(data)
	if err != nil {
		return nil, err
	}
	titleNode := findElement(root, func(n *html.Node) bool {
		return n.Data == "h1" && hasClass(n, "p-title-value")
	})
	if titleNode == nil || nodeText(titleNode) == "" {
		return nil, fmt.Errorf("no XenForo thread title; check login, challenge, or layout")
	}
	title := truncate(nodeText(titleNode), 500)
	seen := make(map[string]bool)
	var result []Candidate
	var parseErr error
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if parseErr != nil {
			return
		}
		if n.Type == html.ElementNode && n.Data == "article" && xenForoPostID.MatchString(attribute(n, "data-content")) {
			id := attribute(n, "data-content")
			if seen[id] {
				return
			}
			body := findElement(n, func(n *html.Node) bool { return hasClass(n, "bbWrapper") })
			published := findElement(n, func(n *html.Node) bool {
				return n.Data == "time" && attribute(n, "itemprop") == "datePublished"
			})
			author := strings.TrimSpace(attribute(n, "data-author"))
			if body == nil || published == nil || author == "" {
				parseErr = fmt.Errorf("incomplete XenForo post metadata or body")
				return
			}
			text, access, hiddenLinks := xenForoBody(xenForoText(body))
			stamp := attribute(published, "datetime")
			if (text == "" && access != "restricted") || !xenForoTimestamp(stamp) {
				parseErr = fmt.Errorf("XenForo post has no text or valid publication timestamp")
				return
			}
			link := *base
			link.Fragment = id
			// Prefer the source's stable post permalink over a page-dependent
			// fragment, so pagination does not manufacture revision updates.
			if permalink := findElement(n, func(a *html.Node) bool {
				if a.Data != "a" {
					return false
				}
				u := crawlURL(base, attribute(a, "href"))
				if u == nil {
					return false
				}
				path := strings.TrimRight(u.Path, "/")
				return strings.HasSuffix(path, "/"+id) || strings.HasSuffix(path, "/posts/"+strings.TrimPrefix(id, "post-"))
			}); permalink != nil {
				link = *crawlURL(base, attribute(permalink, "href"))
			}
			seen[id] = true
			result = append(result, Candidate{
				Key: id, PostID: id, Title: title, URL: link.String(), Author: author,
				Body: text, Summary: truncate(text, 1000), Published: stamp,
				Access: access, HiddenLinks: hiddenLinks,
			})
			return
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	if parseErr != nil {
		return nil, parseErr
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("no XenForo posts; check login, challenge, or layout")
	}
	return result, nil
}

func attribute(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func hasClass(n *html.Node, class string) bool {
	for _, token := range strings.Fields(attribute(n, "class")) {
		if token == class {
			return true
		}
	}
	return false
}

func findElement(n *html.Node, match func(*html.Node) bool) *html.Node {
	if n.Type == html.ElementNode && match(n) {
		return n
	}
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		if result := findElement(child, match); result != nil {
			return result
		}
	}
	return nil
}

func xenForoText(n *html.Node) string {
	var text strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "noscript", "template":
				return
			}
		}
		if n.Type == html.TextNode {
			text.WriteString(n.Data)
			text.WriteByte(' ')
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(text.String()), " ")
}

// Match the observed guest-only link placeholder, not general discussion of
// signing in. Unknown restriction layouts still require source-specific support.
var xenForoHiddenLink = regexp.MustCompile(`(?i)\bLink hidden, please Sign in or Sign up\b`)

func xenForoBody(text string) (string, string, int) {
	text = strings.Join(strings.Fields(text), " ")
	hidden := len(xenForoHiddenLink.FindAllStringIndex(text, -1))
	if hidden == 0 {
		return text, "", 0
	}
	text = strings.Join(strings.Fields(xenForoHiddenLink.ReplaceAllString(text, " ")), " ")
	if !strings.ContainsFunc(text, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }) {
		return "", "restricted", hidden
	}
	return text, "partial", hidden
}

func xenForoTimestamp(value string) bool {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05-0700"} {
		if _, err := time.Parse(layout, value); err == nil {
			return true
		}
	}
	return false
}
