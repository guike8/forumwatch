package watch

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

const maxBody = 2 << 20

type Status struct {
	ID          string    `json:"id"`
	Transport   string    `json:"transport"`
	LastAttempt time.Time `json:"last_attempt"`
	LastSuccess time.Time `json:"last_success"`
	NextCheck   time.Time `json:"next_check"`
	Error       string    `json:"error,omitempty"`
	Failures    int       `json:"consecutive_failures"`
	FetchMS     int64     `json:"fetch_ms"`
}

type Collector struct {
	Config Config
	Store  *Store
	Hub    *Hub
	mu     sync.RWMutex
	status map[string]Status
}

func NewCollector(c Config, s *Store, h *Hub) *Collector {
	statuses := make(map[string]Status)
	for _, source := range c.Sources {
		statuses[source.ID] = Status{ID: source.ID, Transport: source.Transport}
	}
	return &Collector{Config: c, Store: s, Hub: h, status: statuses}
}

func (c *Collector) Statuses() []Status {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make([]Status, 0, len(c.Config.Sources))
	for _, s := range c.Config.Sources {
		result = append(result, c.status[s.ID])
	}
	return result
}

func newClient(s Source, torProxy string) (*http.Client, error) {
	dialer := &net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		DialContext: dialer.DialContext, MaxIdleConns: 8, MaxIdleConnsPerHost: 2,
		TLSHandshakeTimeout: 15 * time.Second, IdleConnTimeout: 60 * time.Second,
	}
	if s.Transport == "tor" {
		d, err := proxy.SOCKS5("tcp", torProxy, nil, dialer)
		if err != nil {
			return nil, err
		}
		contextDialer, ok := d.(proxy.ContextDialer)
		if !ok {
			return nil, fmt.Errorf("SOCKS dialer does not support cancellation")
		}
		// Pass destination hostnames to SOCKS: onion names must not hit local DNS.
		transport.DialContext = contextDialer.DialContext
	}
	return &http.Client{
		Transport: transport, Timeout: 45 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Validators belong to a resource, not an entire redirect chain.
			req.Header.Del("If-None-Match")
			req.Header.Del("If-Modified-Since")
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			if !webURL(req.URL) || req.URL.User != nil {
				return fmt.Errorf("unsupported redirect destination")
			}
			if onion(req.URL.Hostname()) && s.Transport != "tor" {
				return fmt.Errorf("refusing direct redirect to onion destination")
			}
			return nil
		},
	}, nil
}

func (c *Collector) Run(ctx context.Context) error {
	if len(c.Config.Sources) == 0 {
		<-ctx.Done()
		return nil
	}
	var wg sync.WaitGroup
	clients := make([]*http.Client, len(c.Config.Sources))
	hosts := make(map[string]chan struct{})
	for i, s := range c.Config.Sources {
		client, err := newClient(s, c.Config.TorProxy)
		if err != nil {
			return err
		}
		clients[i] = client
		u, _ := url.Parse(s.URL)
		hosts[strings.ToLower(u.Hostname())] = make(chan struct{}, 1)
	}
	global := make(chan struct{}, 4)
	for i, source := range c.Config.Sources {
		u, _ := url.Parse(source.URL)
		host := hosts[strings.ToLower(u.Hostname())]
		wg.Add(1)
		go func(s Source, client *http.Client) {
			defer wg.Done()
			defer client.CloseIdleConnections()
			failures := 0
			delay := time.Duration(rand.IntN(1000)) * time.Millisecond
			for wait(ctx, delay) {
				if !acquire(ctx, host) {
					return
				}
				if !acquire(ctx, global) {
					<-host
					return
				}
				started := time.Now().UTC()
				c.mu.Lock()
				status := c.status[s.ID]
				status.LastAttempt = started
				c.status[s.ID] = status
				c.mu.Unlock()
				retry, err := c.poll(ctx, s, client)
				<-host
				<-global
				delay = time.Duration(s.IntervalSeconds) * time.Second
				c.mu.Lock()
				status = c.status[s.ID]
				status.FetchMS = time.Since(started).Milliseconds()
				if err != nil {
					failures++
					status.Error = err.Error()
					delay = failureDelay(delay, failures)
					slog.Warn("source check failed", "source", s.ID, "error", err)
				} else {
					failures = 0
					status.Error = ""
					status.LastSuccess = time.Now().UTC()
				}
				delay += time.Duration(float64(delay) * rand.Float64() * 0.2)
				delay = max(delay, retry)
				status.Failures = failures
				status.NextCheck = time.Now().UTC().Add(delay)
				c.status[s.ID] = status
				c.mu.Unlock()
			}
		}(source, clients[i])
	}
	wg.Wait()
	return nil
}

func (c *Collector) poll(ctx context.Context, s Source, client *http.Client) (time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		return 0, err
	}
	checkpoint := c.Store.Checkpoint(s.ID)
	req.Header.Set("User-Agent", s.UserAgent)
	if checkpoint.ETag != "" {
		req.Header.Set("If-None-Match", checkpoint.ETag)
	}
	if checkpoint.LastModified != "" {
		req.Header.Set("If-Modified-Since", checkpoint.LastModified)
	}
	res, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotModified && checkpoint.Initialized {
		return 0, nil
	}
	if res.StatusCode != http.StatusOK {
		return retryAfter(res.Header.Get("Retry-After"), time.Now()), fmt.Errorf("HTTP %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxBody+1))
	if err != nil {
		return 0, err
	}
	if len(body) > maxBody {
		return 0, fmt.Errorf("response exceeds %d bytes", maxBody)
	}
	var candidates []Candidate
	if s.Format == "feed" {
		candidates, err = ParseFeed(bytes.NewReader(body), res.Request.URL)
	} else {
		include, compileErr := regexp.Compile(s.IncludeURL)
		if compileErr != nil {
			return 0, compileErr
		}
		candidates, err = ParseHTML(bytes.NewReader(body), res.Request.URL, include)
	}
	if err != nil {
		return 0, err
	}
	checkpoint.ETag = ""
	checkpoint.LastModified = ""
	if res.Request.URL.String() == req.URL.String() {
		checkpoint.ETag = res.Header.Get("ETag")
		checkpoint.LastModified = res.Header.Get("Last-Modified")
	}
	items, err := c.Store.Apply(s.ID, candidates, checkpoint, time.Now().UTC())
	if err != nil {
		return 0, fmt.Errorf("persist checkpoint: %w", err)
	}
	for _, item := range items {
		c.Hub.Publish(item)
	}
	return 0, nil
}

func failureDelay(base time.Duration, failures int) time.Duration {
	return max(base, min(base*time.Duration(1<<min(failures, 6)), 30*time.Minute))
}

func retryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.ParseInt(value, 10, 32); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if t, err := http.ParseTime(value); err == nil && t.After(now) {
		return t.Sub(now)
	}
	return 0
}

func wait(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func acquire(ctx context.Context, semaphore chan struct{}) bool {
	select {
	case <-ctx.Done():
		return false
	case semaphore <- struct{}{}:
		return true
	}
}
