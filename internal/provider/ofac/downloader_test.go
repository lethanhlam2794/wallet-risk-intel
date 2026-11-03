package ofac

import (
	"context"
	"net/http"
	"net/http/httptest"
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
	host := strings.TrimPrefix(strings.TrimPrefix(serverURL, "http://"), "https://")
	d, err := NewDownloader(serverURL, append(opts, WithAllowedHosts(host))...)
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
