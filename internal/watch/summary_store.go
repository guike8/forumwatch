package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
)

type summaryState struct {
	Version int                   `json:"version"`
	Entries map[string]Enrichment `json:"entries"`
}

func summaryKey(id, hash string) string { return id + ":" + hash }

// marshalSourceState keeps derived metadata out of source/checkpoint storage.
func marshalSourceState(state diskState) ([]byte, error) {
	state.Items = maps.Clone(state.Items)
	for id, item := range state.Items {
		item.Enrichment = nil
		state.Items[id] = item
	}
	return json.Marshal(state)
}

func (s *Store) loadEnrichments() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path + ".summaries")
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read local extracts: %w", err)
	}
	var state summaryState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("invalid local extracts (refusing to reset): %w", err)
	}
	if state.Version != 1 || state.Entries == nil {
		return fmt.Errorf("unsupported or incomplete local extract state")
	}
	for id, item := range s.data.Items {
		item.Enrichment = nil
		if enrichment, ok := state.Entries[summaryKey(id, item.Hash)]; ok {
			item.Enrichment = &enrichment
			if !hasSummary(item) {
				item.Enrichment = nil
			}
		}
		s.data.Items[id] = item
	}
	return nil
}

func (s *Store) summaryInput(id, hash string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.data.Items[id]
	if !ok || item.Access == "restricted" || item.Hash != hash || hasSummary(item) {
		return "", false
	}
	text := item.Body
	if text == "" {
		text = item.Summary
	}
	if text == "" {
		text = item.Title
	}
	return boundedSummaryText(text), true
}

// saveEnrichment serializes with Apply using the same store lock. Only current
// revisions are retained, so edits cannot accumulate an unbounded revision log.
// Publishing while still locked prevents an edit from overtaking this result.
func (s *Store) saveEnrichment(ctx context.Context, id string, enrichment Enrichment, hub *Hub) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.data.Items[id]
	if ctx.Err() != nil || !ok || item.Access == "restricted" || item.Hash != enrichment.SourceHash || hasSummary(item) {
		return nil
	}
	if !hub.HasPublished(id, item.Hash) {
		return nil
	}
	state := summaryState{Version: 1, Entries: make(map[string]Enrichment)}
	for existingID, existing := range s.data.Items {
		if hasSummary(existing) {
			state.Entries[summaryKey(existingID, existing.Hash)] = *existing.Enrichment
		}
	}
	state.Entries[summaryKey(id, item.Hash)] = enrichment
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err := atomicWrite(s.path+".summaries", data); err != nil {
		return err
	}
	item.Enrichment = &enrichment
	s.data.Items[id] = item
	hub.Publish(item)
	return nil
}
