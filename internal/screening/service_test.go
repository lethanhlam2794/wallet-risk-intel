package screening

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"checkwallet/internal/db"
	"checkwallet/internal/enrichment"
	"checkwallet/internal/label"
	"checkwallet/internal/network"
	"checkwallet/internal/sanctions"
	"checkwallet/internal/scoring"
	"checkwallet/internal/testutil"
	"checkwallet/internal/wallet"
)

// Package-private test database (P-036) — parallel packages each own a
// _test database so truncation never races.
const testDSN = "postgres://wallet:devonly@127.0.0.1:5433/check_wallet_screening_test?sslmode=disable"

func testNet() network.Network {
	return network.Network{ID: 1, Namespace: "eip155", ChainReference: "1",
		Code: "ethereum", Name: "Ethereum"}
}

const testAddr = "0x0000000000000000000000000000000000000abc"

// ---- collaborator fakes (contract-faithful — same return shapes) ----

type fakeSanctions struct {
	lookup *sanctions.WalletLookup
	status string
	synced *time.Time
	err    error
	calls  atomic.Int32
}

func (f *fakeSanctions) LookupWallet(ctx context.Context, networkID int64,
	networkCode, normalized string) (*sanctions.WalletLookup, string, *time.Time, error) {
	f.calls.Add(1)
	return f.lookup, f.status, f.synced, f.err
}

type fakeLabels struct {
	pages         [][]label.Item
	status        string
	synced        *time.Time
	err           error
	calls         atomic.Int32
	sawActiveOnly bool
}

func (f *fakeLabels) ListLabels(ctx context.Context, networkID int64,
	networkCode, normalized string, flt label.Filters) (*label.ListResult, string, *time.Time, error) {
	f.calls.Add(1)
	if flt.ActiveOnly {
		f.sawActiveOnly = true
	}
	if f.err != nil {
		return nil, "", nil, f.err
	}
	// Emulate the real cursor contract: opaque base64 of last id.
	start := 0
	if flt.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(flt.Cursor)
		if err != nil {
			return nil, "", nil, fmt.Errorf("bad cursor")
		}
		start, _ = strconv.Atoi(string(raw))
	}
	limit := flt.Limit
	if limit > 200 {
		limit = 200
	}
	var flat []label.Item
	for _, p := range f.pages {
		flat = append(flat, p...)
	}
	var items []label.Item
	var lastID int64
	for _, it := range flat {
		if it.WalletLabelID <= int64(start) {
			continue
		}
		if len(items) == limit {
			break
		}
		items = append(items, it)
		lastID = it.WalletLabelID
	}
	res := &label.ListResult{Items: items}
	if int(lastID) < len(flat) && len(items) == limit {
		res.NextCursor = base64.RawURLEncoding.EncodeToString(
			[]byte(strconv.FormatInt(lastID, 10)))
	}
	return res, f.status, f.synced, nil
}

type fakeEnrich struct {
	res *enrichment.Result
	err error
}

func (f *fakeEnrich) Enrich(ctx context.Context, net network.Network,
	rawAddress, normalized string, opts enrichment.Options) (*enrichment.Result, error) {
	return f.res, f.err
}

func newService(t *testing.T, pool *pgxpool.Pool, s SanctionsLookup,
	l LabelsLookup, e Enricher) *Service {
	t.Helper()
	return New(Deps{
		Pool:       pool,
		Queries:    db.New(pool),
		Sanctions:  s,
		Labels:     l,
		Enrichment: e,
		Wallets:    wallet.NewService(db.New(pool)),
	})
}

func canonical(opts Options) CanonicalRequest {
	return CanonicalRequest{Network: "ethereum", Address: testAddr, Options: opts}
}

func runScreening(t *testing.T, svc *Service, opts Options) *Result {
	t.Helper()
	res, err := svc.Run(context.Background(), testNet(), canonical(opts), RunMeta{})
	require.NoError(t, err)
	return res
}

// storedRow fetches the persisted row for assertions.
func storedRow(t *testing.T, pool *pgxpool.Pool, id string) db.ScreeningResult {
	t.Helper()
	uid, err := uuid.Parse(id)
	require.NoError(t, err)
	row, err := db.New(pool).GetScreeningResultByID(context.Background(),
		pgtype.UUID{Bytes: uid, Valid: true})
	require.NoError(t, err)
	return row
}

// --- tests ---

func TestService_InsufficientData_NoSnapshots(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	svc := newService(t, pool,
		&fakeSanctions{lookup: &sanctions.WalletLookup{}, status: DataUnavailable},
		&fakeLabels{status: DataUnavailable},
		nil) // no enrichment wired — local data still answers

	res := runScreening(t, svc, DefaultOptions())
	assert.Equal(t, StatusInsufficientData, res.Status)
	assert.Nil(t, res.RiskScore, "zero usable inputs → NULL score")
	assert.Equal(t, "UNKNOWN", res.RiskLevel)
	assert.False(t, res.DataCompleteness.Sanctions.Usable)

	row := storedRow(t, pool, res.ScreeningID)
	assert.Equal(t, StatusInsufficientData, row.ScreeningStatus)
	assert.False(t, row.RiskScore.Valid, "persisted risk_score must be NULL")
	assert.Equal(t, "v1.0.0", row.ScoringPolicyVersion)
	// The stored result body is byte-faithful to what Run returned.
	marshaled, err := json.Marshal(res)
	require.NoError(t, err)
	assert.Equal(t, string(marshaled), string(row.Result))
}

func TestService_SanctionsExcluded(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	svc := newService(t, pool,
		&fakeSanctions{
			lookup: &sanctions.WalletLookup{
				Matched: true,
				Sanctions: []sanctions.SanctionEntry{{
					EntityID: 7, EntityName: "OFAC entity",
					ListedAddress: testAddr, IdentifierType: "ETH",
					MatchScope: "CHAIN_SPECIFIC",
					Source:     sanctions.SourceRef{Code: "ofac_sdn", Name: "OFAC SDN"},
				}},
			},
			status: DataAvailable,
		},
		&fakeLabels{status: DataAvailable},
		nil)

	opts := DefaultOptions()
	opts.IncludeSanctions = false
	res := runScreening(t, svc, opts)

	assert.False(t, res.DirectSanctions.Evaluated, "excluded → evaluated:false")
	assert.NotContains(t, res.Scoring.Flags, "DIRECT_SANCTIONS")
	// The excluded dimension is visible as NOT_EVALUATED, not absent.
	var sawNotEval bool
	for _, r := range res.Scoring.Reasons {
		if r.Code == "NOT_EVALUATED" {
			sawNotEval = true
		}
	}
	assert.True(t, sawNotEval)
	assert.Equal(t, "NOT_REQUESTED", res.DataCompleteness.Sanctions.DataStatus)
}

func TestService_EnrichFanoutError_ReportedOnFanoutField(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	svc := newService(t, pool,
		&fakeSanctions{lookup: &sanctions.WalletLookup{}, status: DataAvailable},
		&fakeLabels{status: DataAvailable},
		&fakeEnrich{err: fmt.Errorf("fan-out blew up")})

	res := runScreening(t, svc, DefaultOptions())
	assert.Equal(t, StatusPartial, res.Status,
		"sanctions usable + fan-out failure → PARTIAL")
	require.NotNil(t, res.ProviderStatus.Fanout,
		"wholesale fan-out failure must land on the named field")
	assert.Equal(t, "ERROR", res.ProviderStatus.Fanout.Status)
	assert.Empty(t, res.ProviderStatus.Providers,
		"no pseudo-provider keys appear in the per-source map")
}

func TestService_AllProvidersFailed_Partial(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	enrichRes := &enrichment.Result{
		Status: enrichment.StatusFailed,
		Providers: []enrichment.ProviderResult{
			{Source: "goplus", Enabled: true, Status: "TIMEOUT",
				Error: &enrichment.ErrorInfo{Code: "timeout", Message: "deadline"}},
			{Source: "publicaml", Enabled: true, Status: "UPSTREAM_5XX",
				Error: &enrichment.ErrorInfo{Code: "upstream_5xx"}},
			{Source: "chainabuse", Enabled: false, Status: "DISABLED",
				Error: &enrichment.ErrorInfo{Code: "disabled"}},
		},
	}
	svc := newService(t, pool,
		&fakeSanctions{lookup: &sanctions.WalletLookup{}, status: DataAvailable},
		&fakeLabels{status: DataAvailable},
		&fakeEnrich{res: enrichRes})

	res := runScreening(t, svc, DefaultOptions())
	assert.Equal(t, StatusPartial, res.Status,
		"sanctions usable + providers failed → PARTIAL")
	assert.Equal(t, "TIMEOUT", res.ProviderStatus.Providers["goplus"].Status)
	assert.Equal(t, "UPSTREAM_5XX", res.ProviderStatus.Providers["publicaml"].Status)
	assert.Equal(t, "DISABLED", res.ProviderStatus.Providers["chainabuse"].Status)
	// DISABLED is configuration, not failure — excluded from the fold.
	assert.Contains(t, res.DataCompleteness.Providers.Disabled, "chainabuse")
	assert.NotContains(t, res.DataCompleteness.Providers.Degraded, "chainabuse")
	assert.Contains(t, res.DataCompleteness.Providers.Degraded, "goplus")
	// Failures surface as PROVIDER_FAILED reasons — never a clean result.
	var failedReasons int
	for _, r := range res.Scoring.Reasons {
		if r.Code == "PROVIDER_FAILED" {
			failedReasons++
		}
	}
	assert.Equal(t, 2, failedReasons)
}

func TestService_LabelCursorLoop(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	var items []label.Item
	for i := 1; i <= 450; i++ {
		items = append(items, label.Item{
			WalletLabelID: int64(i), Category: "SCAM", CategoryType: "MALICIOUS_ACTIVITY",
			Label: fmt.Sprintf("scam-%d", i), Confidence: "high",
			VerificationStatus: "reported",
			Source:             label.SourceRef{Code: "wallet_attribution"},
		})
	}
	fl := &fakeLabels{pages: [][]label.Item{items}, status: DataAvailable}
	svc := newService(t, pool,
		&fakeSanctions{lookup: &sanctions.WalletLookup{}, status: DataAvailable},
		fl, nil)

	res := runScreening(t, svc, DefaultOptions())
	assert.Equal(t, 450, res.DirectLabels.Collected, "cursor loop collects all")
	assert.False(t, res.DirectLabels.Truncated)
	assert.GreaterOrEqual(t, fl.calls.Load(), int32(3), "450 labels need ≥3 pages")
	// direct_max — never a sum: reported+high SCAM = round(85×0.7×1.0)=60.
	require.NotNil(t, res.RiskScore)
	assert.Equal(t, 60, *res.RiskScore)
	assert.True(t, fl.sawActiveOnly, "revoked labels must be filtered out upstream")
}

func TestService_DisputedAndRevokedLabels(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	items := []label.Item{
		{WalletLabelID: 1, Category: "SCAM", CategoryType: "MALICIOUS_ACTIVITY",
			Label: "disputed-scam", Confidence: "high", VerificationStatus: "disputed",
			Source: label.SourceRef{Code: "wallet_attribution"}},
	}
	svc := newService(t, pool,
		&fakeSanctions{lookup: &sanctions.WalletLookup{}, status: DataAvailable},
		&fakeLabels{pages: [][]label.Item{items}, status: DataAvailable}, nil)

	res := runScreening(t, svc, DefaultOptions())
	require.NotNil(t, res.RiskScore)
	assert.Equal(t, 0, *res.RiskScore, "disputed labels score 0")
	require.Len(t, res.Scoring.Reasons, 1)
	assert.Equal(t, "disputed", res.Scoring.Reasons[0].VerificationStatus)
}

func TestService_ClientDisconnect_StillPersists(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	svc := newService(t, pool,
		&fakeSanctions{lookup: &sanctions.WalletLookup{}, status: DataAvailable},
		&fakeLabels{status: DataAvailable}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // client already gone — the detached pipeline must still land
	res, err := svc.Run(ctx, testNet(), canonical(DefaultOptions()), RunMeta{})
	require.NoError(t, err)
	require.NotNil(t, res)
	row := storedRow(t, pool, res.ScreeningID)
	assert.Equal(t, res.Status, row.ScreeningStatus)
}

func TestService_ExposureHops(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	enrichRes := &enrichment.Result{
		Status: enrichment.StatusCompleted,
		Providers: []enrichment.ProviderResult{{
			Source: "goplus", Enabled: true, Status: "OK",
			Exposures: []enrichment.Exposure{
				{Counterparty: "0x1111", Category: "SANCTIONED", Hops: 1, Direction: "out"},
				{Counterparty: "0x2222", Category: "SCAM", Hops: 3, Direction: "out"},
			},
		}},
	}
	svc := newService(t, pool,
		&fakeSanctions{lookup: &sanctions.WalletLookup{}, status: DataAvailable},
		&fakeLabels{status: DataAvailable},
		&fakeEnrich{res: enrichRes})

	res := runScreening(t, svc, DefaultOptions())
	require.Len(t, res.Exposures.Items, 2)
	for _, it := range res.Exposures.Items {
		assert.Equal(t, "provider_reported", it.Basis)
	}
	assert.True(t, res.Exposures.Items[0].Scored, "1-hop scores")
	assert.False(t, res.Exposures.Items[1].Scored, "hops>2 listed, unscored")
	assert.Equal(t, "provider_reported_only", res.Exposures.Coverage.Tracing)
	assert.Equal(t, 2, res.Exposures.Coverage.MaxHopsRequested)
	// Provider OK also feeds usable: score = provider leg contributions.
	require.NotNil(t, res.RiskScore)
	assert.Equal(t, 7, *res.RiskScore, "1-hop = round(10×0.7)=7, 3-hop=0")
}

func TestService_ExposureMaxHopsFilter(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	enrichRes := &enrichment.Result{
		Status: enrichment.StatusCompleted,
		Providers: []enrichment.ProviderResult{{
			Source: "goplus", Enabled: true, Status: "OK",
			Exposures: []enrichment.Exposure{
				{Counterparty: "0xaaaa", Category: "SCAM", Hops: 2, Direction: "in"},
			},
		}},
	}
	svc := newService(t, pool,
		&fakeSanctions{lookup: &sanctions.WalletLookup{}, status: DataAvailable},
		&fakeLabels{status: DataAvailable},
		&fakeEnrich{res: enrichRes})

	opts := DefaultOptions()
	opts.MaxHops = 1
	res := runScreening(t, svc, opts)
	require.Len(t, res.Exposures.Items, 1)
	assert.False(t, res.Exposures.Items[0].Scored, "hops=2 > max_hops=1 unscored")
	require.NotNil(t, res.RiskScore)
	assert.Equal(t, 0, *res.RiskScore)
	assert.Equal(t, 1, res.Exposures.Coverage.MaxHopsRequested)
}

func TestService_NoEnrichment_LocalDataAnswers(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	svc := newService(t, pool,
		&fakeSanctions{
			lookup: &sanctions.WalletLookup{
				Matched: true,
				Sanctions: []sanctions.SanctionEntry{{
					EntityID: 9, EntityName: "SDN Entity", ListedAddress: testAddr,
					IdentifierType: "ETH", MatchScope: "CHAIN_SPECIFIC",
					Source: sanctions.SourceRef{Code: "ofac_sdn", Name: "OFAC SDN"},
				}},
			},
			status: DataAvailable,
		},
		&fakeLabels{status: DataAvailable},
		nil)

	res := runScreening(t, svc, DefaultOptions())
	assert.Equal(t, StatusCompleted, res.Status)
	require.NotNil(t, res.RiskScore)
	assert.Equal(t, 100, *res.RiskScore, "direct OFAC match → flat 100")
	assert.Contains(t, res.Scoring.Flags, "DIRECT_SANCTIONS")
	assert.Equal(t, "CRITICAL", res.RiskLevel)
	assert.True(t, res.DirectSanctions.Matched)
	// Evidence joins reasons to proof.
	var sawSanctionsEvidence bool
	for _, e := range res.Evidence {
		if e.Kind == "sanctions" {
			sawSanctionsEvidence = true
		}
	}
	assert.True(t, sawSanctionsEvidence)
}

func TestService_ClaimAttachInTx(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	q := db.New(pool)
	ctx := context.Background()

	// Seed an api_client + claim row; the run must attach the result to
	// the claim inside the same commit.
	var clientID int64
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO api_clients (name, key_hash, role) VALUES ('scr-claim', 'h', 'reader') RETURNING id`,
	).Scan(&clientID))
	claim, err := q.InsertScreeningRequest(ctx, db.InsertScreeningRequestParams{
		ClientID: clientID, IdempotencyKey: "k", RequestHash: "h"})
	require.NoError(t, err)

	svc := newService(t, pool,
		&fakeSanctions{lookup: &sanctions.WalletLookup{}, status: DataAvailable},
		&fakeLabels{status: DataAvailable}, nil)

	res, err := svc.Run(ctx, testNet(), canonical(DefaultOptions()),
		RunMeta{ClaimID: &claim.ID})
	require.NoError(t, err)

	got, err := q.GetScreeningRequestByKey(ctx, db.GetScreeningRequestByKeyParams{
		ClientID: clientID, IdempotencyKey: "k"})
	require.NoError(t, err)
	require.True(t, got.ScreeningResultID.Valid)
	uid, err := uuid.Parse(res.ScreeningID)
	require.NoError(t, err)
	assert.Equal(t, [16]byte(uid), got.ScreeningResultID.Bytes,
		"claim must point at the result row committed in the same tx")
}

func TestService_UnusableDimension_EmitsNoFindings(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	// M3: a lookup that returns rows while its dimension is UNAVAILABLE
	// (contract drift upstream) must not emit findings, entries, or a
	// match — and must not rescue the verdict from INSUFFICIENT_DATA.
	svc := newService(t, pool,
		&fakeSanctions{
			lookup: &sanctions.WalletLookup{
				Matched: true,
				Sanctions: []sanctions.SanctionEntry{{
					EntityID: 11, EntityName: "drift-entity", ListedAddress: testAddr,
					IdentifierType: "ETH", MatchScope: "CHAIN_SPECIFIC",
					Source: sanctions.SourceRef{Code: "ofac_sdn", Name: "OFAC SDN"},
				}},
			},
			status: DataUnavailable,
		},
		&fakeLabels{
			status: DataUnavailable,
			pages: [][]label.Item{{{
				WalletLabelID: 1, Category: "SCAM", Label: "drift",
				VerificationStatus: "verified", Confidence: "high",
				Source: label.SourceRef{Code: "wallet_attribution"},
			}}},
		},
		nil)

	res := runScreening(t, svc, DefaultOptions())
	assert.Equal(t, StatusInsufficientData, res.Status)
	assert.Nil(t, res.RiskScore)
	assert.False(t, res.DataCompleteness.Sanctions.Usable)
	assert.False(t, res.DirectSanctions.Matched,
		"unusable dimension never reports a match")
	assert.Empty(t, res.DirectSanctions.Entries)
	assert.Empty(t, res.DirectLabels.Items)
	assert.Equal(t, scoring.Blocks{}, res.Scoring.Blocks)
	assert.NotContains(t, res.Scoring.Flags, "DIRECT_SANCTIONS")
	for _, r := range res.Scoring.Reasons {
		assert.NotEqual(t, "DIRECT_SANCTIONS_MATCH", r.Code)
		assert.NotEqual(t, "DIRECT_LABEL", r.Code)
	}
}

func TestService_PersistFailed_AuditRow(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	ctx := context.Background()
	q := db.New(pool)
	svc := newService(t, pool,
		&fakeSanctions{lookup: &sanctions.WalletLookup{}, status: DataAvailable},
		&fakeLabels{status: DataAvailable}, nil)

	w, err := wallet.NewService(q).GetOrCreate(ctx, testNet().ID, testAddr, testAddr)
	require.NoError(t, err)

	require.NoError(t, svc.persistFailed(w.ID, canonical(DefaultOptions())))

	var row db.ScreeningResult
	rows, err := q.ListScreeningResultsByWallet(ctx,
		db.ListScreeningResultsByWalletParams{WalletID: w.ID, Limit: 10})
	require.NoError(t, err)
	require.Len(t, rows, 1, "exactly one FAILED audit row")
	row = rows[0]

	assert.Equal(t, StatusFailed, row.ScreeningStatus)
	assert.False(t, row.RiskScore.Valid, "FAILED carries NULL score")
	assert.Equal(t, "UNKNOWN", row.RiskLevel)
	assert.Equal(t, "v1.0.0", row.ScoringPolicyVersion)
	var stored Result
	require.NoError(t, json.Unmarshal(row.Result, &stored))
	assert.Equal(t, StatusFailed, stored.Status)
	assert.Nil(t, stored.RiskScore)
	require.NotNil(t, stored.ProviderStatus.Fanout)
	assert.Equal(t, "screening persist failed",
		stored.ProviderStatus.Fanout.Error.Message,
		"persisted message is sanitized — never raw driver detail")
}

func TestService_RawAddressPreserved(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	svc := newService(t, pool,
		&fakeSanctions{lookup: &sanctions.WalletLookup{}, status: DataAvailable},
		&fakeLabels{status: DataAvailable}, nil)

	canon := canonical(DefaultOptions())
	canon.RawAddress = "0x0000000000000000000000000000000000000AbC"
	_, err := svc.Run(context.Background(), testNet(), canon, RunMeta{})
	require.NoError(t, err)

	var addr, normalized string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT address, normalized_address FROM wallets
		 WHERE network_id = $1 AND normalized_address = $2`,
		testNet().ID, testAddr).Scan(&addr, &normalized))
	assert.Equal(t, canon.RawAddress, addr,
		"wallets.address keeps the original representation")
	assert.Equal(t, testAddr, normalized)
}

func TestService_StaleSnapshot_Partial(t *testing.T) {
	pool := testutil.RequireTestPoolDSN(t, testDSN)
	svc := newService(t, pool,
		&fakeSanctions{lookup: &sanctions.WalletLookup{}, status: DataStale},
		&fakeLabels{status: DataAvailable}, nil)

	res := runScreening(t, svc, DefaultOptions())
	assert.Equal(t, StatusPartial, res.Status, "STALE is usable-but-degraded")
	assert.True(t, res.DataCompleteness.Sanctions.Stale)
	var sawStale bool
	for _, r := range res.Scoring.Reasons {
		if r.Code == "SNAPSHOT_STALE" {
			sawStale = true
		}
	}
	assert.True(t, sawStale, "staleness must be an explicit reason")
}
