package publicaml

import "encoding/json"

// enrichRequest is the per-address chain-specific wire shape (spec §4.6:
// batch requests can return different results — we never batch).
type enrichRequest struct {
	Addresses []addressRef `json:"addresses"`
	Include   []string     `json:"include"`
	TopN      int          `json:"top_n"`
}

type addressRef struct {
	WalletAddress string `json:"wallet_address"`
	Chain         string `json:"chain"`
}

// envelope mirrors the live-verified /v1/enrich response. Sub-fields are
// pointer/RawMessage tolerant — the live payload carries nulls freely.
type envelope struct {
	Entities []entity `json:"entities"`
	Error    string   `json:"error"`
}

type entity struct {
	WalletAddress     string          `json:"wallet_address"`
	Chain             string          `json:"chain"`
	AMLScore          *float64        `json:"aml_score"`
	AMLScoreBreakdown json.RawMessage `json:"aml_score_breakdown"`
	Label             string          `json:"label"`
	Category          string          `json:"category"`
	Sanctioned        *bool           `json:"sanctioned"`
	RiskLevel         string          `json:"risk_level"`
}

// breakdown is parsed from aml_score_breakdown with absent/null tolerance.
type breakdown struct {
	DirectAttribution json.RawMessage `json:"direct_attribution"`
	DirectExposure    *float64        `json:"direct_exposure"`
	IndirectExposure  *float64        `json:"indirect_exposure"`
	OutboundExposure  *exposureList   `json:"outbound_exposure"`
	ExposureDirection string          `json:"exposure_direction"`
	IncidentBonus     *float64        `json:"incident_bonus"`
}

type attribution struct {
	Category         string   `json:"category"`
	Score            *float64 `json:"score"`
	Source           string   `json:"source"`
	SourceConfidence *float64 `json:"source_confidence"`
}

type exposureList struct {
	Count int            `json:"count"`
	Items []exposureItem `json:"items"`
}

type exposureItem struct {
	Src       string   `json:"src"`
	Category  string   `json:"category"`
	Hops      int      `json:"hops"`
	Score     *float64 `json:"score"`
	Source    string   `json:"source"`
	Direction string   `json:"direction"`
}
