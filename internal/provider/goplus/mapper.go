package goplus

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"checkwallet/internal/network"
	"checkwallet/internal/provider"
)

// categoryByFlag is the conservative flag→category table (phase-02).
// Confident mappings only — every other "1" flag degrades to OTHER so the
// signal is never dropped and no category is invented (spec §4.4:
// "không suy diễn category"; F8: honeypot_related_address demoted SCAM→OTHER
// because an association flag is not a scam verdict).
var categoryByFlag = map[string]string{
	"sanctioned":                            "SANCTIONED",
	"phishing_activities":                   "PHISHING",
	"money_laundering":                      "MONEY_LAUNDERING",
	"stealing_attack":                       "HACK",
	"mixer":                                 "MIXER",
	"darkweb_transactions":                  "DARKNET",
	"cybercrime":                            "OTHER",
	"financial_crime":                       "OTHER",
	"blacklist_doubt":                       "OTHER",
	"blackmail_activities":                  "OTHER",
	"fake_kyc":                              "OTHER",
	"fake_token":                            "OTHER",
	"fake_standard_interface":               "OTHER",
	"gas_abuse":                             "OTHER",
	"malicious_mining_activities":           "OTHER",
	"honeypot_related_address":              "OTHER",
	"number_of_malicious_contracts_created": "OTHER",
}

// signalOnlyFlags never produce observations — they are context fields
// (contract_address may be empty on a first upstream lookup; documented
// quirk, never retried).
var signalOnlyFlags = map[string]bool{
	"contract_address": true,
	"reinit":           true,
	"data_source":      true,
}

// mapResult converts the raw `result` object into Observations + Signals.
// "0"/"1" flags: only "1" emits an observation — a "0" flag is data, never
// a safe verdict (spec §4.4). Every raw flag lands in Signals verbatim.
func mapResult(net network.Network, normalized string, raw json.RawMessage,
	fetchedAt time.Time) ([]provider.Observation, map[string]any, *provider.LookupError) {

	signals := map[string]any{}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, signals, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, nil, provider.Errorf(provider.ErrMalformed, "goplus result: %v", err)
	}

	dataSource := ""
	if v, ok := fields["data_source"]; ok {
		_ = json.Unmarshal(v, &dataSource)
	}

	// Deterministic order for stable tests/output.
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var obs []provider.Observation
	for _, k := range keys {
		val := flagValue(fields[k])
		signals[k] = val
		if val != "1" || signalOnlyFlags[k] {
			continue
		}
		cat := categoryByFlag[k]
		if cat == "" {
			cat = "OTHER" // unmapped risk flag → OTHER, never dropped
		}
		evidence, _ := json.Marshal(map[string]string{
			"field": k, "value": val, "data_source": dataSource,
		})
		obs = append(obs, provider.Observation{
			Network:            net.Code,
			Address:            normalized,
			Category:           cat,
			Label:              humanizeFlag(k),
			Source:             SourceCode,
			OriginalSource:     dataSource,
			Confidence:         "unknown",
			VerificationStatus: "reported",
			ObservedAt:         fetchedAt,
			Evidence:           evidence,
		})
	}
	return obs, signals, nil
}

// flagValue renders a flag as a string: JSON strings pass through, other
// JSON values keep their literal text.
func flagValue(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(raw)
}

// humanizeFlag turns "stealing_attack" into "Stealing attack".
func humanizeFlag(flag string) string {
	s := strings.ReplaceAll(flag, "_", " ")
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
