package walletattribution

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"checkwallet/internal/provider"
)

func hostOf(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
}

func TestDownloader_RejectsNonAllowlistedHost(t *testing.T) {
	_, err := NewDownloader("https://evil.example.com/data", []string{"ethereum.csv"})
	if err == nil || !strings.Contains(err.Error(), "allowlist") {
		t.Fatalf("expected allowlist error, got %v", err)
	}
}

func TestDownloader_RequiresHTTPS(t *testing.T) {
	_, err := NewDownloader("http://raw.githubusercontent.com/x", []string{"ethereum.csv"})
	if err == nil || !strings.Contains(err.Error(), "scheme") {
		t.Fatalf("expected scheme error, got %v", err)
	}
}

func TestDownloader_RejectsRedirectOffAllowlist(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://evil.example.com/x.csv", http.StatusFound)
	}))
	defer srv.Close()
	d, err := NewDownloader(srv.URL, []string{"ethereum.csv"},
		WithInsecureHTTP(), WithAllowedHosts(hostOf(t, srv.URL)),
		WithMaxRetries(1))
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Download(context.Background())
	if err == nil || !strings.Contains(err.Error(), "allowlist") {
		t.Fatalf("expected redirect allowlist error, got %v", err)
	}
}

func TestDownloader_SizeCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Repeat("a", 1024)))
	}))
	defer srv.Close()
	d, err := NewDownloader(srv.URL, []string{"ethereum.csv"},
		WithInsecureHTTP(), WithAllowedHosts(hostOf(t, srv.URL)),
		WithMaxBytes(64), WithMaxRetries(1))
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Download(context.Background())
	if err == nil || !strings.Contains(err.Error(), "size cap") {
		t.Fatalf("expected size cap error, got %v", err)
	}
}

func TestDownloader_MultiFileDeterministicChecksum(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ethereum.csv", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ETHDATA") })
	mux.HandleFunc("/bsc.csv", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "BSCDATA") })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	fetch := func(order []string) provider.Snapshot {
		d, err := NewDownloader(srv.URL, order,
			WithInsecureHTTP(), WithAllowedHosts(hostOf(t, srv.URL)))
		if err != nil {
			t.Fatal(err)
		}
		snap, err := d.Download(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range snap.Files {
			defer os.Remove(f.Path)
		}
		return snap
	}
	a := fetch([]string{"ethereum.csv", "bsc.csv"})
	b := fetch([]string{"bsc.csv", "ethereum.csv"})
	if a.Checksum != b.Checksum {
		t.Fatal("combined checksum depends on fetch order")
	}
	if a.Checksum == a.Files[0].Checksum {
		t.Fatal("multi-file checksum must differ from single-file checksum")
	}
}

func TestDownloader_RetriesOn5xx_PermanentOn4xx(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n < 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()
	d, err := NewDownloader(srv.URL, []string{"ethereum.csv"},
		WithInsecureHTTP(), WithAllowedHosts(hostOf(t, srv.URL)),
		WithMaxRetries(3), WithBackoff(time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	snap, err := d.Download(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("attempts = %d, want 2", n)
	}
	for _, f := range snap.Files {
		t.Cleanup(func() { os.Remove(f.Path) })
	}

	var m int
	srv4 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m++
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv4.Close()
	d2, err := NewDownloader(srv4.URL, []string{"ethereum.csv"},
		WithInsecureHTTP(), WithAllowedHosts(hostOf(t, srv4.URL)),
		WithMaxRetries(3), WithBackoff(time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d2.Download(context.Background()); err == nil {
		t.Fatal("expected 404 failure")
	}
	if m != 1 {
		t.Fatalf("4xx retried %d times, want 1 (permanent)", m)
	}
}
