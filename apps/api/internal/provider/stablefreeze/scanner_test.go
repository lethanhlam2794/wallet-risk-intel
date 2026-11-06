package stablefreeze

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeCursors is the CursorStore test double.
type fakeCursors struct {
	rows []ScanCursor
	err  error
}

func (f fakeCursors) GetFreezeScanStates(context.Context) ([]ScanCursor, error) {
	return f.rows, f.err
}

func seedCursors() []ScanCursor {
	return []ScanCursor{
		{NetworkID: 1, ContractAddress: "0xdac17f958d2ee523a2206206994597c13d831ec7",
			GenesisBlock: 4634748, LastScannedBlock: 4634747},
		{NetworkID: 1, ContractAddress: "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48",
			GenesisBlock: 6082465, LastScannedBlock: 6082464},
	}
}

// rpcStub answers JSON-RPC calls from canned responses.
type rpcStub struct {
	head         string
	logs         func(params map[string]any) []RawLog
	blocks       map[int64]string // block number → timestamp hex
	failOn       map[string]bool  // method → return rpc error
	shuffleBatch bool             // reverse batch responses (spec permits out-of-order)
}

func (s *rpcStub) serve(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var raw json.RawMessage
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		raw = buf
		var reqs []rpcReq
		wasBatch := strings.HasPrefix(strings.TrimSpace(string(raw)), "[")
		if wasBatch {
			require.NoError(t, json.Unmarshal(raw, &reqs))
		} else {
			var one rpcReq
			require.NoError(t, json.Unmarshal(raw, &one))
			reqs = []rpcReq{one}
		}
		var out []rpcResp
		for _, q := range reqs {
			out = append(out, s.answer(t, q))
		}
		if s.shuffleBatch {
			for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
				out[i], out[j] = out[j], out[i]
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if wasBatch {
			_ = json.NewEncoder(w).Encode(out)
		} else {
			_ = json.NewEncoder(w).Encode(out[0])
		}
	}
}

func (s *rpcStub) answer(t *testing.T, q rpcReq) rpcResp {
	errResp := func(code int, msg string) rpcResp {
		return rpcResp{ID: &q.ID, Error: &struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}{Code: code, Message: msg}}
	}
	if s.failOn[q.Method] {
		return errResp(-32000, "injected failure")
	}
	switch q.Method {
	case "eth_blockNumber":
		b, _ := json.Marshal(s.head)
		return rpcResp{ID: &q.ID, Result: b}
	case "eth_getLogs":
		// Positional params: [filterObject].
		var p []any
		bb, _ := json.Marshal(q.Params)
		require.NoError(t, json.Unmarshal(bb, &p))
		require.Len(t, p, 1)
		var m map[string]any
		bb, _ = json.Marshal(p[0])
		require.NoError(t, json.Unmarshal(bb, &m))
		var logs []RawLog
		if s.logs != nil {
			logs = s.logs(m)
		}
		b, _ := json.Marshal(logs)
		return rpcResp{ID: &q.ID, Result: b}
	case "eth_getBlockByNumber":
		var p []any
		bb, _ := json.Marshal(q.Params)
		require.NoError(t, json.Unmarshal(bb, &p))
		var blk any
		for n, ts := range s.blocks {
			if fmt.Sprintf("0x%x", n) == p[0] {
				blk = map[string]any{"number": p[0], "timestamp": ts}
			}
		}
		b, _ := json.Marshal(blk)
		return rpcResp{ID: &q.ID, Result: b}
	}
	return errResp(-32601, "method not found")
}

func newTestScanner(t *testing.T, stub *rpcStub, cursors CursorStore, cfg Config) *Scanner {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(stub.serve(t)))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	cli, err := NewRPCClient(srv.URL, []string{host},
		WithAllowedHosts(host), WithInsecureHTTP())
	require.NoError(t, err)
	s, err := NewScanner(cursors, cli, cfg)
	require.NoError(t, err)
	return s
}

// readJSONL returns the file's non-empty lines as raw messages.
func readJSONL(t *testing.T, path string) []json.RawMessage {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	var out []json.RawMessage
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if b := sc.Bytes(); len(strings.TrimSpace(string(b))) > 0 {
			out = append(out, append(json.RawMessage{}, b...))
		}
	}
	require.NoError(t, sc.Err())
	return out
}

func usdtFreezeLog(addrTail, blockHex, txHash, logIndex string) RawLog {
	return RawLog{
		Address: "0xdac17f958d2ee523a2206206994597c13d831ec7",
		Topics:  []string{topic0("AddedBlackList(address)")},
		Data: "0x000000000000000000000000" +
			strings.TrimPrefix(addrTail, "0x"),
		BlockNumber:     blockHex,
		TransactionHash: txHash,
		LogIndex:        logIndex,
	}
}

// usdtDestroyLog builds a real-shaped DestroyedBlackFunds(address,uint256)
// entry: TWO 32-byte data words — [subject address][destroyed amount].
func usdtDestroyLog(addrTail, amountHex, blockHex, txHash, logIndex string) RawLog {
	return RawLog{
		Address: "0xdac17f958d2ee523a2206206994597c13d831ec7",
		Topics:  []string{topic0("DestroyedBlackFunds(address,uint256)")},
		Data: "0x000000000000000000000000" + strings.TrimPrefix(addrTail, "0x") +
			strings.TrimPrefix(amountHex, "0x"),
		BlockNumber:     blockHex,
		TransactionHash: txHash,
		LogIndex:        logIndex,
	}
}

func usdcFreezeLog(addrTopic, blockHex, txHash, logIndex string) RawLog {
	return RawLog{
		Address: "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48",
		Topics: []string{topic0("Blacklisted(address)"),
			"0x000000000000000000000000" + strings.TrimPrefix(addrTopic, "0x")},
		Data:            "0x",
		BlockNumber:     blockHex,
		TransactionHash: txHash,
		LogIndex:        logIndex,
	}
}

// Pinned keccak256 topic0 constants — guards against silent drift of the
// event-id computation.
func TestTopic0_Pinned(t *testing.T) {
	assert.Equal(t, "0x42e160154868087d6bfdc0ca23d96a1c1cfa32f1b72ba9ba27b69b98a0d819dc",
		topic0("AddedBlackList(address)"))
	assert.Equal(t, "0xd7e9ec6e6ecd65492dce6bf513cd6867560d49544421d0783ddf06e76c24470c",
		topic0("RemovedBlackList(address)"))
	// Real USDT signature carries the destroyed balance too:
	// DestroyedBlackFunds(address _blackListedUser, uint _balance).
	assert.Equal(t, "0x61e6e66b0d6339b2980aecc6ccc0039736791f0ccde9ed512e789a7fbdd698c6",
		topic0("DestroyedBlackFunds(address,uint256)"))
	assert.Equal(t, "0xffa4e6181777692565cf28528fc88fd1516ea86b56da075235fa575af6a4b855",
		topic0("Blacklisted(address)"))
	assert.Equal(t, "0x117e3210bb9aa7d9baff172026820255c6f6c30ba8999d1c2fd88e2848137c4e",
		topic0("UnBlacklisted(address)"))
}

// Fingerprint mirrors the wattr formula under the stablecoin_freeze
// namespace — pinned so the two sources can never collide.
func TestFingerprint_Namespace(t *testing.T) {
	sum := func(s string) string {
		h := sha256.Sum256([]byte(s))
		return hex.EncodeToString(h[:])
	}
	fp := Fingerprint("ethereum", "0xabc", "FROZEN", "usdt_blacklist")
	assert.Len(t, fp, 64)
	assert.Equal(t, fp, sum("stablecoin_freeze|ethereum|0xabc|FROZEN|usdt_blacklist"))
	assert.NotEqual(t, fp, sum("wallet_attribution|ethereum|0xabc|FROZEN|usdt_blacklist"))
}

// Endpoint host outside the allowlist and non-https schemes are refused.
func TestRPCClient_SSRF(t *testing.T) {
	_, err := NewRPCClient("https://evil.example.com", []string{"rpc.ankr.com"})
	require.Error(t, err)
	_, err = NewRPCClient("http://rpc.ankr.com", []string{"rpc.ankr.com"})
	require.Error(t, err) // http without insecure opt
	_, err = NewRPCClient("https://rpc.ankr.com/eth/key", []string{"rpc.ankr.com"})
	require.NoError(t, err)
}

// Missing cursor rows fail closed — never scan unbounded.
func TestScanner_MissingCursors(t *testing.T) {
	stub := &rpcStub{head: "0x100"}
	s := newTestScanner(t, stub, fakeCursors{rows: nil}, DefaultConfig())
	_, err := s.Download(context.Background())
	require.ErrorContains(t, err, "no freeze_scan_state rows")

	s = newTestScanner(t, stub, fakeCursors{rows: seedCursors()[:1]}, DefaultConfig())
	_, err = s.Download(context.Background())
	require.ErrorContains(t, err, "missing cursor row")
}

// Head RPC failure propagates; no artifact is produced.
func TestScanner_HeadError(t *testing.T) {
	stub := &rpcStub{failOn: map[string]bool{"eth_blockNumber": true}}
	s := newTestScanner(t, stub, fakeCursors{rows: seedCursors()}, DefaultConfig())
	_, err := s.Download(context.Background())
	require.Error(t, err)
}

// Mid-scan getLogs failure aborts the download — partial scans never
// reach publish, so cursors stay put.
func TestScanner_MidScanRPCError(t *testing.T) {
	stub := &rpcStub{
		head:   "0x989680", // 10,000,000 — above both cursors
		failOn: map[string]bool{"eth_getLogs": true},
	}
	s := newTestScanner(t, stub, fakeCursors{rows: seedCursors()}, DefaultConfig())
	_, err := s.Download(context.Background())
	require.Error(t, err)
	require.ErrorContains(t, err, "getLogs")
}

// A healthy scan pages by BlockRange, extracts both address encodings,
// resolves timestamps, and emits a checksummed single-file snapshot.
func TestScanner_HappyPath(t *testing.T) {
	usdtLog := usdtFreezeLog("0x00000000000000000000000000000000000000aa",
		"0x46babc", "0xAAAA000000000000000000000000000000000000000000000000000000000000", "0x3")
	usdcLog := usdcFreezeLog("0x00000000000000000000000000000000000000bb",
		"0x5cdd01", "0xBBBB000000000000000000000000000000000000000000000000000000000000", "0x0")
	var ranges [][2]int64
	stub := &rpcStub{
		head: "0x989680", // 10,000,000
		logs: func(p map[string]any) []RawLog {
			fv, _ := hexInt(p["fromBlock"].(string))
			tv, _ := hexInt(p["toBlock"].(string))
			ranges = append(ranges, [2]int64{fv, tv})
			switch p["address"].(string) {
			case "0xdac17f958d2ee523a2206206994597c13d831ec7":
				return []RawLog{usdtLog}
			case "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48":
				return []RawLog{usdcLog}
			}
			return nil
		},
		blocks: map[int64]string{0x46babc: "0x65000001", 0x5cdd01: "0x65000002"},
	}
	cfg := Config{BlockRange: 100, MaxBlocksPerRun: 250, Confirmations: 12}
	s := newTestScanner(t, stub, fakeCursors{rows: seedCursors()}, cfg)
	snap, err := s.Download(context.Background())
	require.NoError(t, err)
	require.Len(t, snap.Files, 1)
	f := snap.Files[0]
	assert.Equal(t, "freeze_events.jsonl", f.Name)
	assert.Len(t, f.Checksum, 64)
	assert.Equal(t, f.Checksum, snap.Checksum)
	assert.Empty(t, f.FinalURL)
	assert.Greater(t, f.SizeBytes, int64(0))

	// USDT window: (4634747 .. min(9999988, 4634747+250)] =
	//   4634748..4634997 → 3 pages of 100.
	// USDC: (6082464 .. min(9999988, 6082464+250)] = 6082465..6082714 → 3.
	assert.Len(t, ranges, 6)
	assert.Equal(t, [2]int64{4634748, 4634847}, ranges[0])
	assert.Equal(t, [2]int64{6082465, 6082564}, ranges[3])

	lines := readJSONL(t, f.Path)
	var hdr fileHeader
	require.NoError(t, json.Unmarshal(lines[0], &hdr))
	assert.Equal(t, 1, hdr.Version)
	require.Len(t, hdr.Contracts, 2)
	assert.False(t, hdr.Contracts[0].Empty)
	assert.Equal(t, int64(4634748), hdr.Contracts[0].FromBlock)
	assert.Equal(t, int64(4634997), hdr.Contracts[0].ToBlock)
	assert.Equal(t, int64(6082465), hdr.Contracts[1].FromBlock)
	assert.Equal(t, int64(6082714), hdr.Contracts[1].ToBlock)

	var evs []EventLine
	for _, ln := range lines[1:] {
		var e EventLine
		require.NoError(t, json.Unmarshal(ln, &e))
		evs = append(evs, e)
	}
	require.Len(t, evs, 6)
	// USDT: data-tail extraction, USDC: topics[1] — both normalized.
	assert.Equal(t, "0x00000000000000000000000000000000000000aa", evs[0].Wallet)
	assert.Equal(t, "AddedBlackList", evs[0].Event)
	assert.Equal(t, EventFreeze, evs[0].Type)
	assert.Equal(t, "0x00000000000000000000000000000000000000bb", evs[3].Wallet)
	assert.Equal(t, "Blacklisted", evs[3].Event)
	// Timestamps resolved via the batched eth_getBlockByNumber pass.
	assert.Equal(t, int64(0x65000001), evs[0].BlockTS)
	assert.Equal(t, int64(0x65000002), evs[3].BlockTS)
}

// DestroyedBlackFunds carries (address,uint256) — the wallet must come
// from data WORD 0; the blob tail is the destroyed amount. Regression
// for the two-word payload (1-arg tail extraction returned the amount).
func TestScanner_DestroyTwoWordData(t *testing.T) {
	dlog := usdtDestroyLog("0x00000000000000000000000000000000000000cc",
		"00000000000000000000000000000000000000000000000000000000000de0b6",
		"0x46babc", "0xCCCC000000000000000000000000000000000000000000000000000000000000", "0x1")
	stub := &rpcStub{
		head:   "0x989680",
		logs:   func(p map[string]any) []RawLog { return []RawLog{dlog} },
		blocks: map[int64]string{0x46babc: "0x65000001"},
	}
	cfg := Config{BlockRange: 100, MaxBlocksPerRun: 200, Confirmations: 12}
	s := newTestScanner(t, stub, fakeCursors{rows: seedCursors()}, cfg)
	snap, err := s.Download(context.Background())
	require.NoError(t, err)

	lines := readJSONL(t, snap.Files[0].Path)
	var evs []EventLine
	for _, ln := range lines[1:] {
		var e EventLine
		require.NoError(t, json.Unmarshal(ln, &e))
		evs = append(evs, e)
	}
	// USDT pages ×2 ranges emit the destroy twice; USDC also gets it
	// (stub ignores address) — every emitted line must decode word 0.
	require.NotEmpty(t, evs)
	for _, e := range evs {
		assert.Equal(t, "DestroyedBlackFunds", e.Event)
		assert.Equal(t, EventDestroy, e.Type)
		assert.Equal(t, "0x00000000000000000000000000000000000000cc", e.Wallet)
	}
}

// Window cap: MaxBlocksPerRun bounds the scan span; BlockRange pages it.
func TestScanner_WindowCapping(t *testing.T) {
	stub := &rpcStub{
		head: "0x989680", // 10,000,000
		logs: func(map[string]any) []RawLog { return nil },
	}
	cfg := Config{BlockRange: 10, MaxBlocksPerRun: 35, Confirmations: 12}
	s := newTestScanner(t, stub, fakeCursors{rows: seedCursors()}, cfg)
	snap, err := s.Download(context.Background())
	require.NoError(t, err)
	lines := readJSONL(t, snap.Files[0].Path)
	var hdr fileHeader
	require.NoError(t, json.Unmarshal(lines[0], &hdr))
	// USDT: 4634748 .. 4634747+35 = 4634782. USDC: 6082465 .. 6082499.
	assert.Equal(t, int64(4634782), hdr.Contracts[0].ToBlock)
	assert.Equal(t, int64(6082499), hdr.Contracts[1].ToBlock)
}

// Degenerate window (head−conf ≤ cursor): header-only empty sections,
// toBlock = fromBlock−1, zero event lines, no getLogs traffic.
func TestScanner_DegenerateWindow(t *testing.T) {
	cur := seedCursors()
	cur[0].LastScannedBlock = 6082464
	cur[1].LastScannedBlock = 6082464
	stub := &rpcStub{
		head: fmt.Sprintf("0x%x", 6082476), // floor = 6082464 = both cursors
		logs: func(map[string]any) []RawLog {
			t.Fatal("getLogs must not run on a degenerate window")
			return nil
		},
	}
	s := newTestScanner(t, stub, fakeCursors{rows: cur}, DefaultConfig())
	snap, err := s.Download(context.Background())
	require.NoError(t, err)
	lines := readJSONL(t, snap.Files[0].Path)
	require.Len(t, lines, 1) // header only
	var hdr fileHeader
	require.NoError(t, json.Unmarshal(lines[0], &hdr))
	require.Len(t, hdr.Contracts, 2)
	for _, w := range hdr.Contracts {
		assert.True(t, w.Empty)
		assert.Equal(t, w.FromBlock-1, w.ToBlock)
	}
}

// Malformed logs are skipped and counted, never fatal.
func TestScanner_MalformedLogsRejected(t *testing.T) {
	good := usdtFreezeLog("0x00000000000000000000000000000000000000aa",
		"0x46babc", "0xAAAA000000000000000000000000000000000000000000000000000000000000", "0x3")
	bad := []RawLog{
		{Address: "0xdac17f958d2ee523a2206206994597c13d831ec7",
			Topics: []string{topic0("AddedBlackList(address)")},
			Data:   "0xzz", BlockNumber: "0x46babc",
			TransactionHash: "0xCC00000000000000000000000000000000000000000000000000000000000000",
			LogIndex:        "0x1"}, // bad data
		{Address: "0xdac17f958d2ee523a2206206994597c13d831ec7",
			Topics: []string{"0xdeadbeef"}, Data: "0x" + strings.Repeat("00", 32),
			BlockNumber: "0x46babc", TransactionHash: "0xCC00000000000000000000000000000000000000000000000000000000000000",
			LogIndex: "0x2"}, // unknown topic
		{Address: "0xdac17f958d2ee523a2206206994597c13d831ec7",
			Topics: []string{topic0("AddedBlackList(address)")},
			Data:   "0x" + strings.Repeat("00", 32), Removed: true,
			BlockNumber: "0x46babc", TransactionHash: "0xCC00000000000000000000000000000000000000000000000000000000000000",
			LogIndex: "0x3"}, // reorged out
	}
	stub := &rpcStub{
		head: "0x989680",
		logs: func(p map[string]any) []RawLog {
			if p["address"].(string) == "0xdac17f958d2ee523a2206206994597c13d831ec7" {
				return append(bad, good)
			}
			return nil
		},
		blocks: map[int64]string{0x46babc: "0x65000001"},
	}
	// One page per contract keeps the test fast (80+ pages × rate limit).
	cfg := Config{BlockRange: 50000, MaxBlocksPerRun: 50000, Confirmations: 12}
	s := newTestScanner(t, stub, fakeCursors{rows: seedCursors()}, cfg)
	snap, err := s.Download(context.Background())
	require.NoError(t, err)
	lines := readJSONL(t, snap.Files[0].Path)
	var hdr fileHeader
	require.NoError(t, json.Unmarshal(lines[0], &hdr))
	assert.Equal(t, int64(3), hdr.Contracts[0].Rejected)
	var goodLines int
	for _, ln := range lines[1:] {
		var e EventLine
		require.NoError(t, json.Unmarshal(ln, &e))
		goodLines++
		assert.Equal(t, "0x00000000000000000000000000000000000000aa", e.Wallet)
	}
	assert.Equal(t, 1, goodLines)
}

// Cancelled context propagates — the download aborts.
func TestScanner_ContextCancel(t *testing.T) {
	stub := &rpcStub{head: "0x989680",
		logs: func(map[string]any) []RawLog { return nil }}
	s := newTestScanner(t, stub, fakeCursors{rows: seedCursors()}, DefaultConfig())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.Download(ctx)
	require.Error(t, err)
}

// Batch responses may arrive out-of-order (JSON-RPC 2.0 §6). The client
// must rebind results by response id — positional matching would attach
// each block's timestamp to the wrong block, corrupting label provenance.
func TestBlockTimestamps_OutOfOrderBatch(t *testing.T) {
	stub := &rpcStub{
		blocks:       map[int64]string{100: "0x65000001", 200: "0x65000002", 300: "0x65000003"},
		shuffleBatch: true,
	}
	srv := httptest.NewServer(http.HandlerFunc(stub.serve(t)))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	cli, err := NewRPCClient(srv.URL, []string{host},
		WithAllowedHosts(host), WithInsecureHTTP())
	require.NoError(t, err)

	ts, err := cli.BlockTimestamps(context.Background(), []int64{100, 200, 300})
	require.NoError(t, err)
	assert.Equal(t, int64(0x65000001), ts[100].Unix())
	assert.Equal(t, int64(0x65000002), ts[200].Unix())
	assert.Equal(t, int64(0x65000003), ts[300].Unix())
}
