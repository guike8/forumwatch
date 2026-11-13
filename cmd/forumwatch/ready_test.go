package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReadyFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "run", "ready.json")
	if err := writeReadyFile(path, "127.0.0.1:12345"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var ready struct {
		PID     int    `json:"pid"`
		Address string `json:"address"`
	}
	if err := json.Unmarshal(data, &ready); err != nil || ready.PID != os.Getpid() || ready.Address != "127.0.0.1:12345" {
		t.Fatalf("invalid readiness: %s, %v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("not private: %v, %v", info, err)
	}
	if err := writeReadyFile(path, "127.0.0.1:54321"); err == nil {
		t.Fatal("overwrote readiness from an earlier run")
	}
	after, _ := os.ReadFile(path)
	if string(data) != string(after) {
		t.Fatal("stale file changed")
	}
}

func TestReadyFileRejectsUnsafeTargets(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:1234", "localhost:1234", "example.com:1234", "127.0.0.1:0", "127.0.0.1:65536"} {
		t.Run(addr, func(t *testing.T) {
			if err := writeReadyFile(filepath.Join(t.TempDir(), "ready.json"), addr); err == nil {
				t.Fatalf("accepted %s", addr)
			}
		})
	}
	if err := writeReadyFile("relative.json", "127.0.0.1:1234"); err == nil {
		t.Fatal("accepted relative path")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := writeReadyFile(filepath.Join(dir, "ready.json"), "127.0.0.1:1234"); err == nil {
		t.Fatal("accepted public directory")
	}
	private := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(private, link); err != nil {
		t.Fatal(err)
	}
	if err := writeReadyFile(filepath.Join(link, "ready.json"), "127.0.0.1:1234"); err == nil {
		t.Fatal("accepted symlink directory")
	}
}
