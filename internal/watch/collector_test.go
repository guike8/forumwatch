package watch

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testCollector(t *testing.T) *Collector {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	return NewCollector(Config{}, store, NewHub())
}

func TestPollPersistsBeforePublishingAndUsesETag(t *testing.T) {
	var calls atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) > 1 {
			if r.Header.Get("If-None-Match") != `"v1"` {
				t.Error("missing conditional request")
			}
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		fmt.Fprint(w, `<rss><channel><item><guid>x</guid><title>Post</title><link>/thread/1</link></item></channel></rss>`)
	}))
	defer origin.Close()
	c := testCollector(t)
	s := Source{ID: "forum", URL: origin.URL, Transport: "direct", Format: "feed"}
	client, _ := newClient(s, "")
	defer client.CloseIdleConnections()
	ch, unsubscribe := c.Hub.Subscribe()
	defer unsubscribe()
	if _, err := c.poll(context.Background(), s, client); err != nil {
		t.Fatal(err)
	}
	select {
	case item := <-ch:
		reopened, err := OpenStore(c.Store.path)
		if err != nil || len(reopened.Items(0)) != 1 || item.Kind != "baseline" {
			t.Fatalf("event without durable state: %v", err)
		}
	default:
		t.Fatal("no live event")
	}
	if _, err := c.poll(context.Background(), s, client); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ch:
		t.Fatal("duplicate event for 304")
	default:
	}
}

func TestFailedParseDoesNotCommitValidators(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", "bad-page")
		fmt.Fprint(w, `<html>Login required</html>`)
	}))
	defer origin.Close()
	c := testCollector(t)
	s := Source{ID: "forum", URL: origin.URL, Transport: "direct", Format: "feed"}
	client, _ := newClient(s, "")
	defer client.CloseIdleConnections()
	if _, err := c.poll(context.Background(), s, client); err == nil {
		t.Fatal("accepted login page")
	}
	if c.Store.Checkpoint("forum").ETag != "" {
		t.Fatal("cached malformed response")
	}
}

func TestTorDoesNotFallBackToDirect(t *testing.T) {
	var contacted atomic.Bool
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contacted.Store(true)
	}))
	defer origin.Close()
	proxyListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxyAddress := proxyListener.Addr().String()
	proxyListener.Close()
	client, err := newClient(Source{Transport: "tor"}, proxyAddress)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	res, err := client.Get(origin.URL)
	if res != nil {
		res.Body.Close()
	}
	if err == nil || contacted.Load() {
		t.Fatalf("Tor failure did not fail closed: %v", err)
	}
}

func TestSOCKSPassesOnionHostnameToProxy(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	destination := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		greeting := make([]byte, 2)
		if _, err := io.ReadFull(conn, greeting); err != nil {
			return
		}
		methods := make([]byte, int(greeting[1]))
		if _, err := io.ReadFull(conn, methods); err != nil {
			return
		}
		conn.Write([]byte{5, 0})
		header := make([]byte, 5)
		if _, err := io.ReadFull(conn, header); err != nil || header[3] != 3 {
			return
		}
		host := make([]byte, int(header[4])+2)
		if _, err := io.ReadFull(conn, host); err != nil {
			return
		}
		destination <- fmt.Sprintf("%s:%d", host[:len(host)-2], binary.BigEndian.Uint16(host[len(host)-2:]))
		conn.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
	}()
	client, _ := newClient(Source{Transport: "tor"}, listener.Addr().String())
	defer client.CloseIdleConnections()
	res, err := client.Get("http://test-hidden-service.onion/")
	if res != nil {
		res.Body.Close()
	}
	if err == nil {
		t.Fatal("expected fake SOCKS server to refuse connection")
	}
	select {
	case got := <-destination:
		if got != "test-hidden-service.onion:80" {
			t.Fatalf("unexpected SOCKS destination %s", got)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("hostname was not sent through SOCKS")
	}
}

func TestDirectRejectsOnionRedirect(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://hidden.onion/", http.StatusFound)
	}))
	defer origin.Close()
	client, _ := newClient(Source{Transport: "direct"}, "")
	defer client.CloseIdleConnections()
	res, err := client.Get(origin.URL)
	if res != nil {
		res.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "refusing direct redirect") {
		t.Fatalf("did not reject onion redirect: %v", err)
	}
}

func TestRedirectsDoNotReuseResourceValidators(t *testing.T) {
	var version atomic.Int32
	version.Store(1)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest" {
			http.Redirect(w, r, fmt.Sprintf("/feed/%d", version.Load()), http.StatusFound)
			return
		}
		if r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != "" {
			t.Error("resource validator leaked across redirect")
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"shared-tag"`)
		fmt.Fprintf(w, `<rss><channel><item><guid>%d</guid><title>Post</title><link>/thread/%d</link></item></channel></rss>`, version.Load(), version.Load())
	}))
	defer origin.Close()
	c := testCollector(t)
	s := Source{ID: "forum", URL: origin.URL + "/latest", Format: "feed", Transport: "direct"}
	client, _ := newClient(s, "")
	defer client.CloseIdleConnections()
	for i := int32(1); i <= 2; i++ {
		version.Store(i)
		if _, err := c.poll(t.Context(), s, client); err != nil {
			t.Fatal(err)
		}
		if c.Store.Checkpoint(s.ID).ETag != "" {
			t.Fatal("retained final-resource validator under redirecting URL")
		}
	}
	if len(c.Store.Items(0)) != 2 {
		t.Fatal("missed changed redirect destination")
	}
}

func TestFailureBackoffNeverShortensInterval(t *testing.T) {
	if failureDelay(24*time.Hour, 5) < 24*time.Hour {
		t.Fatal("daily source retries too frequently")
	}
	if failureDelay(time.Minute, 2) != 4*time.Minute {
		t.Fatal("unexpected exponential backoff")
	}
}

func TestRunWithoutSourcesWaitsForCancellation(t *testing.T) {
	c := testCollector(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	select {
	case err := <-done:
		t.Fatalf("empty configuration stopped service: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("collector did not stop")
	}
}

func TestRunCollectsAndStops(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<rss><channel><item><guid>x</guid><title>Live</title><link>/thread/1</link></item></channel></rss>`)
	}))
	defer origin.Close()
	c := testCollector(t)
	c.Config.Sources = []Source{{ID: "forum", URL: origin.URL, Transport: "direct", Format: "feed", IntervalSeconds: 60}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ch, unsubscribe := c.Hub.Subscribe()
	defer unsubscribe()
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	select {
	case item := <-ch:
		if item.Title != "Live" {
			t.Fatalf("wrong live item: %+v", item)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("collector did not publish")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("collector did not stop")
	}
}

func TestRetryAfter(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	if retryAfter("120", now) != 2*time.Minute || retryAfter("invalid", now) != 0 {
		t.Fatal("invalid retry delay")
	}
	if retryAfter(now.Add(time.Hour).Format(http.TimeFormat), now) != time.Hour {
		t.Fatal("invalid HTTP-date retry delay")
	}
}
