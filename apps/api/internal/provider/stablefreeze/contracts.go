// Package stablefreeze scans Ethereum stablecoin issuer contracts for
// blacklist lifecycle events and materializes them as a JSONL snapshot
// artifact for the stablecoin_freeze ingestion pipeline.
//
// Delta semantics: absence from a scan window is NOT a signal — only
// explicit unfreeze events revoke, and USDT DestroyedBlackFunds keeps
// isBlackListed set so it is evidence, never a revocation.
package stablefreeze

import (
	"encoding/hex"
	"strings"

	"golang.org/x/crypto/sha3"
)

// SourceCode is the data_sources registry key.
const SourceCode = "stablecoin_freeze"

// Event types staged into stg_freeze_events (CHECK constraint).
const (
	EventFreeze   = "freeze"
	EventUnfreeze = "unfreeze"
	EventDestroy  = "destroy"
)

// EventSpec describes one contract event: how it maps to the delta model
// and where its subject address lives in the log entry.
type EventSpec struct {
	Name  string // upstream event name, e.g. "AddedBlackList"
	Type  string // freeze | unfreeze | destroy
	Topic string // keccak256 of the canonical signature (topic0)
	// Indexed is true when the subject address is topics[1] (USDC
	// Blacklisted/UnBlacklisted); false means the address is the
	// non-indexed argument in the data tail (USDT events).
	Indexed bool
}

// Contract is one watched issuer contract.
type Contract struct {
	Address string // lowercase 0x-prefixed
	Symbol  string // USDT | USDC
	Label   string // label stamped on produced wallet_labels rows
	Genesis int64  // verified deploy block (migration seed)
	Events  []EventSpec
}

// topic0 computes the keccak256 event-id constant from the canonical
// signature — self-documenting; the pinned values are asserted in tests.
func topic0(sig string) string {
	h := sha3.NewLegacyKeccak256()
	_, _ = h.Write([]byte(sig))
	return "0x" + hex.EncodeToString(h.Sum(nil))
}

// Registry is the watched set, ordered USDT then USDC.
func Registry() []Contract {
	return []Contract{
		{
			Address: "0xdac17f958d2ee523a2206206994597c13d831ec7",
			Symbol:  "USDT",
			Label:   "usdt_blacklist",
			Genesis: 4634748,
			Events: []EventSpec{
				{Name: "AddedBlackList", Type: EventFreeze,
					Topic: topic0("AddedBlackList(address)")},
				{Name: "RemovedBlackList", Type: EventUnfreeze,
					Topic: topic0("RemovedBlackList(address)")},
				{Name: "DestroyedBlackFunds", Type: EventDestroy,
					Topic: topic0("DestroyedBlackFunds(address,uint256)")},
			},
		},
		{
			Address: "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48",
			Symbol:  "USDC",
			Label:   "usdc_blacklist",
			Genesis: 6082465,
			Events: []EventSpec{
				{Name: "Blacklisted", Type: EventFreeze, Indexed: true,
					Topic: topic0("Blacklisted(address)")},
				{Name: "UnBlacklisted", Type: EventUnfreeze, Indexed: true,
					Topic: topic0("UnBlacklisted(address)")},
			},
		},
	}
}

// RegistryByAddress indexes the watched set by lowercase contract address.
func RegistryByAddress() map[string]Contract {
	m := make(map[string]Contract, 2)
	for _, c := range Registry() {
		m[c.Address] = c
	}
	return m
}

// EventByTopic indexes a contract's events by topic0.
func (c Contract) EventByTopic() map[string]EventSpec {
	m := make(map[string]EventSpec, len(c.Events))
	for _, e := range c.Events {
		m[strings.ToLower(e.Topic)] = e
	}
	return m
}

// TopicList returns the contract's topic0 set for eth_getLogs filtering —
// positional OR on topic0 (topics: [[a,b,c]]).
func (c Contract) TopicList() []string {
	out := make([]string, 0, len(c.Events))
	for _, e := range c.Events {
		out = append(out, e.Topic)
	}
	return out
}
