package solanarpc

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"checkwallet/internal/provider"
)

// getTxResult is the subset of getTransaction{jsonParsed} the mapper
// reads. Everything else stays undecoded.
type getTxResult struct {
	Slot      int64  `json:"slot"`
	BlockTime *int64 `json:"blockTime"` // absent on genesis-era entries — never fabricated
	Meta      *struct {
		Err               json.RawMessage `json:"err"`
		InnerInstructions []struct {
			Index        int  `json:"index"`
			Instructions []ix `json:"instructions"`
		} `json:"innerInstructions"`
		PreTokenBalances  []tokenBalance `json:"preTokenBalances"`
		PostTokenBalances []tokenBalance `json:"postTokenBalances"`
	} `json:"meta"`
	Transaction *struct {
		Message struct {
			AccountKeys  []json.RawMessage `json:"accountKeys"`
			Instructions []ix              `json:"instructions"`
		} `json:"message"`
	} `json:"transaction"`
}

// tokenBalance is one pre/postTokenBalances element — the ATA→owner and
// ATA→mint attribution evidence (semantics §d/§e).
type tokenBalance struct {
	AccountIndex int    `json:"accountIndex"`
	Mint         string `json:"mint"`
	Owner        string `json:"owner"`
	ProgramID    string `json:"programId"`
}

// ix is one instruction in either form: parsed (program+parsed) or raw
// (programId+accounts+data — Parsed nil, out of scope).
type ix struct {
	Program   string `json:"program"`
	ProgramID string `json:"programId"`
	Parsed    *struct {
		Type string          `json:"type"`
		Info json.RawMessage `json:"info"`
	} `json:"parsed"`
}

// transferInfo covers the parsed-info shapes of system transfer/
// transferWithSeed and SPL transfer/transferChecked (Token + Token-2022
// share the type names).
type transferInfo struct {
	Source      string          `json:"source"`
	Destination string          `json:"destination"`
	Lamports    json.RawMessage `json:"lamports"`
	Amount      string          `json:"amount"`
	Mint        string          `json:"mint"`
	TokenAmount *struct {
		Amount string `json:"amount"`
	} `json:"tokenAmount"`
}

// mapTx extracts transfer rows from one fetched transaction. Execution
// order is preserved: outer[i], then its inner set, then outer[i+1] —
// deterministic per-stream occurrence ordering feeds the content-hash
// identity. meta.err ⇒ the whole tx emits nothing (rolled-back inner
// instructions are not evidence — semantics §e).
func mapTx(tx *getTxResult, s sigRef, wantSol, wantSPL bool, head int64) ([]provider.Transfer, error) {
	if tx.Meta == nil || tx.Transaction == nil {
		return nil, provider.Errorf(provider.ErrMalformed,
			"solanarpc tx %s: missing meta/transaction", s.Sig)
	}
	if len(tx.Meta.Err) > 0 && string(tx.Meta.Err) != "null" {
		return nil, nil // failed tx — excluded entirely
	}
	keys, err := accountKeys(tx)
	if err != nil {
		return nil, err
	}
	attrs := balanceAttrs(tx, keys)

	var out []provider.Transfer
	emitIx := func(inst ix) error {
		tr, ok, err := mapIx(inst, tx, s, attrs, wantSol, wantSPL, head)
		if err != nil {
			return err
		}
		if ok {
			out = append(out, tr)
		}
		return nil
	}
	inners := map[int][]ix{}
	for _, set := range tx.Meta.InnerInstructions {
		inners[set.Index] = set.Instructions
	}
	for i, inst := range tx.Transaction.Message.Instructions {
		if err := emitIx(inst); err != nil {
			return nil, err
		}
		for _, inner := range inners[i] {
			if err := emitIx(inner); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// mapIx converts one instruction into a Transfer (ok=false = out of
// scope, honestly skipped). In-scope ixs missing required evidence are
// malformed — never fabricated.
func mapIx(inst ix, tx *getTxResult, s sigRef, attrs attrs,
	wantSol, wantSPL bool, head int64) (provider.Transfer, bool, error) {

	if inst.Parsed == nil {
		return provider.Transfer{}, false, nil
	}
	var ti transferInfo
	switch {
	case inst.Program == "system" && wantSol &&
		(inst.Parsed.Type == "transfer" || inst.Parsed.Type == "transferWithSeed"):
		if err := json.Unmarshal(inst.Parsed.Info, &ti); err != nil {
			return provider.Transfer{}, false, provider.Errorf(provider.ErrMalformed,
				"solanarpc tx %s: bad %s info", s.Sig, inst.Parsed.Type)
		}
		lamports, err := rawInt(ti.Lamports)
		if err != nil {
			return provider.Transfer{}, false, provider.Errorf(provider.ErrMalformed,
				"solanarpc tx %s: bad lamports %s", s.Sig, ti.Lamports)
		}
		if ti.Source == "" || ti.Destination == "" || lamports == "" {
			return provider.Transfer{}, false, provider.Errorf(provider.ErrMalformed,
				"solanarpc tx %s: system transfer missing fields", s.Sig)
		}
		return provider.Transfer{
			TxHash:         s.Sig, // base58 verbatim — ToLower corruption is the documented §15 blocker
			Stream:         streamSOL,
			Type:           provider.TransferKindNative,
			From:           ti.Source,
			To:             ti.Destination,
			Asset:          "",
			AmountRaw:      lamports,
			BlockNumber:    s.Slot,
			BlockTimestamp: txTime(tx),
			Confirmations:  confs(head, s.Slot),
			IsError:        false, // failed txs never reach mapping
		}, true, nil

	case (inst.Program == "spl-token" || inst.Program == "spl-token-2022") && wantSPL &&
		(inst.Parsed.Type == "transfer" || inst.Parsed.Type == "transferChecked"):
		if err := json.Unmarshal(inst.Parsed.Info, &ti); err != nil {
			return provider.Transfer{}, false, provider.Errorf(provider.ErrMalformed,
				"solanarpc tx %s: bad %s info", s.Sig, inst.Parsed.Type)
		}
		amount := ti.Amount
		if ti.TokenAmount != nil && ti.TokenAmount.Amount != "" {
			amount = ti.TokenAmount.Amount
		}
		if ti.Source == "" || ti.Destination == "" || !digits(amount) {
			return provider.Transfer{}, false, provider.Errorf(provider.ErrMalformed,
				"solanarpc tx %s: %s missing fields (src=%q dst=%q amt=%q)",
				s.Sig, inst.Parsed.Type, ti.Source, ti.Destination, amount)
		}
		// Mint: transferChecked carries it; bare transfer resolves via the
		// tokenBalances account→mint attribution (§e). Missing both ⇒
		// the row is skipped honestly, never invented.
		mint := ti.Mint
		if mint == "" {
			mint = attrs.mintOf(ti.Destination)
		}
		if mint == "" {
			mint = attrs.mintOf(ti.Source)
		}
		if mint == "" {
			return provider.Transfer{}, false, nil
		}
		// Owner attribution via tokenBalances where evidenced, else the
		// raw token account — never invented (semantics §d).
		from, to := ti.Source, ti.Destination
		if o := attrs.ownerOf(ti.Source); o != "" {
			from = o
		}
		if o := attrs.ownerOf(ti.Destination); o != "" {
			to = o
		}
		return provider.Transfer{
			TxHash:         s.Sig,
			Stream:         streamSPL,
			Type:           provider.TransferKindToken,
			From:           from,
			To:             to,
			Asset:          mint,
			AmountRaw:      amount,
			BlockNumber:    s.Slot,
			BlockTimestamp: txTime(tx),
			Confirmations:  confs(head, s.Slot),
			IsError:        false,
		}, true, nil
	}
	return provider.Transfer{}, false, nil
}

// accountKeys decodes message.accountKeys — jsonParsed emits objects
// ({pubkey,signer,writable,source}); a bare-string element (raw encoding
// drift) is accepted by content, never assumed.
func accountKeys(tx *getTxResult) ([]string, error) {
	raw := tx.Transaction.Message.AccountKeys
	keys := make([]string, 0, len(raw))
	for i, k := range raw {
		var obj struct {
			Pubkey string `json:"pubkey"`
		}
		var s string
		if err := json.Unmarshal(k, &obj); err == nil && obj.Pubkey != "" {
			keys = append(keys, obj.Pubkey)
			continue
		}
		if err := json.Unmarshal(k, &s); err == nil && s != "" {
			keys = append(keys, s)
			continue
		}
		return nil, provider.Errorf(provider.ErrMalformed,
			"solanarpc: accountKeys[%d] undecodable", i)
	}
	return keys, nil
}

// attrs is the accountIndex→{mint,owner} attribution derived from
// pre/postTokenBalances (post preferred — the landed state; pre as
// fallback for accounts closed in-tx).
type attrs struct {
	owner map[string]string
	mint  map[string]string
}

func balanceAttrs(tx *getTxResult, keys []string) attrs {
	a := attrs{owner: map[string]string{}, mint: map[string]string{}}
	apply := func(bals []tokenBalance, overwrite bool) {
		for _, b := range bals {
			if b.AccountIndex < 0 || b.AccountIndex >= len(keys) {
				continue
			}
			acct := keys[b.AccountIndex]
			if b.Owner != "" && (overwrite || a.owner[acct] == "") {
				a.owner[acct] = b.Owner
			}
			if b.Mint != "" && (overwrite || a.mint[acct] == "") {
				a.mint[acct] = b.Mint
			}
		}
	}
	apply(tx.Meta.PreTokenBalances, false) // pre fills gaps
	apply(tx.Meta.PostTokenBalances, true) // post wins
	return a
}

func (a attrs) ownerOf(acct string) string { return a.owner[acct] }
func (a attrs) mintOf(acct string) string  { return a.mint[acct] }

// rawInt normalizes an amount that upstream may send as a JSON number or
// string into a raw integer decimal string — never a float.
func rawInt(rm json.RawMessage) (string, error) {
	s := strings.Trim(strings.TrimSpace(string(rm)), `"`)
	if s == "" || s == "null" {
		return "", nil
	}
	if !digits(s) {
		return "", fmt.Errorf("non-integer amount %q", s)
	}
	return s, nil
}

func digits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// txTime = the tx's blockTime; absent ⇒ zero time ⇒ NULL downstream
// (identity.go:192-196). Never fabricated (INV-S4).
func txTime(tx *getTxResult) time.Time {
	if tx.BlockTime == nil {
		return time.Time{}
	}
	return time.Unix(*tx.BlockTime, 0).UTC()
}

// confs = head − slot + 1 — slot DISTANCE, not depth (skipped slots
// inflate it; semantics §g). 0 when either is unknown → 'pending'.
func confs(head, slot int64) int64 {
	if head <= 0 || slot <= 0 || head < slot {
		return 0
	}
	return head - slot + 1
}
