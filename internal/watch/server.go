package watch

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Hub struct {
	mu      sync.Mutex
	clients map[chan Item]struct{}
}

func NewHub() *Hub { return &Hub{clients: make(map[chan Item]struct{})} }

func (h *Hub) Subscribe() (chan Item, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ch := make(chan Item, 32)
	h.clients[ch] = struct{}{}
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, exists := h.clients[ch]; exists {
			delete(h.clients, ch)
			close(ch)
		}
	}
}

func (h *Hub) Publish(item Item) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
		select {
		case ch <- item:
		default:
			// Disconnect slow clients; reconnect and fetch the durable item snapshot.
			delete(h.clients, ch)
			close(ch)
		}
	}
}

func Handler(token string, store *Store, hub *Hub, collector *Collector) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, "ForumWatch\n/feed.xml — private RSS\n/events — live SSE (resync via /api/items on connect)\n/api/items — latest 200 items\n/api/status — collector health\n")
	})
	mux.HandleFunc("GET /api/items", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(store.Items(200))
	})
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(collector.Statuses())
	})
	mux.HandleFunc("GET /feed.xml", func(w http.ResponseWriter, r *http.Request) {
		type rssItem struct {
			Title string `xml:"title"`
			Link  string `xml:"link"`
			GUID  struct {
				PermaLink bool   `xml:"isPermaLink,attr"`
				Value     string `xml:",chardata"`
			} `xml:"guid"`
			Description string `xml:"description"`
			PubDate     string `xml:"pubDate"`
			Category    string `xml:"category"`
		}
		type rssChannel struct {
			Title       string    `xml:"title"`
			Link        string    `xml:"link"`
			Description string    `xml:"description"`
			Items       []rssItem `xml:"item"`
		}
		feed := struct {
			XMLName xml.Name   `xml:"rss"`
			Version string     `xml:"version,attr"`
			Channel rssChannel `xml:"channel"`
		}{Version: "2.0", Channel: rssChannel{Title: "ForumWatch", Link: "http://localhost/", Description: "First-party defensive monitoring; claims are unverified."}}
		for _, item := range store.Items(200) {
			entry := rssItem{Title: "[" + item.Source + "] " + item.Title, Link: item.URL,
				Description: html.EscapeString(item.Summary), PubDate: item.Observed.Format(time.RFC1123Z), Category: item.Kind}
			// A revision gets a new GUID, so feed readers can surface changed entries.
			entry.GUID.Value = "urn:forumwatch:" + item.ID + ":" + item.Hash
			feed.Channel.Items = append(feed.Channel.Items, entry)
		}
		data, err := xml.Marshal(feed)
		if err != nil {
			http.Error(w, "cannot render feed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
		io.WriteString(w, xml.Header)
		w.Write(data)
	})
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unavailable", http.StatusInternalServerError)
			return
		}
		ch, unsubscribe := hub.Subscribe()
		defer unsubscribe()
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Accel-Buffering", "no")
		controller := http.NewResponseController(w)
		send := func(message string) error {
			if err := controller.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
				return err
			}
			if _, err := io.WriteString(w, message); err != nil {
				return err
			}
			flusher.Flush()
			return nil
		}
		if err := send("event: ready\ndata: {\"resync\":\"/api/items\"}\n\n"); err != nil {
			return
		}
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case item, ok := <-ch:
				if !ok {
					return
				}
				data, _ := json.Marshal(item)
				if err := send(fmt.Sprintf("event: item\ndata: %s\n\n", data)); err != nil {
					return
				}
			case <-ticker.C:
				if err := send(": heartbeat\n\n"); err != nil {
					return
				}
			}
		}
	})
	want := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		credential := ""
		fields := strings.Fields(r.Header.Get("Authorization"))
		if len(fields) == 2 && strings.EqualFold(fields[0], "Bearer") {
			credential = fields[1]
		} else if _, password, ok := r.BasicAuth(); ok {
			credential = password
		}
		got := sha256.Sum256([]byte(credential))
		if token == "" || subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="ForumWatch"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	})
}
