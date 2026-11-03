package chainabuse

import (
	"encoding/json"
	"time"

	"checkwallet/internal/network"
	"checkwallet/internal/provider"
)

// categoryByUpstream normalizes documented Chainabuse categories; unmapped
// categories land on OTHER with the raw value in evidence — never dropped,
// never invented.
var categoryByUpstream = map[string]string{
	"scam":             "SCAM",
	"phishing":         "PHISHING",
	"ransomware":       "RANSOMWARE",
	"hack":             "HACK",
	"stolen funds":     "HACK",
	"stolen_funds":     "HACK",
	"mixer":            "MIXER",
	"darknet":          "DARKNET",
	"gambling":         "GAMBLING",
	"money_laundering": "MONEY_LAUNDERING",
	"sanctions":        "SANCTIONED",
	"terrorism":        "TERRORISM",
	"blackmail":        "OTHER",
	"other":            "OTHER",
}

// mapReports converts reports into reported-only observations. The raw
// report rides in evidence verbatim.
func mapReports(net network.Network, normalized string, reports []report,
	fetchedAt time.Time) []provider.Observation {

	obs := make([]provider.Observation, 0, len(reports))
	for _, r := range reports {
		cat := categoryByUpstream[normalizeCat(r.Category)]
		if cat == "" {
			cat = "OTHER"
		}
		evidence, _ := json.Marshal(r)
		obs = append(obs, provider.Observation{
			Network:            net.Code,
			Address:            normalized,
			Category:           cat,
			Label:              r.Category,
			Entity:             r.Subcategory,
			Source:             SourceCode,
			OriginalSource:     SourceCode,
			SourceURL:          r.URL,
			Confidence:         "unknown",
			VerificationStatus: "reported", // community reports never auto-verify
			ObservedAt:         fetchedAt,
			Evidence:           evidence,
		})
	}
	return obs
}

func normalizeCat(c string) string {
	out := make([]byte, 0, len(c))
	for i := 0; i < len(c); i++ {
		ch := c[i]
		if ch >= 'A' && ch <= 'Z' {
			ch += 'a' - 'A'
		}
		out = append(out, ch)
	}
	return string(out)
}
