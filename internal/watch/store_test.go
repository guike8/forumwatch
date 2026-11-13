package watch

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreBaselineDedupUpdatesAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	candidates := []Candidate{{Key: "1", Title: "First", URL: "https://example.com/1"}}
	items, err := s.Apply("forum", candidates, Checkpoint{ETag: "v1"}, now)
	if err != nil || len(items) != 1 || items[0].Kind != "baseline" {
		t.Fatalf("baseline: %+v %v", items, err)
	}
	s, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	items, err = s.Apply("forum", candidates, Checkpoint{ETag: "v1"}, now.Add(time.Second))
	if err != nil || len(items) != 0 {
		t.Fatalf("restart dedup: %+v %v", items, err)
	}
	candidates[0].Title = "Edited"
	candidates = append(candidates, Candidate{Key: "2", Title: "New", URL: "https://example.com/2"})
	items, err = s.Apply("forum", candidates, Checkpoint{ETag: "v2"}, now.Add(2*time.Second))
	if err != nil || len(items) != 2 || items[0].Kind != "updated" || items[1].Kind != "new" {
		t.Fatalf("updates: %+v %v", items, err)
	}
	if !items[0].FirstSeen.Equal(now) || s.Checkpoint("forum").ETag != "v2" {
		t.Fatal("lost first-seen or checkpoint")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("state permissions: %v", info.Mode())
	}
}

func TestStoreDoesNotAdvanceOnFailedWrite(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parent, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(filepath.Join(parent, "state.json"))
	if err == nil {
		// Some platforms report ENOTDIR rather than ENOENT in OpenStore.
		_, err = s.Apply("forum", []Candidate{{Key: "x", Title: "X"}}, Checkpoint{ETag: "v1"}, time.Now())
		if err == nil || len(s.Items(0)) != 0 || s.Checkpoint("forum").Initialized {
			t.Fatal("advanced state after write failure")
		}
		return
	}
	valid := filepath.Join(t.TempDir(), "state.json")
	s, err = OpenStore(valid)
	if err != nil {
		t.Fatal(err)
	}
	s.path = filepath.Join(parent, "state.json")
	_, err = s.Apply("forum", []Candidate{{Key: "x", Title: "X"}}, Checkpoint{ETag: "v1"}, time.Now())
	if err == nil || len(s.Items(0)) != 0 || s.Checkpoint("forum").Initialized {
		t.Fatal("advanced state after write failure")
	}
}

func TestStoreRejectsIncompleteState(t *testing.T) {
	for _, data := range []string{`{}`, `{"version":1}`, `{"version":1,"items":{}}`} {
		path := filepath.Join(t.TempDir(), "state.json")
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenStore(path); err == nil {
			t.Fatalf("accepted incomplete state: %s", data)
		}
	}
}

func TestStoreRefusesCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"version":1`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(path); err == nil {
		t.Fatal("silently reset corrupt state")
	}
}
