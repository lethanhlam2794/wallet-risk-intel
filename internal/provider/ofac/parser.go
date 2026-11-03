package ofac

import (
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
	"time"
)

// XML decode shapes — verified against the live SDN_ADVANCED.XML
// (reports/ofac-xml-shape.md, 2026-11-03). Local names only; the ADVANCED_XML
// default namespace is ignored on purpose.

type xmlValueItem struct {
	ID          int64  `xml:"ID,attr"`
	PartyTypeID int64  `xml:"PartyTypeID,attr"`
	Text        string `xml:",chardata"`
}

type xmlValueSet struct {
	Items []xmlValueItem `xml:",any"`
}

type xmlDateOfIssue struct {
	Year  int `xml:"Year"`
	Month int `xml:"Month"`
	Day   int `xml:"Day"`
}

type xmlDistinctParty struct {
	FixedRef string     `xml:"FixedRef,attr"`
	Profile  xmlProfile `xml:"Profile"`
}

type xmlProfile struct {
	ID             string        `xml:"ID,attr"`
	PartySubTypeID string        `xml:"PartySubTypeID,attr"`
	Identities     []xmlIdentity `xml:"Identity"`
	Features       []xmlFeature  `xml:"Feature"`
}

type xmlIdentity struct {
	ID      string     `xml:"ID,attr"`
	Primary string     `xml:"Primary,attr"`
	Aliases []xmlAlias `xml:"Alias"`
}

type xmlAlias struct {
	Primary         string              `xml:"Primary,attr"`
	AliasTypeID     string              `xml:"AliasTypeID,attr"`
	DocumentedNames []xmlDocumentedName `xml:"DocumentedName"`
}

type xmlDocumentedName struct {
	Parts []xmlDocumentedNamePart `xml:"DocumentedNamePart"`
}

type xmlDocumentedNamePart struct {
	Value xmlNamePartValue `xml:"NamePartValue"`
}

type xmlNamePartValue struct {
	Text string `xml:",chardata"`
}

type xmlFeature struct {
	ID              string              `xml:"ID,attr"`
	FeatureTypeID   string              `xml:"FeatureTypeID,attr"`
	FeatureVersions []xmlFeatureVersion `xml:"FeatureVersion"`
}

type xmlFeatureVersion struct {
	ID             string             `xml:"ID,attr"`
	Comments       []string           `xml:"Comment"`
	VersionDetails []xmlVersionDetail `xml:"VersionDetail"`
}

type xmlVersionDetail struct {
	DetailTypeID string `xml:"DetailTypeID,attr"`
	Text         string `xml:",chardata"`
}

type xmlSanctionsEntry struct {
	ID          string                `xml:"ID,attr"`
	ProfileID   string                `xml:"ProfileID,attr"`
	ListID      string                `xml:"ListID,attr"`
	EntryEvents []xmlEntryEvent       `xml:"EntryEvent"`
	Measures    []xmlSanctionsMeasure `xml:"SanctionsMeasure"`
}

type xmlEntryEvent struct {
	ID               string  `xml:"ID,attr"`
	EntryEventTypeID string  `xml:"EntryEventTypeID,attr"`
	LegalBasisID     string  `xml:"LegalBasisID,attr"`
	Date             xmlDate `xml:"Date"`
}

type xmlDate struct {
	Year  int `xml:"Year"`
	Month int `xml:"Month"`
	Day   int `xml:"Day"`
}

type xmlSanctionsMeasure struct {
	ID              string `xml:"ID,attr"`
	SanctionsTypeID string `xml:"SanctionsTypeID,attr"`
	Comment         string `xml:"Comment"`
}

// Parser streams the Advanced XML via encoding/xml.Decoder — the document is
// never unmarshalled whole (spec §4.1/§16).
type Parser struct{}

// NewParser builds a parser.
func NewParser() *Parser { return &Parser{} }

const digitalCurrencyPrefix = "Digital Currency Address - "

// Parse streams the document, emitting each DistinctParty to the sink and
// collecting document-level metadata (date, program links, references).
func (p *Parser) Parse(ctx context.Context, r io.Reader, sink EntitySink) (*DocMeta, error) {
	br := bufio.NewReader(r)
	if b, err := br.Peek(3); err == nil && bytes.Equal(b, []byte{0xEF, 0xBB, 0xBF}) {
		// Strip a UTF-8 BOM if present.
		if _, err := br.Discard(3); err != nil {
			return nil, fmt.Errorf("ofac parse: bom: %w", err)
		}
	}
	dec := xml.NewDecoder(br)
	dec.Strict = true
	// Reject any non-UTF-8 charset outright.
	dec.CharsetReader = func(charset string, input io.Reader) (io.Reader, error) {
		if !strings.EqualFold(charset, "utf-8") && !strings.EqualFold(charset, "us-ascii") {
			return nil, fmt.Errorf("ofac parse: unsupported charset %q", charset)
		}
		return input, nil
	}

	refSets := newReferenceSets()
	meta := &DocMeta{
		ProgramsByProfile:   map[string][]string{},
		ReferencesByProfile: map[string][]ParsedReference{},
	}

	rootSeen := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("ofac parse: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.Directive:
			if bytes.Contains(bytes.ToUpper(t), []byte("DOCTYPE")) {
				return nil, fmt.Errorf("ofac parse: DOCTYPE declarations are not accepted")
			}
		case xml.StartElement:
			if !rootSeen {
				if t.Name.Local != "Sanctions" {
					return nil, fmt.Errorf("ofac parse: unexpected root element %q", t.Name.Local)
				}
				rootSeen = true
				continue
			}
			switch t.Name.Local {
			case "DateOfIssue":
				var d xmlDateOfIssue
				if err := dec.DecodeElement(&d, &t); err != nil {
					return nil, fmt.Errorf("ofac parse: DateOfIssue: %w", err)
				}
				doi := time.Date(d.Year, time.Month(d.Month), d.Day, 0, 0, 0, 0, time.UTC)
				meta.DateOfIssue = &doi
			case "ReferenceValueSets":
				if err := p.parseReferenceValueSets(ctx, dec, &t, refSets); err != nil {
					return nil, err
				}
			case "DistinctParties":
				if err := p.parseDistinctParties(ctx, dec, &t, refSets, sink); err != nil {
					return nil, err
				}
			case "SanctionsEntries":
				if err := p.parseSanctionsEntries(ctx, dec, &t, refSets, meta); err != nil {
					return nil, err
				}
			default:
				if err := dec.Skip(); err != nil {
					return nil, fmt.Errorf("ofac parse: skip %q: %w", t.Name.Local, err)
				}
			}
		}
	}
	if !rootSeen {
		return nil, fmt.Errorf("ofac parse: document has no Sanctions root")
	}
	return meta, nil
}

// parseDistinctParties streams each DistinctParty block; per-party decode is
// bounded so memory stays flat over the full document.
func (p *Parser) parseDistinctParties(ctx context.Context, dec *xml.Decoder, start *xml.StartElement, rs *referenceSets, sink EntitySink) error {
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("ofac parse: DistinctParties: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "DistinctParty" {
				var party xmlDistinctParty
				if err := dec.DecodeElement(&party, &t); err != nil {
					return fmt.Errorf("ofac parse: DistinctParty: %w", err)
				}
				ent := mapParty(&party, rs)
				if err := sink.HandleEntity(ctx, ent); err != nil {
					return err
				}
			} else {
				depth++
			}
		case xml.EndElement:
			depth--
		}
	}
	return nil
}

// parseSanctionsEntries collects profile→programs and profile→references
// maps (small) flushed at end-of-document by the importer.
func (p *Parser) parseSanctionsEntries(ctx context.Context, dec *xml.Decoder, start *xml.StartElement, rs *referenceSets, meta *DocMeta) error {
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("ofac parse: SanctionsEntries: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "SanctionsEntry" {
				var entry xmlSanctionsEntry
				if err := dec.DecodeElement(&entry, &t); err != nil {
					return fmt.Errorf("ofac parse: SanctionsEntry: %w", err)
				}
				mapEntry(&entry, rs, meta)
			} else {
				depth++
			}
		case xml.EndElement:
			depth--
		}
	}
	return nil
}

// parseReferenceValueSets decodes only the code tables the mapper needs and
// skips everything else (the section is large and mostly unrelated).
func (p *Parser) parseReferenceValueSets(ctx context.Context, dec *xml.Decoder, start *xml.StartElement, rs *referenceSets) error {
	wanted := map[string]bool{
		"FeatureTypeValues": true, "PartyTypeValues": true,
		"PartySubTypeValues": true, "LegalBasisTypeValues": true,
		"ListValues": true,
	}
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("ofac parse: ReferenceValueSets: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name := t.Name.Local
			if wanted[name] {
				var set xmlValueSet
				if err := dec.DecodeElement(&set, &t); err != nil {
					return fmt.Errorf("ofac parse: %s: %w", name, err)
				}
				rs.add(name, set.Items)
			} else {
				if err := dec.Skip(); err != nil {
					return fmt.Errorf("ofac parse: skip %q: %w", name, err)
				}
			}
		case xml.EndElement:
			depth--
		}
	}
	return nil
}

// referenceSets holds the code tables resolved while streaming.
type referenceSets struct {
	featureTypeToCurrency map[int64]string // FeatureTypeID → currency suffix
	featureTypeNames      map[int64]string // FeatureTypeID → verbatim name
	partyTypes            map[int64]string // PartyTypeID → name
	partySubTypes         map[int64]int64  // SubTypeID → PartyTypeID
	legalBases            map[int64]string // LegalBasisID → name
	lists                 map[int64]string // ListID → name
}

func newReferenceSets() *referenceSets {
	return &referenceSets{
		featureTypeToCurrency: map[int64]string{},
		featureTypeNames:      map[int64]string{},
		partyTypes:            map[int64]string{},
		partySubTypes:         map[int64]int64{},
		legalBases:            map[int64]string{},
		lists:                 map[int64]string{},
	}
}

func (rs *referenceSets) add(setName string, items []xmlValueItem) {
	for _, it := range items {
		text := strings.TrimSpace(it.Text)
		switch setName {
		case "FeatureTypeValues":
			rs.featureTypeNames[it.ID] = text
			if cur := currencyFromFeatureType(text); cur != "" {
				rs.featureTypeToCurrency[it.ID] = cur
			}
		case "PartyTypeValues":
			rs.partyTypes[it.ID] = text
		case "PartySubTypeValues":
			rs.partySubTypes[it.ID] = it.PartyTypeID
		case "LegalBasisTypeValues":
			rs.legalBases[it.ID] = text
		case "ListValues":
			rs.lists[it.ID] = text
		}
	}
}
