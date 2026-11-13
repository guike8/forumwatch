package watch

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type Item struct {
	ID          string      `json:"id"`
	Source      string      `json:"source"`
	Title       string      `json:"title"`
	URL         string      `json:"url"`
	Summary     string      `json:"summary,omitempty"`
	Author      string      `json:"author,omitempty"`
	PostID      string      `json:"post_id,omitempty"`
	Body        string      `json:"body,omitempty"`
	Access      string      `json:"access,omitempty"`
	HiddenLinks int         `json:"hidden_links,omitempty"`
	Published   string      `json:"published,omitempty"`
	FirstSeen   time.Time   `json:"first_seen"`
	Observed    time.Time   `json:"observed_at"`
	Kind        string      `json:"kind"`
	Hash        string      `json:"hash"`
	Enrichment  *Enrichment `json:"enrichment,omitempty"`
	Filtered    bool        `json:"filtered,omitempty"` // transient SSE removal, never stored
}

type Checkpoint struct {
	Initialized  bool        `json:"initialized"`
	ETag         string      `json:"etag,omitempty"`
	LastModified string      `json:"last_modified,omitempty"`
	Crawl        *CrawlState `json:"crawl,omitempty"`
}

type diskState struct {
	Version int                   `json:"version"`
	Items   map[string]Item       `json:"items"`
	Sources map[string]Checkpoint `json:"sources"`
}

type Store struct {
	mu   sync.RWMutex
	path string
	data diskState
}

func OpenStore(path string) (*Store, error) {
	s := &Store{path: path, data: diskState{Version: 1, Items: map[string]Item{}, Sources: map[string]Checkpoint{}}}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var data diskState
	if err := json.Unmarshal(b, &data); err != nil {
		return nil, fmt.Errorf("invalid state (refusing to reset): %w", err)
	}
	if data.Version != 1 || data.Items == nil || data.Sources == nil {
		return nil, fmt.Errorf("unsupported or incomplete state")
	}
	s.data = data
	// Repair legacy post bodies before summary recovery or restart alerts. This
	// is a representation correction, not a newly observed source revision.
	repaired := false
	for id, item := range s.data.Items {
		if item.PostID == "" || item.Access != "" {
			continue
		}
		body, access, hidden := xenForoBody(item.Body)
		if hidden == 0 {
			continue
		}
		item.Body, item.Summary = body, truncate(body, 1000)
		item.Access, item.HiddenLinks, item.Enrichment = access, hidden, nil
		item.Hash = candidateHash(Candidate{Title: item.Title, URL: item.URL,
			Summary: item.Summary, Published: item.Published, Author: item.Author,
			Body: body, Access: access, HiddenLinks: hidden})
		s.data.Items[id] = item
		repaired = true
	}
	if repaired {
		b, err := marshalSourceState(s.data)
		if err != nil {
			return nil, err
		}
		if err := atomicWrite(path, b); err != nil {
			return nil, fmt.Errorf("persist access correction: %w", err)
		}
	}
	return s, nil
}

func (s *Store) Checkpoint(id string) Checkpoint {
	s.mu.RLock()
	defer s.mu.RUnlock()
	checkpoint := s.data.Sources[id]
	checkpoint.Crawl = cloneCrawl(checkpoint.Crawl)
	return checkpoint
}

func (s *Store) Apply(source string, candidates []Candidate, checkpoint Checkpoint, now time.Time) ([]Item, error) {
	return s.apply(source, candidates, checkpoint, now, nil)
}

// ApplyPage commits each crawl page and its frontier together. Initial backfill
// remains a baseline even when it spans several successful polls or restarts.
func (s *Store) ApplyPage(source string, candidates []Candidate, checkpoint Checkpoint, now time.Time, baseline bool) ([]Item, error) {
	return s.apply(source, candidates, checkpoint, now, &baseline)
}

func (s *Store) apply(source string, candidates []Candidate, checkpoint Checkpoint, now time.Time, pageBaseline *bool) ([]Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	baseline := !s.data.Sources[source].Initialized
	if pageBaseline != nil {
		baseline = *pageBaseline
	}
	next := diskState{Version: 1, Items: maps.Clone(s.data.Items), Sources: maps.Clone(s.data.Sources)}
	var changed []Item
	for _, c := range candidates {
		id := digest(source, c.Key)
		hash := candidateHash(c)
		previous, exists := next.Items[id]
		if exists && previous.Hash == hash {
			continue
		}
		kind, firstSeen := "new", now
		if exists {
			kind, firstSeen = "updated", previous.FirstSeen
		} else if baseline {
			kind = "baseline"
		}
		item := Item{ID: id, Source: source, Title: c.Title, URL: c.URL, Summary: c.Summary,
			Author: c.Author, PostID: c.PostID, Body: c.Body, Access: c.Access, HiddenLinks: c.HiddenLinks,
			Published: c.Published, FirstSeen: firstSeen, Observed: now, Kind: kind, Hash: hash}
		next.Items[id] = item
		changed = append(changed, item)
	}
	checkpoint.Initialized = true
	checkpoint.Crawl = cloneCrawl(checkpoint.Crawl)
	next.Sources[source] = checkpoint
	b, err := marshalSourceState(next)
	if err != nil {
		return nil, err
	}
	if err := atomicWrite(s.path, b); err != nil {
		return nil, err
	}
	s.data = next
	return changed, nil
}

func (s *Store) Items(limit int) []Item {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]Item, 0, len(s.data.Items))
	for _, item := range s.data.Items {
		if item.Enrichment != nil {
			enrichment := *item.Enrichment
			item.Enrichment = &enrichment
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Observed.Equal(items[j].Observed) {
			return items[i].ID < items[j].ID
		}
		return items[i].Observed.After(items[j].Observed)
	})
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items
}

func candidateHash(c Candidate) string {
	hash := digest(c.Title, c.URL, c.Summary, c.Published, c.Updated)
	if c.Author != "" || c.Body != "" {
		hash = digest(hash, c.Author, c.Body)
	}
	if c.Access != "" || c.HiddenLinks != 0 {
		hash = digest(hash, c.Access, fmt.Sprint(c.HiddenLinks))
	}
	return hash
}

type ContentCoverage struct {
	PublicTextPosts int `json:"public_text_posts"`
	PartialPosts    int `json:"partial_posts"`
	RestrictedPosts int `json:"restricted_posts"`
}

func (s *Store) contentCoverage(source string) ContentCoverage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var counts ContentCoverage
	for _, item := range s.data.Items {
		if item.Source != source || item.PostID == "" {
			continue
		}
		switch item.Access {
		case "restricted":
			counts.RestrictedPosts++
		case "partial":
			counts.PartialPosts++
		default:
			counts.PublicTextPosts++
		}
	}
	return counts
}

func atomicWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
