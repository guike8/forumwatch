package watch

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

type Candidate struct {
	Key         string
	Title       string
	URL         string
	Summary     string
	Author      string
	PostID      string
	Body        string
	Access      string
	HiddenLinks int
	Published   string
	Updated     string
}

type feedText struct {
	Type  string `xml:"type,attr"`
	Value string `xml:",chardata"`
	Inner string `xml:",innerxml"`
}

func (t feedText) text(atom bool) string {
	if atom && (t.Type == "" || t.Type == "text") {
		return strings.Join(strings.Fields(t.Value), " ")
	}
	if t.Type == "xhtml" {
		return plainText(t.Inner)
	}
	return plainText(t.Value)
}

type feedEntry struct {
	Base  string   `xml:"http://www.w3.org/XML/1998/namespace base,attr"`
	ID    string   `xml:"id"`
	GUID  string   `xml:"guid"`
	Title feedText `xml:"title"`
	Links []struct {
		Base string `xml:"http://www.w3.org/XML/1998/namespace base,attr"`
		Href string `xml:"href,attr"`
		Rel  string `xml:"rel,attr"`
		Text string `xml:",chardata"`
	} `xml:"link"`
	Description string   `xml:"description"`
	Summary     feedText `xml:"summary"`
	Published   string   `xml:"published"`
	PubDate     string   `xml:"pubDate"`
	Updated     string   `xml:"updated"`
}

func ParseFeed(data io.Reader, base *url.URL) ([]Candidate, error) {
	var document struct {
		XMLName xml.Name
		Base    string `xml:"http://www.w3.org/XML/1998/namespace base,attr"`
		Channel struct {
			Items []feedEntry `xml:"item"`
		} `xml:"channel"`
		Entries []feedEntry `xml:"entry"`
	}
	if err := xml.NewDecoder(data).Decode(&document); err != nil {
		return nil, fmt.Errorf("parse feed: %w", err)
	}
	var entries []feedEntry
	switch document.XMLName.Local {
	case "rss":
		entries = document.Channel.Items
	case "feed":
		entries = document.Entries
	default:
		return nil, fmt.Errorf("expected RSS or Atom, got %q", document.XMLName.Local)
	}
	feedBase, err := base.Parse(document.Base)
	if err != nil {
		return nil, fmt.Errorf("invalid feed base: %w", err)
	}
	atom := document.XMLName.Local == "feed"
	result := make([]Candidate, 0, len(entries))
	for _, entry := range entries {
		entryBase, err := feedBase.Parse(entry.Base)
		if err != nil {
			return nil, fmt.Errorf("invalid entry base: %w", err)
		}
		link := ""
		linkBase := entryBase
		for _, l := range entry.Links {
			if l.Rel == "alternate" || l.Rel == "" {
				link = first(l.Href, l.Text)
				if link != "" {
					linkBase, err = entryBase.Parse(l.Base)
					if err != nil {
						return nil, fmt.Errorf("invalid link base: %w", err)
					}
					break
				}
			}
		}
		u, err := linkBase.Parse(link)
		if link == "" || err != nil || !webURL(u) || u.User != nil {
			continue
		}
		key := first(entry.ID, entry.GUID, u.String())
		result = append(result, Candidate{
			Key: key, Title: entry.Title.text(atom), URL: u.String(),
			Summary:   truncate(first(entry.Summary.text(atom), plainText(entry.Description)), 1000),
			Published: first(entry.Published, entry.PubDate), Updated: entry.Updated,
		})
	}
	return result, nil
}

func ParseHTML(data io.Reader, base *url.URL, include *regexp.Regexp) ([]Candidate, error) {
	root, err := html.Parse(data)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var result []Candidate
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			for _, a := range n.Attr {
				if a.Key != "href" || strings.TrimSpace(a.Val) == "" {
					continue
				}
				u, err := base.Parse(a.Val)
				if err != nil || !webURL(u) || u.User != nil || !strings.EqualFold(u.Host, base.Host) {
					continue
				}
				link := u.String()
				title := nodeText(n)
				if include == nil || !include.MatchString(link) || seen[link] || title == "" {
					continue
				}
				seen[link] = true
				result = append(result, Candidate{Key: link, Title: truncate(title, 500), URL: link})
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	if len(result) == 0 {
		return nil, fmt.Errorf("no matching thread/post links; check selector, login, or challenge page")
	}
	return result, nil
}

func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style") {
			return
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			b.WriteByte(' ')
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

func plainText(s string) string {
	n, err := html.Parse(strings.NewReader(s))
	if err != nil {
		return ""
	}
	return nodeText(n)
}

func first(values ...string) string {
	for _, s := range values {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

func digest(parts ...string) string {
	var data []byte
	for _, part := range parts {
		data = binary.BigEndian.AppendUint64(data, uint64(len(part)))
		data = append(data, part...)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
