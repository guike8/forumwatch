package watch

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// The test binary is the worker: no Python, browser, Tor, or network is used.
func TestBrowserWorkerHelper(t *testing.T) {
	index := -1
	for i, arg := range os.Args {
		if arg == "--browser-helper" {
			index = i
			break
		}
	}
	if index < 0 {
		return
	}
	mode := os.Args[index+1]
	if mode == "detached-child" {
		signal.Ignore(syscall.SIGTERM)
		os.WriteFile(os.Args[index+2], []byte(strconv.Itoa(os.Getpid())), 0600)
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	if mode == "driver" {
		signal.Ignore(syscall.SIGTERM)
		cmd := exec.Command(os.Args[0], "-test.run=^TestBrowserWorkerHelper$", "--", "--browser-helper", "detached-child", os.Args[index+2])
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if cmd.Start() != nil {
			os.Exit(5)
		}
		cmd.Wait()
		os.Exit(0)
	}
	if mode == "no-read" {
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	reader := bufio.NewReader(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	count := 0
	lastProfile := ""
	for {
		var input browserRequest
		line, err := reader.ReadBytes('\n')
		if err != nil || json.Unmarshal(line, &input) != nil {
			if mode == "graceful-eof" && lastProfile != "" {
				os.WriteFile(filepath.Join(lastProfile, "eof-cleanup"), []byte("closed"), 0600)
			}
			os.Exit(0)
		}
		lastProfile = input.ProfileDir
		count++
		if input.Proxy != "socks5://127.0.0.1:9050" || input.TimeoutMS != 40000 || input.MaxBodyBytes != maxBody || !filepath.IsAbs(input.ProfileDir) {
			os.Exit(3)
		}
		output := browserResponse{
			ID: input.ID, URL: input.URL, Status: 200,
			Headers: map[string]string{"X-Worker-Pid": strconv.Itoa(os.Getpid()), "X-Request-Count": strconv.Itoa(count)},
			Body:    "ok",
		}
		switch mode {
		case "descendant-idle", "descendant-active":
			marker := filepath.Join(input.ProfileDir, "descendant-pid")
			cmd := exec.Command(os.Args[0], "-test.run=^TestBrowserWorkerHelper$", "--", "--browser-helper", "driver", marker)
			if cmd.Start() != nil {
				os.Exit(6)
			}
			go cmd.Wait()
			for start := time.Now(); time.Since(start) < 3*time.Second; {
				if _, err := os.Stat(marker); err == nil {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if mode == "descendant-active" {
				time.Sleep(time.Minute)
			}
		case "malformed":
			fmt.Fprintln(os.Stdout, "secret-cookie malformed JSON")
			continue
		case "oversized-frame":
			fmt.Fprintln(os.Stdout, strings.Repeat("x", maxBrowserFrame+1))
			continue
		case "invalid-utf8":
			os.Stdout.Write([]byte{'"', 0xff, '"', '\n'})
			continue
		case "unterminated":
			data, _ := json.Marshal(output)
			os.Stdout.Write(data)
			os.Exit(0)
		case "oversized-body":
			output.Body = strings.Repeat("x", maxBody+1)
		case "escaped-limit":
			output.Body = strings.Repeat("\x00", maxBody)
		case "mismatched-id":
			output.ID++
		case "foreign-host":
			output.URL = "https://clearnet.example/private?token=secret-query"
		case "credentials":
			output.URL = "http://user:secret-password@forum.onion/"
		case "invalid-status":
			output.Status = 700
		case "invalid-header":
			output.Headers["X-Bad"] = "secret-cookie\r\nInjected: yes"
		case "error":
			output.Error = "secret-cookie secret-body secret-query"
			output.ErrorKind = "secret-kind"
		case "timeout":
			os.WriteFile(filepath.Join(input.ProfileDir, "ready"), []byte("ready"), 0600)
			time.Sleep(time.Minute)
		case "crash-once":
			marker := filepath.Join(input.ProfileDir, "crashed")
			if _, err := os.Stat(marker); errors.Is(err, os.ErrNotExist) {
				os.WriteFile(marker, []byte("yes"), 0600)
				os.Exit(4)
			}
		case "environment":
			data, _ := json.Marshal(os.Environ())
			output.Body = string(data)
		case "feed":
			output.Body = `<rss><channel><item><guid>x</guid><title>Post</title><link>/thread/1</link></item></channel></rss>`
			output.Headers["ETag"] = "must-not-cache"
			output.Headers["Last-Modified"] = "must-not-cache"
		case "bad-feed":
			output.Body = `<secret-body-token/>`
		case "rate-limit":
			output.Status = 429
			output.Headers["Retry-After"] = "120"
		case "final-path":
			output.URL = "https://FORUM.onion:443/redirected/"
		}
		fmt.Fprintln(os.Stderr, "secret-cookie stderr must never reach logs")
		if encoder.Encode(output) != nil {
			os.Exit(0)
		}
	}
}

func browserTestClient(t *testing.T, mode string) (*http.Client, *browserTransport) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	client, err := newBrowserClient(Source{ID: "../../unsafe/source", Transport: "tor"}, Config{
		TorProxy: "127.0.0.1:9050", BrowserProfileDir: filepath.Join(t.TempDir(), "profiles"),
		BrowserCommand: []string{executable, "-test.run=^TestBrowserWorkerHelper$", "--", "--browser-helper", mode},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.CloseIdleConnections)
	return client, client.Transport.(*browserTransport)
}

func TestBrowserReusesWorkerAndCloses(t *testing.T) {
	client, transport := browserTestClient(t, "normal")
	if transport.process != nil {
		t.Fatal("worker did not start lazily")
	}
	var pid string
	for i := 1; i <= 2; i++ {
		res, err := client.Get("http://forum.onion/?token=secret-query")
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if i == 1 {
			pid = res.Header.Get("X-Worker-Pid")
		}
		if res.Header.Get("X-Worker-Pid") != pid || res.Header.Get("X-Request-Count") != strconv.Itoa(i) {
			t.Fatal("requests did not reuse worker")
		}
	}
	if filepath.Base(transport.profileDir) == "source" || strings.Contains(filepath.Base(transport.profileDir), "unsafe") {
		t.Fatal("unsafe profile leaf")
	}
	if len(filepath.Base(transport.profileDir)) != 64 {
		t.Fatal("profile leaf is not a hash")
	}
	for _, dir := range []string{filepath.Dir(transport.profileDir), transport.profileDir} {
		info, err := os.Stat(dir)
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatalf("profile permissions: %v %v", info, err)
		}
	}
	p := transport.process
	client.CloseIdleConnections()
	select {
	case <-p.done:
	default:
		t.Fatal("worker was not reaped")
	}
	if transport.process != nil {
		t.Fatal("closed worker retained")
	}
	res, err := client.Get("http://forum.onion/")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.Header.Get("X-Worker-Pid") == pid || res.Header.Get("X-Request-Count") != "1" {
		t.Fatal("future poll did not restart after close")
	}
}

func TestBrowserRejectsBadResponses(t *testing.T) {
	for _, mode := range []string{"malformed", "invalid-utf8", "unterminated", "oversized-frame", "oversized-body", "mismatched-id", "foreign-host", "credentials", "invalid-status", "invalid-header", "error"} {
		t.Run(mode, func(t *testing.T) {
			client, transport := browserTestClient(t, mode)
			_, err := client.Get("http://forum.onion/")
			if err == nil {
				t.Fatal("accepted unsafe worker response")
			}
			if strings.Contains(err.Error(), "secret-") {
				t.Fatalf("worker details leaked: %v", err)
			}
			if transport.process != nil {
				t.Fatal("failed session was not discarded")
			}
		})
	}
}

func TestBrowserAcceptsMaximallyEscapedBody(t *testing.T) {
	client, _ := browserTestClient(t, "escaped-limit")
	res, err := client.Get("http://forum.onion/")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil || len(body) != maxBody {
		t.Fatalf("valid body at limit rejected: %d %v", len(body), err)
	}
}

func TestBrowserTimeoutAndCancellation(t *testing.T) {
	for _, mode := range []string{"deadline", "cancel", "blocked-write"} {
		t.Run(mode, func(t *testing.T) {
			workerMode := "timeout"
			if mode == "blocked-write" {
				workerMode = "no-read"
			}
			client, transport := browserTestClient(t, workerMode)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "deadline" || mode == "blocked-write" {
				ctx, cancel = context.WithTimeout(ctx, 150*time.Millisecond)
				defer cancel()
			}
			u := "http://forum.onion/"
			if mode == "blocked-write" {
				u += strings.Repeat("x", maxBody)
			}
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
			done := make(chan error, 1)
			started := time.Now()
			go func() {
				_, err := client.Do(req)
				done <- err
			}()
			if mode == "cancel" {
				deadline := time.NewTimer(3 * time.Second)
				defer deadline.Stop()
				ticker := time.NewTicker(5 * time.Millisecond)
				defer ticker.Stop()
			ready:
				for {
					select {
					case <-ticker.C:
						if _, err := os.Stat(filepath.Join(transport.profileDir, "ready")); err == nil {
							break ready
						}
					case <-deadline.C:
						t.Fatal("worker did not become ready")
					}
				}
				cancel()
			}
			select {
			case err := <-done:
				want := context.DeadlineExceeded
				if mode == "cancel" {
					want = context.Canceled
				}
				if !errors.Is(err, want) {
					t.Fatalf("wrong cancellation error: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("worker request did not stop")
			}
			if time.Since(started) > 3*time.Second || transport.process != nil {
				t.Fatal("canceled worker was not promptly discarded")
			}
		})
	}
}

func TestBrowserCrashRestartsOnlyOnNextRequest(t *testing.T) {
	client, transport := browserTestClient(t, "crash-once")
	if res, err := client.Get("http://forum.onion/"); err == nil {
		res.Body.Close()
		t.Fatal("crashed worker silently retried")
	}
	if transport.process != nil {
		t.Fatal("crashed worker retained")
	}
	res, err := client.Get("http://forum.onion/")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.Header.Get("X-Request-Count") != "1" {
		t.Fatal("unexpected requests in restarted worker")
	}
}

func TestBrowserSerializesConcurrentRequests(t *testing.T) {
	client, _ := browserTestClient(t, "normal")
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			res, err := client.Get("http://forum.onion/")
			if err != nil {
				t.Error(err)
				return
			}
			res.Body.Close()
		})
	}
	wg.Wait()
}

func TestBrowserEnvironmentAllowlist(t *testing.T) {
	t.Setenv("FORUMWATCH_TOKEN", "secret-token")
	t.Setenv("ANTHROPIC_API_KEY", "secret-key")
	t.Setenv("HTTPS_PROXY", "secret-proxy")
	t.Setenv("PLAYWRIGHT_BROWSERS_PATH", "/test/browser-cache")
	client, _ := browserTestClient(t, "environment")
	res, err := client.Get("http://forum.onion/")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var env []string
	if err := json.NewDecoder(res.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"PATH": true, "HOME": true, "TMPDIR": true, "LANG": true, "LC_ALL": true, "PLAYWRIGHT_BROWSERS_PATH": true}
	foundCache := false
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if !allowed[key] {
			t.Fatalf("unexpected child environment key %s", key)
		}
		foundCache = foundCache || entry == "PLAYWRIGHT_BROWSERS_PATH=/test/browser-cache"
	}
	if !foundCache {
		t.Fatal("browser cache path was not preserved")
	}
}

func TestBrowserFinalURLPolicy(t *testing.T) {
	for _, tc := range []struct {
		original, final string
		want            bool
	}{
		{"http://forum.onion/", "http://FORUM.onion./next", true},
		{"http://forum.onion/", "https://forum.onion:443/next", true},
		{"http://forum.onion:80/", "https://forum.onion/", true},
		{"http://forum.onion:8080/", "https://forum.onion:8080/", false},
		{"https://forum.onion:8443/", "http://forum.onion:8443/", false},
		{"https://forum.onion:8443/", "https://forum.onion:8443/next", true},
		{"http://forum.onion:8080/", "https://forum.onion/", false},
		{"https://forum.onion/", "http://forum.onion/", false},
		{"http://forum.onion/", "http://forum.onion:81/", false},
		{"http://forum.onion/", "https://other.onion/", false},
		{"http://forum.onion/", "https://clearnet.example/", false},
		{"http://forum.onion/", "http://user:password@forum.onion/", false},
		{"http://forum.onion/", "file:///tmp/page.html", false},
		{"http://forum.onion/", "http://forum.onion:99999/", false},
		{"http://bücher.example/", "http://xn--bcher-kva.example/", true},
	} {
		a, _ := url.Parse(tc.original)
		b, _ := url.Parse(tc.final)
		if got := browserFinalURL(a, b); got != tc.want {
			t.Errorf("%s -> %s: got %v", tc.original, tc.final, got)
		}
	}
}

type browserInspectTransport struct {
	inner http.RoundTripper
	t     *testing.T
}

func (b browserInspectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	for _, key := range []string{"User-Agent", "If-None-Match", "If-Modified-Since"} {
		if req.Header.Get(key) != "" {
			b.t.Errorf("browser request unexpectedly has %s", key)
		}
	}
	return b.inner.RoundTrip(req)
}

func TestBrowserPollNoValidatorsAndSanitizedFailures(t *testing.T) {
	for _, mode := range []string{"feed", "error", "bad-feed", "rate-limit"} {
		t.Run(mode, func(t *testing.T) {
			client, transport := browserTestClient(t, mode)
			client.Transport = browserInspectTransport{inner: transport, t: t}
			t.Cleanup(transport.CloseIdleConnections)
			c := testCollector(t)
			s := Source{ID: "forum", URL: "http://forum.onion/?token=secret-query", Transport: "tor", Fetcher: "browser", Format: "feed", UserAgent: "fake-fingerprint"}
			retry, err := c.poll(t.Context(), s, client)
			if mode == "feed" {
				if err != nil {
					t.Fatal(err)
				}
				checkpoint := c.Store.Checkpoint(s.ID)
				if !checkpoint.Initialized || checkpoint.ETag != "" || checkpoint.LastModified != "" {
					t.Fatal("browser checkpoint has HTTP validators or no baseline")
				}
				// Switching an existing HTTP source to browser must also ignore
				// and clear validators already present on disk.
				checkpoint.ETag = "old-http-tag"
				checkpoint.LastModified = "old-http-date"
				if _, err := c.Store.Apply(s.ID, nil, checkpoint, time.Now()); err != nil {
					t.Fatal(err)
				}
				if _, err := c.poll(t.Context(), s, client); err != nil {
					t.Fatal(err)
				}
				if checkpoint := c.Store.Checkpoint(s.ID); checkpoint.ETag != "" || checkpoint.LastModified != "" {
					t.Fatal("browser retained old HTTP validators")
				}
			} else {
				if err == nil || strings.Contains(err.Error(), "secret-") {
					t.Fatalf("missing or unsafe error: %v", err)
				}
				if c.Store.Checkpoint(s.ID).Initialized {
					t.Fatal("failed browser poll checkpointed")
				}
				if mode == "rate-limit" && retry != 2*time.Minute {
					t.Fatalf("lost Retry-After: %v", retry)
				}
			}
		})
	}
}

func TestBrowserFinalURLUsedForParsing(t *testing.T) {
	client, _ := browserTestClient(t, "final-path")
	res, err := client.Get("http://forum.onion/start")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.Request.URL.String() != "https://FORUM.onion:443/redirected/" {
		t.Fatalf("final URL lost: %s", res.Request.URL)
	}
}

func TestBrowserGracefulEOFAndDetachedDescendantCleanup(t *testing.T) {
	t.Run("graceful-eof", func(t *testing.T) {
		client, transport := browserTestClient(t, "graceful-eof")
		res, err := client.Get("http://forum.onion/")
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		client.CloseIdleConnections()
		if _, err := os.Stat(filepath.Join(transport.profileDir, "eof-cleanup")); err != nil {
			t.Fatal("worker was killed before stdin EOF cleanup")
		}
	})
	for _, mode := range []string{"descendant-idle", "descendant-active"} {
		t.Run(mode, func(t *testing.T) {
			client, transport := browserTestClient(t, mode)
			// A separate process deliberately resembles the owned descendant but
			// has no ancestry relationship. Shutdown must leave it alone.
			sibling := exec.Command(transport.command[0], "-test.run=^TestBrowserWorkerHelper$", "--", "--browser-helper", "detached-child", filepath.Join(t.TempDir(), "sibling-pid"))
			sibling.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			if err := sibling.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { sibling.Process.Kill(); sibling.Wait() })
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://forum.onion/", nil)
			finished := make(chan error, 1)
			go func() {
				res, err := client.Do(req)
				if res != nil {
					res.Body.Close()
				}
				finished <- err
			}()
			marker := filepath.Join(transport.profileDir, "descendant-pid")
			var pid int
			for start := time.Now(); time.Since(start) < 3*time.Second; {
				if data, err := os.ReadFile(marker); err == nil {
					pid, _ = strconv.Atoi(string(data))
					if pid > 1 {
						break
					}
				}
				time.Sleep(5 * time.Millisecond)
			}
			if pid <= 1 {
				t.Fatal("detached child did not start")
			}
			// Emergency cleanup is scoped to the child this helper just created.
			t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
			started := time.Now()
			if mode == "descendant-idle" {
				if err := <-finished; err != nil {
					t.Fatal(err)
				}
				client.CloseIdleConnections()
			} else {
				cancel()
				select {
				case err := <-finished:
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("lost request cancellation: %v", err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("active descendant cleanup exceeded bound")
				}
			}
			if time.Since(started) > 2*time.Second {
				t.Fatal("shutdown exceeded cleanup bound")
			}
			if sibling.Process.Signal(syscall.Signal(0)) != nil {
				t.Fatal("shutdown touched unrelated process")
			}
			// A dead child may briefly be a zombie until its driver/init reaps it.
			output, err := exec.Command("/bin/ps", "-p", strconv.Itoa(pid), "-o", "stat=").Output()
			if err == nil && !strings.HasPrefix(strings.TrimSpace(string(output)), "Z") {
				t.Fatalf("detached browser descendant survived: pid %d state %q", pid, output)
			}
		})
	}
}

func TestBrowserWorkerErrorKindsAreFixedMessages(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "http://forum.onion/", nil)
	for kind, want := range map[string]string{
		"authentication_required": "browser source requires authentication",
		"challenge":               "browser source is still serving a challenge",
		"timeout":                 "browser worker request timed out",
		"secret-kind":             "browser worker reported a fetch failure",
	} {
		_, err := browserHTTPResponse(req, 1, browserResponse{ID: 1, Error: "secret-body secret-cookie secret-query", ErrorKind: kind})
		if err == nil || err.Error() != want {
			t.Errorf("kind %q: got %v, want %q", kind, err, want)
		}
	}
}

func TestBrowserStartFailureAndSymlinkProfile(t *testing.T) {
	client, transport := browserTestClient(t, "normal")
	transport.command[0] = filepath.Join(t.TempDir(), "missing-worker")
	if _, err := client.Get("http://forum.onion/"); err == nil || transport.process != nil {
		t.Fatal("worker start failure was not closed")
	}
	client, transport = browserTestClient(t, "normal")
	if err := os.MkdirAll(filepath.Dir(transport.profileDir), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), transport.profileDir); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Get("http://forum.onion/"); err == nil {
		t.Fatal("accepted symlink profile")
	}
}
