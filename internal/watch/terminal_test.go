package watch

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTerminalSummaryFollowsAlertWithoutDuplicate(t *testing.T) {
	for _, showBaseline := range []bool{false, true} {
		t.Run(fmt.Sprint(showBaseline), func(t *testing.T) {
			c := testCollector(t)
			ctx, cancel := context.WithCancel(t.Context())
			out := &terminalCapture{notify: make(chan struct{}, 16)}
			done := RunTerminal(ctx, c.Store, c.Hub, out, showBaseline)
			now := time.Now().UTC()
			item := Item{ID: "new", Hash: "1", Kind: "new", Title: "Fresh post", Observed: now}
			c.Hub.Publish(item)
			item.Enrichment = &Enrichment{Method: "local/extractive", SourceHash: item.Hash, Text: "The author claims this happened.\x1b[2J‮"}
			c.Hub.Publish(item)
			c.Hub.Publish(item)
			baseline := item
			baseline.ID, baseline.Kind = "baseline", "baseline"
			baseline.Enrichment = nil
			c.Hub.Publish(baseline)
			baseline.Enrichment = item.Enrichment
			c.Hub.Publish(baseline)
			wantSummaries := 1
			if showBaseline {
				wantSummaries++
			}
			awaitTerminalText(t, out, func(text string) bool { return strings.Count(text, "[SUMMARY]") == wantSummaries })
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			text := out.String()
			if strings.Count(text, "[NEW]") != 1 || strings.Count(text, "[SUMMARY]") != wantSummaries || strings.Index(text, "[NEW]") > strings.Index(text, "[SUMMARY]") {
				t.Fatalf("duplicate/misordered output: %s", text)
			}
			if strings.ContainsAny(text, "\x1b‮") || !strings.Contains(text, "Local/extractive summary (not AI)") || !strings.Contains(text, "unverified (including author/context claims)") {
				t.Fatalf("unsafe or misleading summary output: %q", text)
			}
		})
	}
}

func TestTerminalRecoveredExtractDoesNotReplaySavedBaseline(t *testing.T) {
	for _, showBaseline := range []bool{false, true} {
		c := testCollector(t)
		items, err := c.Store.Apply("forum", []Candidate{{Key: "1", Title: "Saved baseline"}}, Checkpoint{}, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		out := &terminalCapture{notify: make(chan struct{}, 16)}
		done := RunTerminal(ctx, c.Store, c.Hub, out, showBaseline)
		baseline := items[0]
		baseline.Enrichment = &Enrichment{Method: "local/extractive", Text: "Saved baseline source text.", SourceHash: baseline.Hash}
		c.Hub.Publish(baseline)
		// A following distinct item serves as a barrier for the terminal loop.
		c.Hub.Publish(Item{ID: "barrier", Hash: "1", Kind: "new", Title: "Barrier post", Observed: time.Now().UTC()})
		awaitTerminalText(t, out, func(text string) bool { return strings.Contains(text, "Barrier post") })
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), "Saved baseline") || strings.Contains(out.String(), "[SUMMARY]") {
			t.Fatalf("saved baseline was re-alerted on restart: %s", out.String())
		}
	}
}

func TestTerminalIgnoresStaleSummaryAfterEdit(t *testing.T) {
	c := testCollector(t)
	ctx, cancel := context.WithCancel(t.Context())
	out := &terminalCapture{notify: make(chan struct{}, 16)}
	done := RunTerminal(ctx, c.Store, c.Hub, out, true)
	first := Item{ID: "post", Hash: "1", Kind: "new", Title: "First", Observed: time.Now().UTC()}
	c.Hub.Publish(first)
	latest := first
	latest.Hash, latest.Kind, latest.Title = "2", "updated", "Edited post"
	latest.Observed = latest.Observed.Add(time.Second)
	c.Hub.Publish(latest)
	first.Enrichment = &Enrichment{Method: "local/extractive", Text: "Stale claim.", SourceHash: first.Hash}
	c.Hub.Publish(first)
	latest.Enrichment = &Enrichment{Method: "local/extractive", Text: "Current claim.", SourceHash: latest.Hash}
	c.Hub.Publish(latest)
	awaitTerminalText(t, out, func(text string) bool { return strings.Contains(text, "Current claim.") })
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "Stale claim.") || strings.Count(out.String(), "[SUMMARY]") != 1 {
		t.Fatalf("stale summary reached terminal: %s", out.String())
	}
}

type terminalCapture struct {
	mu sync.Mutex
	bytes.Buffer
	notify chan struct{}
}

func (c *terminalCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	n, err := c.Buffer.Write(p)
	c.mu.Unlock()
	select {
	case c.notify <- struct{}{}:
	default:
	}
	return n, err
}

func (c *terminalCapture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Buffer.String()
}

func awaitTerminalText(t *testing.T, out *terminalCapture, ready func(string) bool) {
	t.Helper()
	timeout := time.NewTimer(3 * time.Second)
	defer timeout.Stop()
	for !ready(out.String()) {
		select {
		case <-out.notify:
		case <-timeout.C:
			t.Fatalf("timed out waiting for terminal output: %s", out.String())
		}
	}
}

func TestTerminalItemEscapesUntrustedControls(t *testing.T) {
	var out bytes.Buffer
	err := WriteTerminalItem(&out, Item{
		Kind: "new", Source: "forum", Title: "Title\x1b[2J\nForged‮",
		Summary: "Excerpt\x07\rhidden", URL: "https://example.com/post/1\x1b]52;clipboard\x07",
		Author: "name\x1b\nspoof‮", Published: "2026-11-13\rspoof",
		Observed: time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if strings.ContainsAny(text, "\x1b\x07\r‮") || strings.Contains(text, "\nForged") {
		t.Fatalf("unsafe terminal output: %q", text)
	}
	for _, want := range []string{"[NEW]", "forum", "Excerpt:", "Source: https://example.com/post/1", "unverified"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %q", want, text)
		}
	}
}

func TestTerminalStreamsNewItemsWithoutPrintingBaseline(t *testing.T) {
	c := testCollector(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	done := RunTerminal(ctx, c.Store, c.Hub, writer, false)
	now := time.Now().UTC()
	c.Hub.Publish(Item{ID: "baseline", Hash: "1", Kind: "baseline", Title: "Old post", Observed: now})
	c.Hub.Publish(Item{ID: "new", Hash: "2", Kind: "new", Title: "Fresh post", Source: "forum", URL: "https://example.com/new", Observed: now})
	data := make([]byte, 4096)
	n, err := reader.Read(data)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data[:n])
	if strings.Contains(text, "Old post") || !strings.Contains(text, "Fresh post") {
		t.Fatalf("wrong terminal output: %q", text)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal did not stop")
	}
}

func TestTerminalDoesNotReplaySavedItemsOnRestart(t *testing.T) {
	c := testCollector(t)
	items, err := c.Store.Apply("forum", []Candidate{{Key: "1", Title: "Old", URL: "https://example.com/1"}}, Checkpoint{}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	var out bytes.Buffer
	done := RunTerminal(ctx, c.Store, c.Hub, &out, true)
	c.Hub.Publish(items[0])
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("replayed saved item: %s", out.String())
	}
}
