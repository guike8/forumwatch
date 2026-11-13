package watch

import (
	"context"
	"log/slog"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	summaryInputBytes = 32 << 10
	summaryTextRunes  = 400
	summaryQueueSize  = 64
	summaryBatchSize  = 16
)

// Enrichment is derived local output, never a source revision or verification.
// SourceHash binds the extract to precisely the revision it describes.
type Enrichment struct {
	Method     string `json:"method"`
	Text       string `json:"text"`
	SourceHash string `json:"source_hash"`
}

func hasSummary(item Item) bool {
	return item.Access != "restricted" && item.Enrichment != nil && item.Enrichment.SourceHash == item.Hash &&
		item.Enrichment.Method == "local/extractive" && item.Enrichment.Text != ""
}

type summaryRevision struct {
	id   string
	hash string
}

// Summarizer has one worker and a fixed-capacity queue. Overflow is recovered by
// rescanning durable latest revisions; Enqueue never reads or writes the store.
type Summarizer struct {
	store *Store
	hub   *Hub
	queue chan summaryRevision
}

// NewSummarizer loads separately persisted extracts before collection starts.
func NewSummarizer(store *Store, hub *Hub) (*Summarizer, error) {
	if err := store.loadEnrichments(); err != nil {
		return nil, err
	}
	s := &Summarizer{store: store, hub: hub, queue: make(chan summaryRevision, summaryQueueSize)}
	// These revisions were already observed in an earlier process. Recovery may
	// enrich them without replaying an initial alert.
	hub.markSummaryRecovery(store.Items(0))
	return s, nil
}

// Enqueue must be called only after Hub.Publish of the initial item. It is safe
// for concurrent collectors and does not block on summary work or persistence.
func (s *Summarizer) Enqueue(item Item) {
	if item.Access == "restricted" || !s.hub.Matches(item) {
		return
	}
	select {
	case s.queue <- summaryRevision{item.ID, item.Hash}:
	default:
		// The durable revision remains pending for the worker's next rescan.
	}
}

// Run processes pending latest revisions until cancellation. Call once, before
// starting collection. Failed writes remain pending and are retried on a tick.
func (s *Summarizer) Run(ctx context.Context) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	s.recover(ctx)
	for {
		if ctx.Err() != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case revision := <-s.queue:
			s.process(ctx, revision)
		case <-ticker.C:
			s.recover(ctx)
		}
	}
}

func (s *Summarizer) recover(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	processed := 0
	for _, item := range s.store.Items(0) {
		if ctx.Err() != nil || processed >= summaryBatchSize {
			return
		}
		if item.Access == "restricted" || hasSummary(item) || !s.hub.HasPublished(item.ID, item.Hash) {
			continue
		}
		s.process(ctx, summaryRevision{item.ID, item.Hash})
		processed++
	}
}

func (s *Summarizer) process(ctx context.Context, revision summaryRevision) {
	if ctx.Err() != nil || !s.hub.HasPublished(revision.id, revision.hash) {
		return
	}
	text, ok := s.store.summaryInput(revision.id, revision.hash)
	if !ok {
		return
	}
	enrichment := Enrichment{Method: "local/extractive", Text: localExtract(text), SourceHash: revision.hash}
	if ctx.Err() != nil {
		return
	}
	if err := s.store.saveEnrichment(ctx, revision.id, enrichment, s.hub); err != nil {
		slog.Warn("local extract could not be saved; will retry", "error", err)
	}
}

// boundedSummaryText bounds bytes before normalization or sentence extraction.
func boundedSummaryText(text string) string {
	if len(text) > summaryInputBytes {
		text = text[:summaryInputBytes]
	}
	return strings.Clone(strings.ToValidUTF8(text, " "))
}

func localExtract(input string) string {
	text := terminalText(boundedSummaryText(input))
	if text == "" {
		return "No meaningful source text collected; open the original post."
	}
	// Keep at most the first two substantive sentences, in source order. This
	// deliberately does not infer facts, identities, intent, or credibility.
	var sentences []string
	start := 0
	for i, r := range text {
		end := i + utf8.RuneLen(r)
		if !strings.ContainsRune(".!?。！？", r) {
			continue
		}
		if r < utf8.RuneSelf && end < len(text) {
			next, _ := utf8.DecodeRuneInString(text[end:])
			if !unicode.IsSpace(next) {
				continue
			}
		}
		piece := strings.TrimSpace(text[start:end])
		if utf8.RuneCountInString(piece) >= 12 {
			sentences = append(sentences, piece)
		}
		start = end
		if len(sentences) == 2 {
			break
		}
	}
	if len(sentences) < 2 && start < len(text) {
		piece := strings.TrimSpace(text[start:])
		if utf8.RuneCountInString(piece) >= 12 || len(sentences) == 0 {
			sentences = append(sentences, piece)
		}
	}
	if len(sentences) == 0 {
		return truncate(text, summaryTextRunes)
	}
	return truncate(strings.Join(sentences, " "), summaryTextRunes)
}
