package watch

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/idna"
)

type Config struct {
	Listen            string   `json:"listen"`
	TokenEnv          string   `json:"token_env"`
	StatePath         string   `json:"state_path"`
	TorProxy          string   `json:"tor_proxy"`
	BrowserCommand    []string `json:"browser_command"`
	BrowserProfileDir string   `json:"browser_profile_dir"`
	Sources           []Source `json:"sources"`
}

type Source struct {
	ID                string          `json:"id"`
	URL               string          `json:"url"`
	Transport         string          `json:"transport"`
	Fetcher           string          `json:"fetcher,omitempty"`
	Format            string          `json:"format"`
	IntervalSeconds   int             `json:"interval_seconds"`
	IncludeURL        string          `json:"include_url,omitempty"`
	UserAgent         string          `json:"user_agent,omitempty"`
	MaxPagesPerPoll   int             `json:"max_pages_per_poll,omitempty"`
	MaxThreads        int             `json:"max_threads,omitempty"`
	RequestGapSeconds int             `json:"request_gap_seconds,omitempty"`
	AlertFilter       AlertFilterSpec `json:"alert_filter,omitempty"`
}

func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return c, err
	}
	return c, c.Validate()
}

func (c *Config) Validate() error {
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8080"
	}
	if c.TokenEnv == "" {
		c.TokenEnv = "FORUMWATCH_TOKEN"
	}
	if c.StatePath == "" {
		c.StatePath = "data/state.json"
	}
	if c.TorProxy == "" {
		c.TorProxy = "127.0.0.1:9050"
	}
	if c.BrowserProfileDir == "" {
		c.BrowserProfileDir = "data/browser-profiles"
	}
	if len(c.BrowserCommand) > 0 && !filepath.IsAbs(c.BrowserCommand[0]) {
		return fmt.Errorf("browser_command executable must be an absolute path")
	}
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	if _, _, err := net.SplitHostPort(c.TorProxy); err != nil {
		return fmt.Errorf("tor_proxy: %w", err)
	}
	ids := make(map[string]bool)
	for i := range c.Sources {
		s := &c.Sources[i]
		if s.ID == "" || ids[s.ID] {
			return fmt.Errorf("source IDs must be nonempty and unique: %q", s.ID)
		}
		ids[s.ID] = true
		if s.Transport != "direct" && s.Transport != "tor" {
			return fmt.Errorf("%s: explicitly select transport direct or tor", s.ID)
		}
		if s.Fetcher == "" {
			s.Fetcher = "http"
		}
		if s.Fetcher != "http" && s.Fetcher != "browser" {
			return fmt.Errorf("%s: fetcher must be http or browser", s.ID)
		}
		if s.Fetcher == "browser" && (s.Transport != "tor" || len(c.BrowserCommand) == 0) {
			return fmt.Errorf("%s: browser fetcher requires tor transport and browser_command", s.ID)
		}
		u, err := url.Parse(s.URL)
		if err != nil || !webURL(u) || u.User != nil {
			return fmt.Errorf("%s: expected an HTTP(S) URL without credentials", s.ID)
		}
		if onion(u.Hostname()) && s.Transport != "tor" {
			return fmt.Errorf("%s: onion destinations require tor transport", s.ID)
		}
		if s.Format != "feed" && s.Format != "html" && !crawlFormat(s.Format) {
			return fmt.Errorf("%s: format must be feed, html, xenforo, xenforo-index, mybb, or mybb-index", s.ID)
		}
		if crawlFormat(s.Format) {
			if s.MaxPagesPerPoll == 0 {
				s.MaxPagesPerPoll = 4
			}
			if s.MaxThreads == 0 {
				s.MaxThreads = 256
			}
			if s.RequestGapSeconds == 0 {
				s.RequestGapSeconds = 2
			}
			if s.MaxPagesPerPoll < 1 || s.MaxPagesPerPoll > 32 || s.MaxThreads < 1 || s.MaxThreads > 4096 || s.RequestGapSeconds < 1 || s.RequestGapSeconds > 60 {
				return fmt.Errorf("%s: crawl bounds require max_pages_per_poll 1..32, max_threads 1..4096, request_gap_seconds 1..60", s.ID)
			}
		}
		if s.IntervalSeconds == 0 {
			s.IntervalSeconds = 60
		}
		if s.IntervalSeconds < 10 || s.IntervalSeconds > int((24*time.Hour)/time.Second) {
			return fmt.Errorf("%s: interval_seconds must be between 10 and 86400", s.ID)
		}
		if s.Format == "html" && s.IncludeURL == "" {
			return fmt.Errorf("%s: HTML sources need include_url to select thread/post links", s.ID)
		}
		if s.IncludeURL != "" {
			if _, err := regexp.Compile(s.IncludeURL); err != nil {
				return fmt.Errorf("%s: include_url: %w", s.ID, err)
			}
		}
		if _, err := compileAlertFilter(s.AlertFilter); err != nil {
			return fmt.Errorf("%s: %w", s.ID, err)
		}
		if s.UserAgent == "" && s.Fetcher != "browser" {
			s.UserAgent = "ForumWatch/0.1 (defensive monitoring)"
		}
	}
	return nil
}

func webURL(u *url.URL) bool {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return false
	}
	_, err := canonicalHost(u.Hostname())
	return err == nil
}

func canonicalHost(host string) (string, error) {
	if net.ParseIP(host) != nil {
		return host, nil
	}
	ascii, err := idna.Lookup.ToASCII(host)
	return strings.ToLower(strings.TrimSuffix(ascii, ".")), err
}

func onion(host string) bool {
	ascii, err := canonicalHost(host)
	return err == nil && strings.HasSuffix(ascii, ".onion")
}
