package walletattribution

import "time"

// CSVRow is one raw data row from the upstream attribution CSV.
type CSVRow struct {
	Line        int // 1-based physical line of the record start (diagnostics)
	Address     string
	Network     string
	Entity      string
	Label       string
	Category    string
	Source      string
	SourceURL   string
	Confidence  string
	LastUpdated string
}

// ParsedLabel is a validated, normalized row ready for staging.
type ParsedLabel struct {
	Address           string // CSV spelling verbatim
	NormalizedAddress string
	NetworkCode       string
	EntityName        string // "" → NULL entity link at publish
	Label             string
	UpstreamCategory  string // verbatim
	CategoryCode      string // UPPER(upstream_category)
	SourceField       string // upstream `source` column
	SourceURL         string
	Confidence        string // high|medium|low passthrough, else unknown
	ObservedAt        *time.Time
	DateUnparsed      bool
	Fingerprint       string
	Evidence          map[string]any
}

// FileStat reports per-file import counters for pipeline reconciliation:
// every data row is either staged or rejected; an insert that affects 0 rows
// is a fingerprint collision (schema-drift posture → hard-fail at validate).
type FileStat struct {
	Name        string
	NetworkCode string
	Parsed      int64 // data rows seen (all)
	Attempted   int64 // rows reaching the staging insert
	Staged      int64 // rows actually inserted (collisions excluded)
	Rejected    int64
}

// ImportMeta is the importer's per-run report consumed by the labels
// pipeline's Validate step.
type ImportMeta struct {
	Files []FileStat
}
