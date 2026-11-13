package watch

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/net/http/httpguts"
)

// JSON may escape each body byte as six ASCII bytes. Leave bounded space for
// headers and metadata as well; the decoded UTF-8 body has its own stricter cap.
const maxBrowserFrame = 6*maxBody + 64<<10

type browserRequest struct {
	ID           uint64 `json:"id"`
	URL          string `json:"url"`
	Proxy        string `json:"proxy"`
	ProfileDir   string `json:"profile_dir"`
	TimeoutMS    int    `json:"timeout_ms"`
	MaxBodyBytes int    `json:"max_body_bytes"`
}

type browserResponse struct {
	ID        uint64            `json:"id"`
	URL       string            `json:"url"`
	Status    int               `json:"status"`
	Headers   map[string]string `json:"headers"`
	Body      string            `json:"body"`
	Error     string            `json:"error"`
	ErrorKind string            `json:"error_kind"`
}

// A transport belongs to exactly one source. The gate serializes its worker's
// JSON-lines exchanges, while mu lets shutdown interrupt an active exchange.
// Failed exchanges are never retried here: the collector owns backoff.
type browserTransport struct {
	command    []string
	proxy      string
	profileDir string
	gate       chan struct{}
	mu         sync.Mutex
	process    *browserProcess
	nextID     uint64 // protected by gate
}

func newBrowserClient(s Source, c Config) (*http.Client, error) {
	if s.Transport != "tor" || len(c.BrowserCommand) == 0 || !filepath.IsAbs(c.BrowserCommand[0]) {
		return nil, errors.New("browser fetcher requires tor and an absolute browser_command")
	}
	root := c.BrowserProfileDir
	if root == "" {
		root = "data/browser-profiles"
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, errors.New("invalid browser profile directory")
	}
	leaf := fmt.Sprintf("%x", sha256.Sum256([]byte(s.ID)))
	transport := &browserTransport{
		command: append([]string(nil), c.BrowserCommand...), proxy: "socks5://" + c.TorProxy,
		profileDir: filepath.Join(root, leaf), gate: make(chan struct{}, 1),
	}
	return &http.Client{
		Transport: transport, Timeout: 45 * time.Second,
		// The worker navigates redirects itself. Never navigate again in Go.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}

func (t *browserTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(req.Context(), 45*time.Second)
	defer cancel()
	if !acquire(ctx, t.gate) {
		return nil, ctx.Err()
	}
	defer func() { <-t.gate }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !webURL(req.URL) || req.URL.User != nil {
		return nil, errors.New("invalid browser request URL")
	}
	t.mu.Lock()
	p := t.process
	if p != nil {
		select {
		case <-p.done:
			p.close()
			p = nil
		default:
		}
	}
	if p == nil {
		var err error
		p, err = startBrowserProcess(t.command, t.profileDir)
		if err != nil {
			t.process = nil
			t.mu.Unlock()
			return nil, err
		}
		t.process = p
	}
	t.mu.Unlock()
	t.nextID++
	input := browserRequest{
		ID: t.nextID, URL: req.URL.String(), Proxy: t.proxy, ProfileDir: t.profileDir,
		TimeoutMS: 40000, MaxBodyBytes: maxBody,
	}
	type result struct {
		output browserResponse
		err    error
	}
	finished := make(chan result, 1)
	go func() {
		output, err := p.exchange(input)
		finished <- result{output, err}
	}()
	var output browserResponse
	select {
	case r := <-finished:
		if r.err != nil {
			t.discard(p)
			return nil, r.err
		}
		output = r.output
	case <-ctx.Done():
		t.discard(p) // closing pipes unblocks both reads and writes
		<-finished   // join the exchange; do not leak a goroutine per timeout
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		t.discard(p)
		return nil, err
	}
	res, err := browserHTTPResponse(req, input.ID, output)
	if err != nil {
		t.discard(p)
		return nil, err
	}
	return res, nil
}

func (t *browserTransport) discard(p *browserProcess) {
	t.mu.Lock()
	if t.process == p {
		t.process = nil
	}
	t.mu.Unlock()
	p.close()
}

func (t *browserTransport) CloseIdleConnections() {
	t.mu.Lock()
	p := t.process
	t.process = nil
	t.mu.Unlock()
	if p != nil {
		p.close()
	}
}

type browserProcess struct {
	cancel  context.CancelFunc
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	reader  *bufio.Reader
	started chan struct{}
	exited  chan struct{}
	done    chan struct{}
	startOK bool // startOK and pid are published by closing started
	pid     int
	once    sync.Once
}

func startBrowserProcess(command []string, profileDir string) (*browserProcess, error) {
	// Neither raw source IDs nor symlink profile directories may escape the root.
	for _, dir := range []string{filepath.Dir(profileDir), profileDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, errors.New("cannot create private browser profile")
		}
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("browser profile must be a private directory")
		}
		if err := os.Chmod(dir, 0700); err != nil {
			return nil, errors.New("cannot secure browser profile")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Env = browserEnvironment()
	// Give the worker/driver a private group. Chromium itself starts detached;
	// shutdown below also records descendant-owned groups before signaling.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, errors.New("cannot open browser worker input")
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		cancel()
		return nil, errors.New("cannot open browser worker output")
	}
	// nil Stderr goes to the null device, never to logs or an unbounded buffer.
	p := &browserProcess{
		cancel: cancel, stdin: stdin, stdout: stdout, reader: bufio.NewReader(stdout),
		started: make(chan struct{}), exited: make(chan struct{}), done: make(chan struct{}),
	}
	go func() {
		p.startOK = cmd.Start() == nil
		if p.startOK {
			p.pid = cmd.Process.Pid
		}
		close(p.started)
		if p.startOK {
			_ = cmd.Wait()
		}
		close(p.exited)
		p.shutdown()
		close(p.done)
	}()
	return p, nil
}

func (p *browserProcess) close() {
	p.shutdown()
	<-p.done
}

func (p *browserProcess) shutdown() {
	p.once.Do(func() {
		select {
		case <-p.started:
		default:
			p.cancel() // also bound cancellation while exec is starting
			<-p.started
		}
		if p.startOK {
			// Patchright launches Chromium detached from Python/Node. Snapshot
			// exact parent ancestry before EOF/TERM can reparent those children.
			groups := browserProcessGroups(p.pid)
			p.stdin.Close()  // idle Python can run finally/session.close on EOF
			p.stdout.Close() // unblock an active response reader
			timer := time.NewTimer(150 * time.Millisecond)
			select {
			case <-p.exited:
			case <-timer.C:
			}
			timer.Stop()
			for group := range groups {
				_ = syscall.Kill(-group, syscall.SIGTERM)
			}
			// Node's SIGTERM/pipe-EOF handlers close detached Chromium. Keep
			// this bounded even when Python is stuck in navigation or a child
			// ignores TERM, then kill only groups established as ours.
			deadline := time.Now().Add(150 * time.Millisecond)
			for browserGroupsAlive(groups) && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			for group := range browserProcessGroups(p.pid) {
				groups[group] = true
			}
			for group := range groups {
				_ = syscall.Kill(-group, syscall.SIGKILL)
			}
		}
		p.cancel()
		p.stdin.Close()
		p.stdout.Close()
	})
}

func browserGroupsAlive(groups map[int]bool) bool {
	for group := range groups {
		if syscall.Kill(-group, 0) == nil {
			return true
		}
	}
	return false
}

// Discover only descendants of our private worker group. Never match process
// names, command lines, browser profiles, or arbitrary existing browser groups.
// ps exposes the required numeric ancestry on both macOS and Linux; its output
// is bounded and never logged. Failure still permits scoped worker-group kill.
func browserProcessGroups(root int) map[int]bool {
	groups := map[int]bool{root: true}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/ps", "-A", "-o", "pid=", "-o", "ppid=", "-o", "pgid=")
	cmd.Env = browserEnvironment()
	stdout, err := cmd.StdoutPipe()
	if err != nil || cmd.Start() != nil {
		return groups
	}
	data, readErr := io.ReadAll(io.LimitReader(stdout, 1<<20))
	if len(data) == 1<<20 {
		cancel()
	}
	if cmd.Wait() != nil || readErr != nil {
		return groups
	}
	type process struct{ pid, parent, group int }
	var processes []process
	owned := map[int]bool{root: true}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		pid, e1 := strconv.Atoi(fields[0])
		parent, e2 := strconv.Atoi(fields[1])
		group, e3 := strconv.Atoi(fields[2])
		if e1 != nil || e2 != nil || e3 != nil || pid <= 1 || group <= 1 {
			continue
		}
		processes = append(processes, process{pid, parent, group})
		if group == root {
			owned[pid] = true // includes Node if Python has just crashed
		}
	}
	for changed := true; changed; {
		changed = false
		for _, p := range processes {
			if owned[p.parent] && !owned[p.pid] {
				owned[p.pid] = true
				changed = true
			}
		}
	}
	for _, p := range processes {
		// A detached group may be killed only if its leader is in this tree.
		if owned[p.pid] && owned[p.group] {
			groups[p.group] = true
		}
	}
	return groups
}

func (p *browserProcess) exchange(input browserRequest) (browserResponse, error) {
	var output browserResponse
	<-p.started
	if !p.startOK {
		return output, errors.New("browser worker could not start")
	}
	if err := json.NewEncoder(p.stdin).Encode(input); err != nil {
		return output, errors.New("browser worker write failed")
	}
	line, err := readBrowserFrame(p.reader)
	if err != nil {
		return output, errors.New("browser worker response framing failed")
	}
	if !utf8.Valid(line) || json.Unmarshal(line, &output) != nil {
		return output, errors.New("browser worker returned invalid JSON")
	}
	return output, nil
}

func readBrowserFrame(reader *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		part, err := reader.ReadSlice('\n')
		if len(line)+len(part) > maxBrowserFrame {
			return nil, errors.New("browser response frame too large")
		}
		line = append(line, part...)
		if err == nil {
			return line, nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return nil, err
		}
	}
}

func browserHTTPResponse(req *http.Request, id uint64, output browserResponse) (*http.Response, error) {
	// Worker error text/kind may contain page content, cookies, or private URLs.
	// Never surface it in collector errors, status, or logs.
	if output.ID != id {
		return nil, errors.New("browser worker response ID mismatch")
	}
	if output.Error != "" {
		switch output.ErrorKind {
		case "authentication_required":
			return nil, errors.New("browser source requires authentication")
		case "challenge":
			return nil, errors.New("browser source is still serving a challenge")
		case "timeout":
			return nil, errors.New("browser worker request timed out")
		default:
			return nil, errors.New("browser worker reported a fetch failure")
		}
	}
	if output.Status < 100 || output.Status > 599 {
		return nil, errors.New("browser worker returned invalid HTTP status")
	}
	if len(output.Body) > maxBody || !utf8.ValidString(output.Body) {
		return nil, errors.New("browser worker body exceeds limit or is not UTF-8")
	}
	final, err := url.Parse(output.URL)
	if err != nil || !browserFinalURL(req.URL, final) {
		return nil, errors.New("browser worker returned an unsafe final URL")
	}
	headers := make(http.Header, len(output.Headers))
	for key, value := range output.Headers {
		if !httpguts.ValidHeaderFieldName(key) || !httpguts.ValidHeaderFieldValue(value) {
			return nil, errors.New("browser worker returned invalid headers")
		}
		headers.Set(key, value)
	}
	finalReq := req.Clone(req.Context())
	finalReq.URL = final
	return &http.Response{
		StatusCode: output.Status, Status: fmt.Sprintf("%d %s", output.Status, http.StatusText(output.Status)),
		Header: headers, Body: io.NopCloser(bytes.NewBufferString(output.Body)),
		ContentLength: int64(len(output.Body)), Request: finalReq,
	}, nil
}

func browserFinalURL(original, final *url.URL) bool {
	if !webURL(final) || final.User != nil {
		return false
	}
	host := func(u *url.URL) string {
		if ip := net.ParseIP(u.Hostname()); ip != nil {
			return ip.String()
		}
		h, _ := canonicalHost(u.Hostname())
		return h
	}
	if host(original) != host(final) {
		return false
	}
	port := func(u *url.URL) int {
		if u.Port() != "" {
			n, err := strconv.Atoi(u.Port())
			if err != nil || n < 1 || n > 65535 {
				return -1
			}
			return n
		}
		if u.Scheme == "https" {
			return 443
		}
		return 80
	}
	a, b := port(original), port(final)
	return a > 0 && b > 0 && ((original.Scheme == final.Scheme && a == b) || (original.Scheme == "http" && final.Scheme == "https" && a == 80 && b == 443))
}

func browserEnvironment() []string {
	var env []string
	for _, key := range []string{"PATH", "HOME", "TMPDIR", "LANG", "LC_ALL", "PLAYWRIGHT_BROWSERS_PATH"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}
