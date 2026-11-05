package publicaml

import (
	"encoding/json"
	"time"

	"checkwallet/internal/network"
	"checkwallet/internal/provider"
)

// categoryByUpstream normalizes PublicAML category vocabulary; anything
// unmapped lands on OTHER with the raw value preserved in evidence — never
// invent a category, never drop a signal.
var categoryByUpstream = map[string]string{
	"sanction":   "SANCTIONED",
	"sanctions":  "SANCTIONED",
	"scam":       "SCAM",
	"phishing":   "PHISHING",
	"hack":       "HACK",
	"mixer":      "MIXER",
	"ransomware": "RANSOMWARE",
	"gambling":   "GAMBLING",
	"cex":        "EXCHANGE",
	"exchange":   "EXCHANGE",
	"darknet":    "DARKNET",
}

// mapEntity splits one entity into direct attribution observations,
// indirect exposures, provider score, and raw signals — spec §4.6 keeps
// direct attribution and indirect exposure strictly separated; a graph
// path never implies ownership.
func mapEntity(net network.Network, normalized string, e entity,
	fetchedAt time.Time) (*provider.LookupResult, *provider.LookupError) {

	res := &provider.LookupResult{
		Source:         SourceCode,
		Network:        net.Code,
		Address:        normalized,
		RequestedChain: requestedChain(net),
		Signals:        map[string]any{},
		FetchedAt:      fetchedAt,
	}
	if e.AMLScore != nil {
		score := *e.AMLScore
		res.ProviderScore = &score
	}
	res.Signals["label"] = e.Label
	res.Signals["category"] = e.Category
	res.Signals["risk_level"] = e.RiskLevel
	res.Signals["chain"] = e.Chain
	res.Signals["wallet_address"] = e.WalletAddress
	if e.Sanctioned != nil {
		res.Signals["sanctioned"] = *e.Sanctioned
	}

	// Entity-level sanctioned flag — a provider claim, kept distinct from
	// our first-party OFAC match and never merged with it.
	if e.Sanctioned != nil && *e.Sanctioned {
		evidence, _ := json.Marshal(map[string]any{
			"field": "sanctioned", "value": true, "entity_label": e.Label,
		})
		res.Observations = append(res.Observations, provider.Observation{
			Network:            net.Code,
			Address:            normalized,
			Category:           "SANCTIONED",
			Label:              "Sanctioned",
			Entity:             e.Label,
			Source:             SourceCode,
			OriginalSource:     SourceCode,
			Confidence:         "unknown",
			VerificationStatus: "reported",
			ObservedAt:         fetchedAt,
			Evidence:           evidence,
		})
	}

	var bd breakdown
	if len(e.AMLScoreBreakdown) > 0 && string(e.AMLScoreBreakdown) != "null" {
		if err := json.Unmarshal(e.AMLScoreBreakdown, &bd); err != nil {
			return nil, provider.Errorf(provider.ErrMalformed,
				"publicaml breakdown: %v", err)
		}
	}
	if bd.DirectExposure != nil {
		res.Signals["direct_exposure"] = *bd.DirectExposure
	}
	if bd.IndirectExposure != nil {
		res.Signals["indirect_exposure"] = *bd.IndirectExposure
	}
	if bd.ExposureDirection != "" {
		res.Signals["exposure_direction"] = bd.ExposureDirection
	}
	if bd.IncidentBonus != nil {
		res.Signals["incident_bonus"] = *bd.IncidentBonus
	}

	// Direct attribution → observation; evidence is the raw breakdown slice.
	if len(bd.DirectAttribution) > 0 && string(bd.DirectAttribution) != "null" {
		var da attribution
		if err := json.Unmarshal(bd.DirectAttribution, &da); err != nil {
			return nil, provider.Errorf(provider.ErrMalformed,
				"publicaml direct_attribution: %v", err)
		}
		cat := categoryByUpstream[da.Category]
		if cat == "" {
			cat = "OTHER"
		}
		res.Observations = append(res.Observations, provider.Observation{
			Network:            net.Code,
			Address:            normalized,
			Category:           cat,
			Label:              da.Category,
			Entity:             e.Label,
			Source:             SourceCode,
			OriginalSource:     da.Source,
			Confidence:         "unknown",
			VerificationStatus: "reported",
			ObservedAt:         fetchedAt,
			Evidence:           json.RawMessage(bd.DirectAttribution),
		})
	}

	// Indirect exposure items ride in Exposures only — never folded into
	// Observations/attribution (§4.6; graph data ≠ ownership proof).
	if bd.OutboundExposure != nil {
		for _, it := range bd.OutboundExposure.Items {
			dir := it.Direction
			if dir == "" {
				dir = "outbound"
			}
			res.Exposures = append(res.Exposures, provider.Exposure{
				Counterparty: it.Src,
				Category:     it.Category,
				Hops:         it.Hops,
				Score:        it.Score,
				Direction:    dir,
			})
		}
		res.Signals["outbound_exposure_count"] = bd.OutboundExposure.Count
	}
	return res, nil
}

// requestedChain maps our network code to the literal chain token sent
// upstream (F1 — upper(Code) would yield "ETHEREUM"; the static map is the
// contract, and the token is recorded in provider_observations).
func requestedChain(net network.Network) string {
	return chainByCode[net.Code]
}

var chainByCode = map[string]string{
	"ethereum": "ETH",
	"bsc":      "BSC",
}
