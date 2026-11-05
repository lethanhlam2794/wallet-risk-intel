// Package walletattribution adapts the public prettydeath/wallet-attribution
// GitHub CSV dataset (MIT license). One CSV per supported network; rows are
// community/provider attributions — never auto-verified.
//
// Adapter isolation: this package deliberately duplicates the small
// allowlist/redirect/bounded-retry helpers from the OFAC downloader rather
// than importing that package — provider quirks stay inside the owning
// adapter (AGENTS.md §3, §11).
package walletattribution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"checkwallet/internal/provider"
)

// defaultAllowedHosts is the SSRF allowlist — the dataset is GitHub-hosted.
var defaultAllowedHosts = []string{"raw.githubusercontent.com"}

// defaultMaxRetryWait caps a server-supplied Retry-After hint.
const defaultMaxRetryWait = 5 * time.Minute

// Downloader fetches the per-network CSVs to temp files with validation,
// bounded retry, per-file checksums, and a deterministic combined checksum.
// Circuit breaker is deferred (batch job, same rationale as OFAC).
type Downloader struct {
	baseURL      string
	files        []string // e.g. ["ethereum.csv","bsc.csv"]
	allowedHosts []string
	insecureHTTP bool // test-only escape hatch; production requires https
	httpClient   *http.Client
	maxBytes     int64 // per-file cap
	maxRetries   int
	maxRetryWait time.Duration
	backoff      time.Duration
	tmpDir       string
	onAttempt    func(attempt int, err error)
}

// DownloaderOption configures the downloader.
type DownloaderOption func(*Downloader)

// WithAllowedHosts overrides the SSRF allowlist (tests point at httptest).
func WithAllowedHosts(hosts ...string) DownloaderOption {
	return func(d *Downloader) { d.allowedHosts = hosts }
}

// WithInsecureHTTP permits plain-http URLs. Test-only.
func WithInsecureHTTP() DownloaderOption {
	return func(d *Downloader) { d.insecureHTTP = true }
}

// WithMaxBytes caps each file's download size (~32MB default covers the
// ~10MB current dataset with headroom).
func WithMaxBytes(n int64) DownloaderOption {
	return func(d *Downloader) { d.maxBytes = n }
}

// WithMaxRetries caps attempts per file.
func WithMaxRetries(n int) DownloaderOption {
	return func(d *Downloader) { d.maxRetries = n }
}

// WithMaxRetryWait caps a server-supplied Retry-After hint per attempt.
func WithMaxRetryWait(w time.Duration) DownloaderOption {
	return func(d *Downloader) { d.maxRetryWait = w }
}

// WithBackoff sets the exponential backoff base (tests shrink it).
func WithBackoff(d0 time.Duration) DownloaderOption {
	return func(d *Downloader) { d.backoff = d0 }
}

// WithTmpDir sets the temp directory for downloads.
func WithTmpDir(dir string) DownloaderOption {
	return func(d *Downloader) { d.tmpDir = dir }
}

// WithAttemptHook registers a per-attempt observability hook.
func WithAttemptHook(f func(attempt int, err error)) DownloaderOption {
	return func(d *Downloader) { d.onAttempt = f }
}

// NewDownloader validates baseURL against the SSRF allowlist. files are the
// artifact names appended to the base (e.g. "ethereum.csv").
func NewDownloader(baseURL string, files []string, opts ...DownloaderOption) (*Downloader, error) {
	d := &Downloader{
		allowedHosts: defaultAllowedHosts,
		maxBytes:     32 << 20,
		maxRetries:   3,
		maxRetryWait: defaultMaxRetryWait,
		backoff:      2 * time.Second,
		tmpDir:       os.TempDir(),
		onAttempt:    func(int, error) {},
	}
	for _, o := range opts {
		o(d)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("wallet-attribution downloader: no files configured")
	}
	d.httpClient = &http.Client{
		Timeout: 10 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("wallet-attribution download: too many redirects")
			}
			return d.checkURLAllowed(req.URL)
		},
	}
	base := strings.TrimRight(baseURL, "/")
	u, err := url.Parse(base + "/" + files[0])
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("wallet-attribution downloader: invalid base url %q", baseURL)
	}
	if err := d.checkURLAllowed(u); err != nil {
		return nil, err
	}
	d.baseURL = base
	d.files = files
	return d, nil
}

// checkURLAllowed enforces the scheme policy and SSRF host allowlist for the
// configured URL and every redirect hop.
func (d *Downloader) checkURLAllowed(u *url.URL) error {
	schemeOK := u.Scheme == "https" || (u.Scheme == "http" && d.insecureHTTP)
	if !schemeOK {
		return fmt.Errorf("wallet-attribution downloader: scheme %q not allowed", u.Scheme)
	}
	for _, h := range d.allowedHosts {
		if strings.EqualFold(u.Host, h) {
			return nil
		}
	}
	return fmt.Errorf("wallet-attribution downloader: host %q not in allowlist", u.Host)
}

// Download fetches every configured file. On any failure all temp files
// produced so far are removed and no snapshot is produced.
func (d *Downloader) Download(ctx context.Context) (provider.Snapshot, error) {
	files := make([]provider.File, 0, len(d.files))
	cleanup := func() {
		for _, f := range files {
			os.Remove(f.Path)
		}
	}
	for _, name := range d.files {
		f, err := d.downloadOne(ctx, name)
		if err != nil {
			cleanup()
			return provider.Snapshot{}, err
		}
		files = append(files, f)
	}
	return provider.Snapshot{
		Files:     files,
		Checksum:  provider.CombinedChecksum(files),
		SizeBytes: provider.TotalSize(files),
	}, nil
}

func (d *Downloader) downloadOne(ctx context.Context, name string) (provider.File, error) {
	var lastErr error
	for attempt := 1; attempt <= d.maxRetries; attempt++ {
		f, retryAfter, err := d.attempt(ctx, name)
		d.onAttempt(attempt, err)
		if err == nil {
			return f, nil
		}
		lastErr = err
		if re, ok := err.(*permanentError); ok {
			return provider.File{}, re
		}
		wait := d.backoffFor(attempt)
		if retryAfter > 0 {
			wait = min(retryAfter, d.maxRetryWait)
		}
		select {
		case <-ctx.Done():
			return provider.File{}, ctx.Err()
		case <-time.After(wait):
		}
	}
	return provider.File{}, fmt.Errorf("wallet-attribution download %q failed after %d attempts: %w",
		name, d.maxRetries, lastErr)
}

func (d *Downloader) backoffFor(attempt int) time.Duration {
	b := d.backoff << (attempt - 1)
	if b <= 2 {
		return time.Duration(b)
	}
	j := time.Duration(rand.Int63n(int64(b) / 2))
	return b - b/4 + j
}

type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

func (d *Downloader) attempt(ctx context.Context, name string) (provider.File, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.baseURL+"/"+name, nil)
	if err != nil {
		return provider.File{}, 0, &permanentError{fmt.Errorf("wallet-attribution download: build request: %w", err)}
	}
	req.Header.Set("Accept", "text/csv, text/plain, */*")
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return provider.File{}, 0, fmt.Errorf("wallet-attribution download %q: %w", name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
		if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
			return provider.File{}, 0, &permanentError{fmt.Errorf("wallet-attribution download %q: unexpected status %d", name, resp.StatusCode)}
		}
		return provider.File{}, retryAfter, fmt.Errorf("wallet-attribution download %q: status %d", name, resp.StatusCode)
	}

	f, err := os.CreateTemp(d.tmpDir, "wattr-*-"+name)
	if err != nil {
		return provider.File{}, 0, fmt.Errorf("wallet-attribution download: temp file: %w", err)
	}
	cleanup := func() {
		f.Close()
		os.Remove(f.Name())
	}

	h := sha256.New()
	written, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, d.maxBytes+1))
	if err != nil {
		cleanup()
		return provider.File{}, 0, fmt.Errorf("wallet-attribution download %q: stream interrupted: %w", name, err)
	}
	if written > d.maxBytes {
		cleanup()
		return provider.File{}, 0, &permanentError{fmt.Errorf("wallet-attribution download %q: size cap exceeded (%d > %d bytes)", name, written, d.maxBytes)}
	}
	if written == 0 {
		cleanup()
		return provider.File{}, 0, &permanentError{fmt.Errorf("wallet-attribution download %q: empty body", name)}
	}
	if err := f.Sync(); err != nil {
		cleanup()
		return provider.File{}, 0, fmt.Errorf("wallet-attribution download: fsync: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return provider.File{}, 0, fmt.Errorf("wallet-attribution download: close: %w", err)
	}
	return provider.File{
		Path:      filepath.Clean(f.Name()),
		Name:      name,
		Checksum:  hex.EncodeToString(h.Sum(nil)),
		SizeBytes: written,
		FinalURL:  resp.Request.URL.String(),
	}, 0, nil
}

func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return time.Until(t)
	}
	return 0
}
