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
	ID        string    `json:"id"`
	Source    string    `json:"source"`
	Title     string    `json:"title"`
	URL       string    `json:"url"`
	Summary   string    `json:"summary,omitempty"`
	Published string    `json:"published,omitempty"`
	FirstSeen time.Time `json:"first_seen"`
	Observed  time.Time `json:"observed_at"`
	Kind      string    `json:"kind"`
	Hash      string    `json:"hash"`
}

type Checkpoint struct {
	Initialized  bool   `json:"initialized"`
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"last_modified,omitempty"`
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
	return s, nil
}

func (s *Store) Checkpoint(id string) Checkpoint {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.Sources[id]
}

func (s *Store) Apply(source string, candidates []Candidate, checkpoint Checkpoint, now time.Time) ([]Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	baseline := !s.data.Sources[source].Initialized
	next := diskState{Version: 1, Items: maps.Clone(s.data.Items), Sources: maps.Clone(s.data.Sources)}
	var changed []Item
	for _, c := range candidates {
		id := digest(source, c.Key)
		hash := digest(c.Title, c.URL, c.Summary, c.Published, c.Updated)
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
			Published: c.Published, FirstSeen: firstSeen, Observed: now, Kind: kind, Hash: hash}
		next.Items[id] = item
		changed = append(changed, item)
	}
	checkpoint.Initialized = true
	next.Sources[source] = checkpoint
	b, err := json.Marshal(next)
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
