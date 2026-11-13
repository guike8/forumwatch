package watch

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
)

// RunTerminal subscribes before returning so collection can start without losing
// the first events. Output runs separately from fetchers; after a slow-client
// disconnect it reconciles the latest persisted state, not an event history.
func RunTerminal(ctx context.Context, store *Store, hub *Hub, out io.Writer, showBaseline bool) <-chan error {
	done := make(chan error, 1)
	type revision struct {
		hash       string
		at         time.Time
		summarized bool
	}
	seen := make(map[string]revision)
	for _, item := range store.Items(0) {
		// Saved baselines must not become restart alerts, including delayed
		// extracts. Other pending extracts may arrive without replaying alerts.
		seen[item.ID] = revision{item.Hash, item.Observed, hasSummary(item) || item.Kind == "baseline"}
	}
	ch, unsubscribe := hub.Subscribe()
	go func() {
		defer close(done)
		defer func() { unsubscribe() }()
		emit := func(item Item) error {
			if !hub.Matches(item) {
				return nil
			}
			old, exists := seen[item.ID]
			if exists && old.hash == item.Hash {
				if old.summarized || !hasSummary(item) {
					return nil
				}
				old.summarized = true
				seen[item.ID] = old
				if item.Kind == "baseline" && !showBaseline {
					return nil
				}
				return WriteTerminalSummary(out, item)
			}
			if exists && !item.Observed.After(old.at) {
				return nil
			}
			seen[item.ID] = revision{item.Hash, item.Observed, hasSummary(item)}
			if item.Kind == "baseline" && !showBaseline {
				return nil
			}
			if err := WriteTerminalItem(out, item); err != nil {
				return err
			}
			// A slow-client reconciliation may first see an enriched revision.
			if hasSummary(item) {
				return WriteTerminalSummary(out, item)
			}
			return nil
		}
		for {
			select {
			case <-ctx.Done():
				done <- nil
				return
			case item, ok := <-ch:
				if ok {
					if err := emit(item); err != nil {
						done <- err
						return
					}
					continue
				}
				unsubscribe()
				ch, unsubscribe = hub.Subscribe()
				if _, err := fmt.Fprintln(out, "[RECOVERY] Terminal fell behind; reconciling latest saved revisions (intermediate edits may be missing)."); err != nil {
					done <- err
					return
				}
				items := store.Items(0)
				for i := len(items) - 1; i >= 0; i-- {
					if err := emit(items[i]); err != nil {
						done <- err
						return
					}
				}
			}
		}
	}()
	return done
}

func accessNotice(item Item) string {
	switch item.Access {
	case "restricted":
		return fmt.Sprintf("Access: RESTRICTED — %d hidden link(s); no readable body collected. Sign-in required by source.\n", item.HiddenLinks)
	case "partial":
		return fmt.Sprintf("Access: PARTIAL — public text only; %d hidden link(s) unavailable without sign-in.\n", item.HiddenLinks)
	default:
		return ""
	}
}

func WriteTerminalItem(out io.Writer, item Item) error {
	excerpt := terminalText(item.Summary)
	if excerpt == "" {
		excerpt = "No excerpt collected; open the original post."
	}
	metadata := ""
	if item.Author != "" {
		metadata = fmt.Sprintf("Author: %s | Published (source claim): %s\n",
			truncate(terminalText(item.Author), 200), truncate(terminalText(item.Published), 100))
	}
	label := strings.ToUpper(terminalText(item.Kind))
	if item.Access == "restricted" {
		label = "RESTRICTED/" + label
		excerpt = "Not collected — source requires sign-in to view the hidden content."
	}
	_, err := fmt.Fprintf(out, "\n[%s] %s | %s\n%s\n%s%sExcerpt: %s\nSource: %s\nClaim status: unverified\n",
		label, item.Observed.UTC().Format(time.RFC3339Nano),
		terminalText(item.Source), truncate(terminalText(item.Title), 500), metadata, accessNotice(item),
		truncate(excerpt, 1000), terminalText(item.URL))
	return err
}

func WriteTerminalSummary(out io.Writer, item Item) error {
	if !hasSummary(item) {
		return nil
	}
	_, err := fmt.Fprintf(out, "\n[SUMMARY] %s | %s\n%s\n%sLocal/extractive summary (not AI): %s\nSource: %s\nClaim status: unverified (including author/context claims)\n",
		item.Observed.UTC().Format(time.RFC3339Nano), terminalText(item.Source),
		truncate(terminalText(item.Title), 500), accessNotice(item), truncate(terminalText(item.Enrichment.Text), summaryTextRunes),
		terminalText(item.URL))
	return err
}

func terminalText(text string) string {
	// Forum content is untrusted: strip terminal escapes, line controls, bidi
	// formatting controls, and zero-width formatting before displaying it.
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return ' '
		}
		return r
	}, text)
	return strings.Join(strings.Fields(text), " ")
}
