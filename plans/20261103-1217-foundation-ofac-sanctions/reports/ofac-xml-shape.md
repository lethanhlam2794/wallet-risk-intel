# OFAC SDN_ADVANCED.XML — Verified Document Shape (live fetch 2026-11-03)

Source: `https://sanctionslistservice.ofac.treas.gov/api/PublicationPreview/exports/SDN_ADVANCED.XML`
Ranged fetches (HTTP 206) verified the following structure against the real
document (Last-Modified: 2026-10-28).

## Root

```xml
<Sanctions xmlns="https://sanctionslistservice.ofac.treas.gov/api/PublicationPreview/exports/ADVANCED_XML"
           xmlns:xsi="..." Version="3" xsi:schemaLocation="... ADVANCED_XML.xsd">
  <DateOfIssue CalendarTypeID="1"><Year>..</Year><Month/><Day/></DateOfIssue>
  <ReferenceValueSets> ... </ReferenceValueSets>
  <DistinctParties> ...
  <SanctionsEntries> ...   <!-- appears after all DistinctParty blocks -->
```

## ReferenceValueSets (authoritative code tables)

Includes: `AliasTypeValues`, `AreaCode(Values)`, `CalendarTypeValues`,
`DetailTypeValues`, `EntryEventTypeValues`, `EntryLinkTypeValues`,
`ExRefTypeValues`, `FeatureTypeValues`, `IdentityFeatureLinkTypeValues`,
`IDRegDocDateTypeValues`, `IDRegDocTypeValues`, `LegalBasisTypeValues`,
`LocPartTypeValues`, `LocPartValueTypeValues`, `NamePartTypeValues`,
`PartySubTypeValues`, `PartyTypeValues`, `RelationTypeValues`,
`SanctionsTypeValues`, `SupInfoTypeValues`, `TargetTypeValues`.

Digital currency feature types observed (name = `Digital Currency Address - <CUR>`):
XBT(344), ETH(345), XMR(444), LTC(566), ZEC(686), DASH(687), BTG(688),
ETC(689), BSV(706), BCH(726), XVG(746), USDT(887), XRP(907), BNB(989),
TRX(992), DOGE(996), USDC(998), ARB(1007), BSC(1008), SOL(1167).
→ Parser MUST resolve membership dynamically from FeatureTypeValues
(name prefix `Digital Currency Address - `), never hardcode IDs.

PartyTypeValues: Individual(1), Entity(2), Location(3), Transport(4),
Other Entity(5). PartySubTypeValues link SubType→PartyType.

SanctionsTypeValues: e.g. Block(1705), Reject(1706), sectoral directives...

## Entity (DistinctParty → Profile)

```xml
<DistinctParty FixedRef="31940">
  <Profile ID="31940" PartySubTypeID="1">
    <Identity ID="23584" FixedRef="31940" Primary="true" False="false">
      <Alias FixedRef="31940" AliasTypeID="1403" Primary="true" LowQuality="false">
        <DocumentedName ID="44581" DocNameStatusID="1">
          <DocumentedNamePart>
            <NamePartValue NamePartGroupID="84641" ScriptID="215">SPASATEL KAREV</NamePartValue>
          </DocumentedNamePart>
        </DocumentedName>
      </Alias>
      <NamePartGroups>...</NamePartGroups>
    </Identity>
    <Feature ID="40577" FeatureTypeID="1">
      <FeatureVersion ID="38296" ReliabilityID="1">
        <Comment/>
        <VersionDetail DetailTypeID="1432">UBKI6</VersionDetail>
      </FeatureVersion>
      <IdentityReference IdentityID="23584" IdentityFeatureLinkTypeID="1"/>
    </Feature>
  </Profile>
</DistinctParty>
```

Mapping:
- `external_entity_id` ← `Profile@ID` (stable OFAC profile id).
- `entity_type` ← `Profile@PartySubTypeID` → PartySubType→PartyType name.
- `primary_name` ← Identity[@Primary=true] → Alias[@Primary=true] →
  DocumentedName → DocumentedNamePart/NamePartValue texts joined in order.
  Non-primary aliases kept in `details` jsonb.
- Digital currency listing ← Feature where FeatureTypeID resolves to
  `Digital Currency Address - *`; address = `FeatureVersion/VersionDetail`
  text; `source_identifier_id` = `Feature@ID`; currency = type-name suffix.

## Programs & measures (SanctionsEntries section)

```xml
<SanctionsEntry ID="40243" ProfileID="40243" ListID="1550">
  <EntryEvent ID="40243" EntryEventTypeID="1" LegalBasisID="92049">
    <Date CalendarTypeID="1"><Year>2022</Year>...
  </EntryEvent>
  <SanctionsMeasure ID="32781" SanctionsTypeID="1705"><DatePeriod .../></SanctionsMeasure>
  <SanctionsMeasure ID="168367" SanctionsTypeID="1">
    <Comment>RUSSIA-EO14024</Comment>
  </SanctionsMeasure>
</SanctionsEntry>
```

Mapping:
- Program codes ← `SanctionsEntry[@ProfileID]/SanctionsMeasure/Comment`
  (e.g. `RUSSIA-EO14024`, `DPRK3`); dedup per entity.
- `ListID` → list identity (SDN); EntryEvent `LegalBasisID` →
  LegalBasisTypeValues → `sanctions_references.reference_type='LEGAL_AUTHORITY'`
  where resolvable; EntryEvent date → listing date evidence.
- OFAC publishes **no per-entity reason text** → reason model defaults to
  `PROGRAM_CONTEXT_ONLY` (programs exist) or `NOT_AVAILABLE`.

## Network resolution rules (currency code × address format)

- ETH + 0x-EVM → eip155:1 (ethereum), CHAIN_SPECIFIC.
- BSC + 0x-EVM → eip155:56 (bsc), CHAIN_SPECIFIC.
- BNB + 0x-EVM → eip155:56 (BSC hosts BNB; address format is EVM).
- USDT/USDC/ARB/other multi-chain codes → `network_id=NULL` (chain not
  evidenced even if 0x format) — surfaced later as unresolved-chain
  observations, never as direct matches.
- XBT/XMR/TRX/SOL/other non-EVM → `network_id=NULL`.
- Address preserved verbatim in `listed_address`; `normalized_address`
  lowercase only for EVM-formatted values.
