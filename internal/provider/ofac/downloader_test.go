package ofac

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testDownloader(t *testing.T, serverURL string, opts ...DownloaderOption) *Downloader {
	t.Helper()
	u, err := url.Parse(serverURL)
	require.NoError(t, err)
	d, err := NewDownloader(serverURL,
		append(opts, WithAllowedHosts(u.Host), WithInsecureHTTP())...)
	require.NoError(t, err)
	return d
}

func TestDownload_Success(t *testing.T) {
	payload := "<?xml version=\"1.0\"?><Sanctions/>"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	d := testDownloader(t, srv.URL, WithTmpDir(t.TempDir()))
	snap, err := d.Download(context.Background())
	require.NoError(t, err)
	defer os.Remove(snap.Path)

	assert.Equal(t, int64(len(payload)), snap.SizeBytes)
	assert.Len(t, snap.Checksum, 64)
	data, err := os.ReadFile(snap.Path)
	require.NoError(t, err)
	assert.Equal(t, payload, string(data))
}

func TestDownload_Non200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	d := testDownloader(t, srv.URL, WithTmpDir(t.TempDir()), WithMaxRetries(2), WithBackoff(time.Millisecond))
	_, err := d.Download(context.Background())
	require.Error(t, err)
}

func TestDownload_4xxFailsFastNoRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	d := testDownloader(t, srv.URL, WithTmpDir(t.TempDir()), WithMaxRetries(5), WithBackoff(time.Millisecond))
	_, err := d.Download(context.Background())
	require.Error(t, err)
	assert.Equal(t, int32(1), calls.Load(), "4xx must not be retried")
}

func TestDownload_429HonorsRetryAfter(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte("<ok/>"))
	}))
	defer srv.Close()

	d := testDownloader(t, srv.URL, WithTmpDir(t.TempDir()), WithMaxRetries(3), WithBackoff(time.Millisecond))
	snap, err := d.Download(context.Background())
	require.NoError(t, err)
	defer os.Remove(snap.Path)
	assert.Equal(t, int32(2), calls.Load())
}

func TestDownload_SizeCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		_, _ = w.Write(make([]byte, 1024))
	}))
	defer srv.Close()

	d := testDownloader(t, srv.URL, WithTmpDir(t.TempDir()), WithMaxBytes(100))
	_, err := d.Download(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "size")
	// Temp file must be cleaned on failure.
	entries, _ := os.ReadDir(d.tmpDir)
	assert.Empty(t, entries)
}

func TestDownload_TempCleanedOnFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	dir := t.TempDir()
	d := testDownloader(t, srv.URL, WithTmpDir(dir), WithMaxRetries(1), WithBackoff(time.Millisecond))
	_, err := d.Download(context.Background())
	require.Error(t, err)
	entries, _ := os.ReadDir(dir)
	assert.Empty(t, entries)
}

func TestDownload_InterruptedStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.Header().Set("Content-Type", "text/xml")
		_, _ = w.Write([]byte("short"))
		// Handler returns before filling Content-Length → truncated body.
	}))
	defer srv.Close()

	d := testDownloader(t, srv.URL, WithTmpDir(t.TempDir()), WithMaxRetries(2), WithBackoff(time.Millisecond))
	_, err := d.Download(context.Background())
	require.Error(t, err)
}

func TestDownload_RespectsContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		_, _ = w.Write([]byte("x"))
	}))
	defer srv.Close()

	d := testDownloader(t, srv.URL, WithTmpDir(t.TempDir()), WithMaxRetries(1), WithBackoff(time.Millisecond))
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := d.Download(ctx)
	require.Error(t, err)
}

func TestNewDownloader_RejectsNonAllowlistedHost(t *testing.T) {
	_, err := NewDownloader("https://evil.example.com/x.xml", WithTmpDir(t.TempDir()))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "allowlist")
}

// Production requires https: a plaintext URL is rejected unless the
// explicit test-only escape hatch is set.
func TestNewDownloader_RequiresHTTPS(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	_, err := NewDownloader(srv.URL, WithAllowedHosts(host), WithTmpDir(t.TempDir()))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scheme")

	_, err = NewDownloader(srv.URL, WithAllowedHosts(host), WithInsecureHTTP(), WithTmpDir(t.TempDir()))
	require.NoError(t, err)
}

// A redirect to a host outside the allowlist must be refused — the
// allowlist is re-validated on every hop, not just the configured URL.
func TestDownload_RedirectToNonAllowlistedHostRejected(t *testing.T) {
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<Sanctions/>"))
	}))
	defer evil.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evil.URL+"/sdn.xml", http.StatusFound)
	}))
	defer redirector.Close()

	d := testDownloader(t, redirector.URL, WithTmpDir(t.TempDir()), WithMaxRetries(1))
	_, err := d.Download(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "allowlist")
}

// A same-host redirect is allowed and the final URL is recorded for audit.
func TestDownload_AllowlistedRedirectWorks(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/go" {
			http.Redirect(w, r, srv.URL+"/sdn.xml", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("<Sanctions/>"))
	}))
	defer srv.Close()

	d := testDownloader(t, srv.URL+"/go", WithTmpDir(t.TempDir()), WithMaxRetries(1))
	snap, err := d.Download(context.Background())
	require.NoError(t, err)
	defer os.Remove(snap.Path)
	assert.Equal(t, srv.URL+"/sdn.xml", snap.FinalURL)
}

// A hostile Retry-After hint is capped — it must never wedge the run.
func TestDownload_RetryAfterCapped(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "31536000") // one year
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte("<ok/>"))
	}))
	defer srv.Close()

	d := testDownloader(t, srv.URL, WithTmpDir(t.TempDir()), WithMaxRetries(3),
		WithBackoff(time.Millisecond), WithMaxRetryWait(20*time.Millisecond))
	start := time.Now()
	snap, err := d.Download(context.Background())
	require.NoError(t, err)
	defer os.Remove(snap.Path)
	assert.Equal(t, int32(2), calls.Load())
	assert.Less(t, time.Since(start), 2*time.Second, "Retry-After hint must be capped, not honored verbatim")
}
