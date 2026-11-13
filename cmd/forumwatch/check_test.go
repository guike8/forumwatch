package main

import (
	"encoding/json"
	"flag"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guike8/forumwatch/internal/watch"
)

func TestCheckConfigDoesNotStartCollection(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv("NIGHTWIRE_CHECK_TEST_TOKEN", "")
	for _, test := range []struct {
		name, transport, excludeTitle, wantError string
		quietSales                               bool
	}{
		{name: "quiet preset", transport: "tor", quietSales: true},
		{name: "invalid RE2", transport: "tor", quietSales: true, excludeTitle: "(?=invalid)", wantError: "alert_filter.exclude_title"},
		{name: "require tor", transport: "direct", wantError: "require-tor refuses"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			state := filepath.Join(root, "unopened", "state.json")
			ready := filepath.Join(root, "ready.json")
			config := watch.Config{
				Listen: listener.Addr().String(), TokenEnv: "NIGHTWIRE_CHECK_TEST_TOKEN", StatePath: state,
				Sources: []watch.Source{{ID: "fixture", URL: "https://example.invalid/feed", Transport: test.transport, Format: "feed",
					AlertFilter: watch.AlertFilterSpec{QuietSales: test.quietSales, ExcludeTitle: test.excludeTitle}}},
			}
			data, err := json.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "config.json")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			previousArgs, previousFlags := os.Args, flag.CommandLine
			defer func() { os.Args, flag.CommandLine = previousArgs, previousFlags }()
			flag.CommandLine = flag.NewFlagSet("forumwatch-check", flag.ContinueOnError)
			os.Args = []string{"forumwatch", "-config", path, "-check-config", "-require-tor", "-ready-file", ready}
			err = run()
			if test.wantError == "" && err != nil {
				t.Fatal(err)
			}
			if test.wantError != "" && (err == nil || !strings.Contains(err.Error(), test.wantError)) {
				t.Fatalf("got %v, want error containing %q", err, test.wantError)
			}
			for _, untouched := range []string{filepath.Dir(state), ready} {
				if _, err := os.Stat(untouched); !os.IsNotExist(err) {
					t.Fatalf("validation touched %s: %v", untouched, err)
				}
			}
		})
	}
}
