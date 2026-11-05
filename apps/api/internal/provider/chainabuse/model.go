// Package chainabuse is the on-demand Chainabuse adapter (spec §4.5).
// PROVISIONAL: the wire model is coded from TRM API docs — it cannot be
// live-verified without a partner key (free tier = 10 calls/month, unusable
// for on-demand enrichment). The adapter ships DISABLED unless
// CHAINABUSE_API_KEY is configured; a disabled adapter emits zero traffic
// and reports a typed disabled error. Community reports are always
// verification_status "reported" — never auto-verified.
package chainabuse

// report mirrors the documented /v0/reports item shape (provisional —
// see package doc).
type report struct {
	ID          int64        `json:"id"`
	CreatedAt   string       `json:"createdAt"`
	Addresses   []reportAddr `json:"addresses"`
	Category    string       `json:"category"`
	Subcategory string       `json:"subcategory"`
	Description string       `json:"description"`
	Trusted     bool         `json:"trusted"`
	URL         string       `json:"url"`
}

type reportAddr struct {
	Address string `json:"address"`
	Chain   string `json:"chain"`
}
