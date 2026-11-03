package ofac

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

// defaultAllowedHosts is the SSRF allowlist (spec §16: fixed allowlist for
// provider endpoints).
var defaultAllowedHosts = []string{"sanctionslistservice.ofac.treas.gov"}

// Downloader downloads the OFAC Advanced XML to a temp file with validation,
// bounded retry, and checksum. Circuit breaker is explicitly deferred
// (single-source batch job — recorded decision).
type Downloader struct {
	url          string
	allowedHosts []string
	httpClient   *http.Client
	maxBytes     int64
	maxRetries   int
	backoff      time.Duration
	tmpDir       string
	// onAttempt is an observability hook (failure metrics land here).
	onAttempt func(attempt int, err error)
}

// DownloaderOption configures the downloader.
type DownloaderOption func(*Downloader)

// WithAllowedHosts overrides the SSRF allowlist (tests point at httptest).
func WithAllowedHosts(hosts ...string) DownloaderOption {
	return func(d *Downloader) { d.allowedHosts = hosts }
}

// WithTmpDir sets the temp directory for downloads.
func WithTmpDir(dir string) DownloaderOption {
	return func(d *Downloader) { d.tmpDir = dir }
}

// WithMaxBytes caps the download size.
func WithMaxBytes(n int64) DownloaderOption {
	return func(d *Downloader) { d.maxBytes = n }
}

// WithMaxRetries caps download attempts (bounded retry, spec §15).
func WithMaxRetries(n int) DownloaderOption {
	return func(d *Downloader) { d.maxRetries = n }
}

// WithBackoff sets the exponential backoff base (tests shrink it).
func WithBackoff(d0 time.Duration) DownloaderOption {
	return func(d *Downloader) { d.backoff = d0 }
}

// WithAttemptHook registers a per-attempt observability hook.
func WithAttemptHook(f func(attempt int, err error)) DownloaderOption {
	return func(d *Downloader) { d.onAttempt = f }
}

// NewDownloader validates the URL against the SSRF allowlist.
func NewDownloader(rawURL string, opts ...DownloaderOption) (*Downloader, error) {
	d := &Downloader{
		allowedHosts: defaultAllowedHosts,
		httpClient:   &http.Client{Timeout: 10 * time.Minute},
		maxBytes:     512 << 20,
		maxRetries:   3,
		backoff:      2 * time.Second,
		tmpDir:       os.TempDir(),
		onAttempt:    func(int, error) {},
	}
	for _, o := range opts {
		o(d)
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("ofac downloader: invalid url %q", rawURL)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, fmt.Errorf("ofac downloader: scheme %q not allowed", u.Scheme)
	}
	allowed := false
	for _, h := range d.allowedHosts {
		if strings.EqualFold(u.Host, h) {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, fmt.Errorf("ofac downloader: host %q not in allowlist", u.Host)
	}
	d.url = rawURL
	return d, nil
}

// Download fetches the XML with bounded retries, streams it to a temp file
// under the size cap, and returns a checksummed snapshot. On any failure the
// temp file is removed and no snapshot is produced.
func (d *Downloader) Download(ctx context.Context) (provider.Snapshot, error) {
	var lastErr error
	for attempt := 1; attempt <= d.maxRetries; attempt++ {
		snap, retryAfter, err := d.attempt(ctx)
		d.onAttempt(attempt, err)
		if err == nil {
			return snap, nil
		}
		lastErr = err
		if re, ok := err.(*permanentError); ok {
			return provider.Snapshot{}, re
		}
		wait := d.backoffFor(attempt)
		if retryAfter > 0 {
			wait = retryAfter
		}
		select {
		case <-ctx.Done():
			return provider.Snapshot{}, ctx.Err()
		case <-time.After(wait):
		}
	}
	return provider.Snapshot{}, fmt.Errorf("ofac download failed after %d attempts: %w", d.maxRetries, lastErr)
}

func (d *Downloader) backoffFor(attempt int) time.Duration {
	b := d.backoff << (attempt - 1)
	// jitter ±25%
	j := time.Duration(rand.Int63n(int64(b) / 2))
	return b - b/4 + j
}

type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

// attempt performs a single download try. Returns the retryAfter hint when
// the server sent one.
func (d *Downloader) attempt(ctx context.Context) (provider.Snapshot, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.url, nil)
	if err != nil {
		return provider.Snapshot{}, 0, &permanentError{fmt.Errorf("ofac download: build request: %w", err)}
	}
	req.Header.Set("Accept", "text/xml, application/xml")
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return provider.Snapshot{}, 0, fmt.Errorf("ofac download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
		if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
			return provider.Snapshot{}, 0, &permanentError{fmt.Errorf("ofac download: unexpected status %d", resp.StatusCode)}
		}
		return provider.Snapshot{}, retryAfter, fmt.Errorf("ofac download: status %d", resp.StatusCode)
	}

	f, err := os.CreateTemp(d.tmpDir, "ofac-sdn-*.xml")
	if err != nil {
		return provider.Snapshot{}, 0, fmt.Errorf("ofac download: temp file: %w", err)
	}
	cleanup := func() {
		f.Close()
		os.Remove(f.Name())
	}

	h := sha256.New()
	written, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, d.maxBytes+1))
	if err != nil {
		cleanup()
		return provider.Snapshot{}, 0, fmt.Errorf("ofac download: stream interrupted: %w", err)
	}
	if written > d.maxBytes {
		cleanup()
		return provider.Snapshot{}, 0, &permanentError{fmt.Errorf("ofac download: size cap exceeded (%d > %d bytes)", written, d.maxBytes)}
	}
	if written == 0 {
		cleanup()
		return provider.Snapshot{}, 0, fmt.Errorf("ofac download: empty body")
	}
	if err := f.Sync(); err != nil {
		cleanup()
		return provider.Snapshot{}, 0, fmt.Errorf("ofac download: fsync: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return provider.Snapshot{}, 0, fmt.Errorf("ofac download: close: %w", err)
	}
	return provider.Snapshot{
		Path:      filepath.Clean(f.Name()),
		Checksum:  hex.EncodeToString(h.Sum(nil)),
		SizeBytes: written,
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
