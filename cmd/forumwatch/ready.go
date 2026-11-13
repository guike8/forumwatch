package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
)

// A private, exclusive file binds an ephemeral API port to its exact child PID.
// No credentials are written here. A stale file is an error, never overwritten.
func writeReadyFile(path, address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return fmt.Errorf("ready-file requires a numeric loopback API listener")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("ready-file requires an assigned API port")
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("ready-file must be an absolute path in a private directory")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("ready-file directory must be private (0700) and not a symlink")
	}
	data, err := json.Marshal(struct {
		PID     int    `json:"pid"`
		Address string `json:"address"`
	}{os.Getpid(), address})
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".ready-*")
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
	// Linking publishes a complete file atomically without replacing any target.
	if err := os.Link(f.Name(), path); err != nil {
		return fmt.Errorf("publish ready-file: %w", err)
	}
	return nil
}
