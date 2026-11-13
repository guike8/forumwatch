package watch

import (
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

var myBBPostID = regexp.MustCompile(`^post_([1-9][0-9]*)$`)

// Read only the observed quick_quote call's numeric fields. This is not a JS
// interpreter; scripts, relative dates, and the author's quoted string are never
// executed or used as content. Repeated calls must agree on the publication time.
var myBBQuickQuote = regexp.MustCompile(`\bquick_quote\(\s*([0-9]+)\s*,\s*(?:'(?:\\.|[^'\\])*'|"(?:\\.|[^"\\])*")\s*,\s*([0-9]+)\s*\)`)

// ParseMyBB supports the captured DarkForums friendly thread/post layout only.
func ParseMyBB(data io.Reader, base *url.URL) ([]Candidate, error) {
	if myBBThread(base) == "" {
		return nil, fmt.Errorf("invalid or unsupported MyBB thread URL")
	}
	root, err := html.Parse(data)
	if err != nil {
		return nil, err
	}
	if myBBGate(root) {
		return nil, fmt.Errorf("MyBB login or challenge page")
	}
	titleNode := findElement(root, func(n *html.Node) bool { return n.Data == "title" })
	if titleNode == nil {
		return nil, fmt.Errorf("no MyBB document title")
	}
	title := xenForoText(titleNode)
	for _, suffix := range []string{" - DarkForums", " | DarkForums"} {
		title = strings.TrimSuffix(title, suffix)
	}
	if title == "" || title == "DarkForums" || title == "- DarkForums" || title == "| DarkForums" {
		return nil, fmt.Errorf("no MyBB thread title")
	}
	title = truncate(title, 500)

	var result []Candidate
	seen := map[string]bool{}
	var parseErr error
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if parseErr != nil {
			return
		}
		if n.Type == html.ElementNode && n.Data == "div" && hasClass(n, "post") {
			match := myBBPostID.FindStringSubmatch(attribute(n, "id"))
			if match == nil {
				parseErr = fmt.Errorf("malformed MyBB post ID")
				return
			}
			id := match[1]
			if seen[id] {
				return
			}
			body := myBBPostElement(n, func(n *html.Node) bool {
				return n.Data == "div" && hasClass(n, "post_body") && attribute(n, "id") == "pid_"+id
			})
			profile := myBBPostElement(n, func(n *html.Node) bool { return hasClass(n, "post__user-profile") })
			if body == nil || profile == nil || xenForoText(profile) == "" {
				parseErr = fmt.Errorf("incomplete MyBB post body or author")
				return
			}
			stamp, stampErr := myBBPostTime(n, id)
			if stampErr != nil {
				parseErr = stampErr
				return
			}
			text, access, hidden := xenForoBody(xenForoText(body))
			if text == "" && access != "restricted" {
				parseErr = fmt.Errorf("MyBB post has no recognized content")
				return
			}
			// The observed source permalink is independent of the page on which a
			// post currently appears. Do not retain the volatile page query.
			link := *base
			link.RawQuery, link.ForceQuery = "pid="+id, false
			link.Fragment, link.RawFragment = "pid"+id, ""
			postID := "post-" + id
			result = append(result, Candidate{
				Key: postID, PostID: postID, Title: title, URL: link.String(),
				Author: xenForoText(profile), Body: text, Summary: truncate(text, 1000),
				Published: stamp, Access: access, HiddenLinks: hidden,
			})
			seen[id] = true
			return
		}
		if myBBInert(n) {
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
		return nil, fmt.Errorf("no MyBB posts; check login, challenge, or layout")
	}
	return result, nil
}

// Do not borrow metadata from a nested post or inert template.
func myBBPostElement(post *html.Node, match func(*html.Node) bool) *html.Node {
	var walk func(*html.Node) *html.Node
	walk = func(n *html.Node) *html.Node {
		if n != post && (myBBInert(n) || hasClass(n, "post")) {
			return nil
		}
		if n.Type == html.ElementNode && match(n) {
			return n
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			if found := walk(child); found != nil {
				return found
			}
		}
		return nil
	}
	return walk(post)
}

func myBBPostTime(post *html.Node, id string) (string, error) {
	var stamp int64
	var invalid bool
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n != post && (hasClass(n, "post") || hasClass(n, "post_body") || n.Data == "template" || n.Data == "noscript") {
			return
		}
		if n.Type == html.ElementNode && n.Data == "script" {
			var script strings.Builder
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.TextNode {
					script.WriteString(c.Data)
				}
			}
			for _, match := range myBBQuickQuote.FindAllStringSubmatch(script.String(), -1) {
				if match[1] != id {
					continue
				}
				value, err := strconv.ParseInt(match[2], 10, 64)
				// MyBB-era dates only, with a small allowance for source clock skew.
				if err != nil || value < 946684800 || value > time.Now().Add(24*time.Hour).Unix() || (stamp != 0 && value != stamp) {
					invalid = true
				}
				stamp = value
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(post)
	if invalid || stamp == 0 {
		return "", fmt.Errorf("MyBB post has no valid, consistent quick_quote timestamp")
	}
	return time.Unix(stamp, 0).UTC().Format(time.RFC3339), nil
}

func myBBInert(n *html.Node) bool {
	return n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style" || n.Data == "noscript" || n.Data == "template")
}

// A password field alone is not a gate: public threads may include a nav login
// widget. Require the known gate title/structure, or let missing posts fail closed.
func myBBGate(root *html.Node) bool {
	return findElement(root, func(n *html.Node) bool {
		if n.Data == "title" {
			title := strings.ToLower(xenForoText(n))
			for _, suffix := range []string{" - darkforums", " | darkforums"} {
				title = strings.TrimSuffix(title, suffix)
			}
			title = strings.TrimPrefix(title, "darkforums - ")
			switch title {
			case "login", "log in", "sign in", "just a moment...", "attention required! | cloudflare", "access denied":
				return true
			}
		}
		switch attribute(n, "id") {
		case "challenge-form", "challenge-running", "cf-challenge-running":
			return true
		}
		return false
	}) != nil
}
