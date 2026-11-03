// Package provider hosts external source adapters and the spec §13
// contracts. Only the interfaces this increment needs are defined — not
// every provider supports every interface (spec §13).
package provider

import "context"

// Snapshot is a validated downloaded artifact on local disk.
type Snapshot struct {
	Path      string
	Checksum  string // sha256 hex of the file bytes
	SizeBytes int64
}

// ImportResult reports staging-write counters for one run.
type ImportResult struct {
	Entities       int64
	Wallets        int64
	Programs       int64
	EntityPrograms int64
	References     int64
	Rejected       int64
}

// SnapshotSource is the spec §13 file-based bulk-source contract.
type SnapshotSource interface {
	Download(ctx context.Context) (Snapshot, error)
}
