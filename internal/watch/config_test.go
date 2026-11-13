package watch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBrowserConfigValidation(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		fetcher   string
		transport string
		command   []string
		wantError bool
	}{
		{"default-http", "", "direct", nil, false},
		{"explicit-http", "http", "tor", nil, false},
		{"browser", "browser", "tor", []string{executable, "worker.py"}, false},
		{"browser-no-tor", "browser", "direct", []string{executable}, true},
		{"browser-no-command", "browser", "tor", nil, true},
		{"relative-command", "browser", "tor", []string{"python3", "worker.py"}, true},
		{"cwd-relative-command", "browser", "tor", []string{"./python3"}, true},
		{"empty-command", "browser", "tor", []string{""}, true},
		{"unused-relative-command", "http", "direct", []string{"python3"}, true},
		{"unknown-fetcher", "curl", "tor", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Config{BrowserCommand: tc.command, Sources: []Source{{
				ID: "forum", URL: "https://forum.example/", Format: "feed", Fetcher: tc.fetcher, Transport: tc.transport,
			}}}
			err := c.Validate()
			if (err != nil) != tc.wantError {
				t.Fatalf("Validate() = %v, wantError %v", err, tc.wantError)
			}
			if err != nil {
				return
			}
			if c.BrowserProfileDir != filepath.Join("data", "browser-profiles") {
				t.Fatal("missing browser profile default")
			}
			if tc.fetcher == "" && c.Sources[0].Fetcher != "http" {
				t.Fatal("missing HTTP fetcher default")
			}
			if tc.fetcher == "browser" && c.Sources[0].UserAgent != "" {
				t.Fatal("browser source received fake HTTP User-Agent")
			}
		})
	}
}

func TestCrawlConfigBounds(t *testing.T) {
	for _, source := range []Source{
		{MaxPagesPerPoll: -1}, {MaxPagesPerPoll: 33}, {MaxThreads: -1}, {MaxThreads: 4097}, {RequestGapSeconds: -1}, {RequestGapSeconds: 61},
	} {
		source.ID, source.URL, source.Transport, source.Format = "forum", "https://forum.example/forums/1/", "direct", "xenforo-index"
		c := Config{Sources: []Source{source}}
		if err := c.Validate(); err == nil {
			t.Fatalf("accepted invalid bounds: %+v", source)
		}
	}
	c := Config{Sources: []Source{{ID: "forum", URL: "https://forum.example/forums/1/", Transport: "direct", Format: "xenforo-index"}}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.Sources[0].MaxPagesPerPoll != 4 || c.Sources[0].MaxThreads != 256 || c.Sources[0].RequestGapSeconds != 2 {
		t.Fatal("missing crawl defaults")
	}
}

func TestXenForoConfigNeedsNoIncludeURL(t *testing.T) {
	c := Config{Sources: []Source{{ID: "forum", URL: "https://forum.example/", Transport: "direct", Format: "xenforo"}}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}
