// Package provider hosts external source adapters and the spec §13
// contracts. Only the interfaces this increment needs are defined — not
// every provider supports every interface (spec §13).
package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
)

// File is one validated artifact file on local disk.
type File struct {
	Path      string // local file path
	Name      string // logical name (e.g. "ethereum.csv") — canonical ordering key
	Checksum  string // sha256 hex of this file's bytes
	SizeBytes int64
	FinalURL  string // effective URL after redirects — audit trail for SSRF checks
}

// Snapshot is a validated downloaded artifact set on local disk. F2: a
// source artifact may be multiple files (wallet-attribution ships one CSV
// per network); Checksum composes the set deterministically.
type Snapshot struct {
	Files     []File // ≥1
	Checksum  string // combined checksum — see CombinedChecksum
	SizeBytes int64  // summed across files
}

// CombinedChecksum deterministically composes a snapshot checksum over the
// provided files. One file → its own checksum verbatim (single-file
// semantics unchanged); N files → sha256 over the per-file sha256 hex
// concatenated in filename order, so fetch order cannot change the result.
func CombinedChecksum(files []File) string {
	if len(files) == 1 {
		return files[0].Checksum
	}
	sums := make([]string, 0, len(files))
	names := make([]string, 0, len(files))
	byName := map[string]string{}
	for _, f := range files {
		names = append(names, f.Name)
		byName[f.Name] = f.Checksum
	}
	sort.Strings(names)
	for _, n := range names {
		sums = append(sums, byName[n])
	}
	h := sha256.New()
	for _, s := range sums {
		h.Write([]byte(s))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// TotalSize sums file sizes for a snapshot.
func TotalSize(files []File) int64 {
	var n int64
	for _, f := range files {
		n += f.SizeBytes
	}
	return n
}

// ImportResult reports staging-write counters for one run.
type ImportResult struct {
	Entities       int64
	Wallets        int64
	Programs       int64
	EntityPrograms int64
	References     int64
	Labels         int64 // wallet labels staged (wallet-attribution source)
	Rejected       int64
}

// SnapshotSource is the spec §13 file-based bulk-source contract.
type SnapshotSource interface {
	Download(ctx context.Context) (Snapshot, error)
}
