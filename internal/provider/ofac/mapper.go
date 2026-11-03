package ofac

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"checkwallet/internal/wallet"
)

// currencyFromFeatureType extracts the normalized currency code from a
// "Digital Currency Address - <CUR>" feature type name. Membership is
// resolved dynamically from the document's FeatureTypeValues — never
// hardcoded IDs (shape report).
func currencyFromFeatureType(featureTypeName string) string {
	name := strings.TrimSpace(featureTypeName)
	if !strings.HasPrefix(name, digitalCurrencyPrefix) {
		return ""
	}
	return strings.ToUpper(strings.TrimSpace(strings.TrimPrefix(name, digitalCurrencyPrefix)))
}

// resolveListing applies the verified currency-code × address-format rules:
//   - ETH + EVM format → eip155:1 (ethereum), CHAIN_SPECIFIC
//   - BSC|BNB + EVM format → eip155:56 (bsc), CHAIN_SPECIFIC
//   - other codes on EVM format → network unevidenced → UNRESOLVED, ""
//   - non-EVM format → chain-specific on a chain we do not index → "", CHAIN_SPECIFIC
//
// An ETH listing never becomes a BSC listing; an unresolved row never gains
// an invented network.
func resolveListing(currency, listedAddress string) (networkCode, matchScope string) {
	if !wallet.IsEVMAddress(strings.TrimSpace(listedAddress)) {
		return "", ScopeChainSpecific
	}
	switch strings.ToUpper(strings.TrimSpace(currency)) {
	case "ETH":
		return "ethereum", ScopeChainSpecific
	case "BSC", "BNB":
		return "bsc", ScopeChainSpecific
	default:
		return "", ScopeUnresolved
	}
}

// mapParty converts one decoded DistinctParty into a ParsedEntity.
func mapParty(party *xmlDistinctParty, rs *referenceSets) *ParsedEntity {
	p := &party.Profile
	ent := &ParsedEntity{
		ExternalID: strings.TrimSpace(p.ID),
		Details:    map[string]any{"fixed_ref": party.FixedRef},
	}
	if sub, err := strconv.ParseInt(strings.TrimSpace(p.PartySubTypeID), 10, 64); err == nil {
		ent.EntityType = rs.partyTypes[rs.partySubTypes[sub]]
	}

	primary, nonPrimary := extractNames(&party.Profile)
	ent.PrimaryName = primary
	ent.NonPrimaryNames = nonPrimary
	if len(nonPrimary) > 0 {
		ent.Details["non_primary_names"] = nonPrimary
	}

	seen := map[string]bool{}
	for _, f := range p.Features {
		typeID, err := strconv.ParseInt(strings.TrimSpace(f.FeatureTypeID), 10, 64)
		if err != nil {
			continue
		}
		currency, isDigital := rs.featureTypeToCurrency[typeID]
		if !isDigital {
			continue
		}
		idType := rs.featureTypeNames[typeID]
		versionIDs := make([]string, 0, len(f.FeatureVersions))
		comments := make([]string, 0)
		for _, v := range f.FeatureVersions {
			versionIDs = append(versionIDs, v.ID)
			for _, c := range v.Comments {
				if s := strings.TrimSpace(c); s != "" {
					comments = append(comments, s)
				}
			}
			for _, vd := range v.VersionDetails {
				addr := strings.TrimSpace(vd.Text)
				if addr == "" {
					continue
				}
				norm, valid := normalizeListed(addr)
				key := norm + "|" + idType + "|" + currency + "|" + f.ID
				if seen[key] {
					continue
				}
				seen[key] = true
				net, scope := resolveListing(currency, addr)
				ent.Wallets = append(ent.Wallets, ParsedWallet{
					ListedAddress:      addr,
					NormalizedAddress:  norm,
					CurrencyCode:       currency,
					IdentifierType:     idType,
					SourceIdentifierID: f.ID,
					MatchScope:         scope,
					NetworkCode:        net,
					AddressValid:       valid,
					Evidence: map[string]any{
						"feature_id":          f.ID,
						"feature_type_id":     typeID,
						"feature_version_ids": versionIDs,
						"version_detail_id":   vd.DetailTypeID,
						"comments":            comments,
						"address_valid":       valid,
					},
				})
			}
		}
	}
	return ent
}

// normalizeListed normalizes EVM-format addresses (lowercase 0x canonical)
// and keeps every other representation verbatim — chain-specific codecs for
// non-EVM chains are out of scope (spec §7.3).
func normalizeListed(addr string) (normalized string, valid bool) {
	if n, err := wallet.NormalizeEVM(addr); err == nil {
		return n, true
	}
	return addr, wallet.IsEVMAddress(addr)
}

// extractNames picks the primary documented name: primary Identity →
// primary Alias → DocumentedName → NamePartValue texts joined in order.
// Non-primary alias names are preserved for details. Falls back to the
// first available documented name when nothing is marked primary; if the
// document truly has no name, "" is returned (never invented).
func extractNames(p *xmlProfile) (primary string, nonPrimary []string) {
	type candidate struct {
		name    string
		primary bool
	}
	var candidates []candidate
	for _, ident := range p.Identities {
		identPrimary := strings.EqualFold(ident.Primary, "true")
		for _, a := range ident.Aliases {
			aliasPrimary := strings.EqualFold(a.Primary, "true")
			for _, dn := range a.DocumentedNames {
				var parts []string
				for _, part := range dn.Parts {
					if s := strings.TrimSpace(part.Value.Text); s != "" {
						parts = append(parts, s)
					}
				}
				if len(parts) == 0 {
					continue
				}
				c := candidate{name: strings.Join(parts, " "), primary: identPrimary && aliasPrimary}
				candidates = append(candidates, c)
				if !c.primary {
					nonPrimary = append(nonPrimary, c.name)
				}
			}
		}
	}
	for _, c := range candidates {
		if c.primary {
			return c.name, nonPrimary
		}
	}
	if len(candidates) > 0 {
		return candidates[0].name, nonPrimary
	}
	return "", nonPrimary
}

// mapEntry folds one SanctionsEntry into the document-level program and
// reference maps keyed by ProfileID.
func mapEntry(entry *xmlSanctionsEntry, rs *referenceSets, meta *DocMeta) {
	pid := strings.TrimSpace(entry.ProfileID)
	if pid == "" {
		return
	}
	seen := map[string]bool{}
	existing := meta.ProgramsByProfile[pid]
	for _, s := range existing {
		seen[s] = true
	}
	for _, m := range entry.Measures {
		code := strings.TrimSpace(m.Comment)
		if code == "" || seen[code] {
			continue
		}
		seen[code] = true
		existing = append(existing, code)
	}
	if len(existing) > 0 {
		sort.Strings(existing)
		meta.ProgramsByProfile[pid] = existing
	}

	listName := ""
	if lid, err := strconv.ParseInt(strings.TrimSpace(entry.ListID), 10, 64); err == nil {
		listName = rs.lists[lid]
	}
	for _, ev := range entry.EntryEvents {
		basisID, err := strconv.ParseInt(strings.TrimSpace(ev.LegalBasisID), 10, 64)
		if err != nil {
			continue
		}
		name := strings.TrimSpace(rs.legalBases[basisID])
		if name == "" || name == "Unknown" {
			continue
		}
		ref := ParsedReference{
			ReferenceType: "LEGAL_AUTHORITY",
			Title:         name,
			Details: map[string]any{
				"legal_basis_id":      basisID,
				"entry_event_id":      ev.ID,
				"entry_event_type_id": ev.EntryEventTypeID,
				"list_id":             entry.ListID,
				"list_name":           listName,
			},
		}
		if ev.Date.Year > 0 {
			d := time.Date(ev.Date.Year, time.Month(ev.Date.Month), ev.Date.Day, 0, 0, 0, 0, time.UTC)
			ref.PublishedAt = &d
		}
		meta.ReferencesByProfile[pid] = append(meta.ReferencesByProfile[pid], ref)
	}
}
