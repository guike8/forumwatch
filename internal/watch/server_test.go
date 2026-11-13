package watch

import (
	"bufio"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testToken = "test-token-not-for-real-deployment"

func TestHTTPAuthenticationAndFeed(t *testing.T) {
	c := testCollector(t)
	_, err := c.Store.Apply("forum", []Candidate{{Key: "x", Title: "A & B", URL: "https://example.com/1", Summary: "Plain text"}}, Checkpoint{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	h := Handler(testToken, c.Store, c.Hub, c)
	for _, path := range []string{"/", "/feed.xml", "/events", "/api/items", "/api/status"} {
		r := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s returned %d", path, w.Code)
		}
	}
	for _, basic := range []bool{false, true} {
		r := httptest.NewRequest("GET", "/feed.xml", nil)
		if basic {
			r.SetBasicAuth("feed", testToken)
		} else {
			r.Header.Set("Authorization", "Bearer "+testToken)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("feed returned %d", w.Code)
		}
		var rss struct {
			Title string `xml:"channel>item>title"`
		}
		if err := xml.Unmarshal(w.Body.Bytes(), &rss); err != nil || rss.Title != "[forum] A & B" {
			t.Fatalf("invalid RSS: %+v %v", rss, err)
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("private feed can be cached")
		}
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/feed.xml?token="+testToken, nil)
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatal("accepted token in URL")
	}
}

func TestLiveSSE(t *testing.T) {
	c := testCollector(t)
	server := httptest.NewServer(Handler(testToken, c.Store, c.Hub, c))
	defer server.Close()
	req, _ := http.NewRequest("GET", server.URL+"/events", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	client := &http.Client{Timeout: 3 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	scanner := bufio.NewScanner(res.Body)
	if !scanner.Scan() || scanner.Text() != "event: ready" {
		t.Fatal("missing SSE ready event")
	}
	c.Hub.Publish(Item{ID: "test", Title: "Live post"})
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "data: ") && strings.Contains(scanner.Text(), "Live post") {
			return
		}
	}
	t.Fatalf("missing live item: %v", scanner.Err())
}

func TestSlowSubscriberDisconnected(t *testing.T) {
	h := NewHub()
	ch, cancel := h.Subscribe()
	defer cancel()
	for i := 0; i < 40; i++ {
		h.Publish(Item{ID: "x"})
	}
	count := 0
	for range ch {
		count++
	}
	if count != 32 {
		t.Fatalf("unexpected buffered items: %d", count)
	}
}

func TestOversizeBodyRejected(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, strings.Repeat("x", maxBody+1))
	}))
	defer origin.Close()
	c := testCollector(t)
	s := Source{ID: "forum", URL: origin.URL, Format: "feed", Transport: "direct"}
	client, _ := newClient(s, "")
	defer client.CloseIdleConnections()
	if _, err := c.poll(t.Context(), s, client); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("accepted oversized body: %v", err)
	}
}
