package watch

import (
	"io"
	"net/url"
)

func crawlFormat(format string) bool {
	return format == "xenforo" || format == "xenforo-index" || format == "mybb" || format == "mybb-index"
}

func myBBFormat(format string) bool {
	return format == "mybb" || format == "mybb-index"
}

func indexFormat(format string) bool {
	return format == "xenforo-index" || format == "mybb-index"
}

func forumThread(u *url.URL, format string) string {
	if myBBFormat(format) {
		return myBBThread(u)
	}
	return xenForoThread(u)
}

func parseForumPosts(data io.Reader, base *url.URL, format string) ([]Candidate, error) {
	if myBBFormat(format) {
		return ParseMyBB(data, base)
	}
	return ParseXenForo(data, base)
}

func parseForumLinks(data io.Reader, base *url.URL, index bool, format string) (XenForoLinks, error) {
	if myBBFormat(format) {
		return ParseMyBBLinks(data, base, index)
	}
	return ParseXenForoLinks(data, base, index)
}
