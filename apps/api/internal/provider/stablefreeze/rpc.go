package stablefreeze

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"checkwallet/internal/provider"
	"checkwallet/internal/provider/resilience"
)

// RawLog is one eth_getLogs entry. Hex quantities stay verbatim — the
// scanner decodes them.
type RawLog struct {
	Address          string   `json:"address"`
	Topics           []string `json:"topics"`
	Data             string   `json:"data"`
	BlockNumber      string   `json:"blockNumber"`
	TransactionHash  string   `json:"transactionHash"`
	LogIndex         string   `json:"logIndex"`
	Removed          bool     `json:"removed"`
	TransactionIndex string   `json:"transactionIndex"`
}

// RPCClient is a minimal Ethereum JSON-RPC client over the shared
// resilience toolkit: bounded timeout/retry, circuit breaker, rate
// limiter, per-request host allowlist (SSRF, spec §16). The endpoint is
// already fully derived (key embedded in path for Ankr-style providers).
type RPCClient struct {
	endpoint string
	hosts    []string
	insecure bool
	resOpts  []resilience.Option
	cli      *resilience.Client
}

// RPCOption configures the client (test hooks only).
type RPCOption func(*RPCClient)

// WithAllowedHosts overrides the SSRF allowlist (tests point at httptest).
func WithAllowedHosts(hosts ...string) RPCOption {
	return func(c *RPCClient) { c.hosts = hosts }
}

// WithInsecureHTTP permits plain-http endpoints. Test-only.
func WithInsecureHTTP() RPCOption {
	return func(c *RPCClient) { c.insecure = true }
}

// WithResilienceOption forwards a construction-time knob to the toolkit.
func WithResilienceOption(o resilience.Option) RPCOption {
	return func(c *RPCClient) { c.resOpts = append(c.resOpts, o) }
}

// NewRPCClient validates the endpoint and builds the resilience client.
// allowedHosts is the resolved provider's SSRF allowlist (e.g.
// provider.AllowedHostsFor("ankr")); the endpoint host must appear in it.
func NewRPCClient(endpoint string, allowedHosts []string, opts ...RPCOption) (*RPCClient, error) {
	c := &RPCClient{endpoint: endpoint, hosts: allowedHosts}
	for _, o := range opts {
		o(c)
	}
	if err := c.validateEndpoint(); err != nil {
		return nil, err
	}
	cli, err := resilience.NewClient(resilience.Config{
		Source:          SourceCode,
		Timeout:         30 * time.Second,
		Rate:            rate.Every(200 * time.Millisecond),
		Burst:           4,
		MaxLimiterWait:  8 * time.Second,
		Concurrency:     4,
		MaxRetries:      3,
		Backoff:         300 * time.Millisecond,
		BreakerFailures: 5,
		BreakerOpen:     60 * time.Second,
		MaxBodyBytes:    32 << 20, // large ranges of dense logs
		AllowedHosts:    c.hosts,
	}, c.resOpts...)
	if err != nil {
		return nil, fmt.Errorf("stablefreeze: resilience client: %w", err)
	}
	c.cli = cli
	return c, nil
}

func (c *RPCClient) validateEndpoint() error {
	u, err := url.Parse(c.endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		// Never echo the endpoint — it embeds the provider key in path.
		return fmt.Errorf("stablefreeze: unparseable endpoint url")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && c.insecure) {
		return fmt.Errorf("stablefreeze: scheme %q not allowed", u.Scheme)
	}
	for _, h := range c.hosts {
		if strings.EqualFold(u.Host, h) {
			return nil
		}
	}
	return fmt.Errorf("stablefreeze: host %q not in allowlist", u.Host)
}

type rpcReq struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

type rpcResp struct {
	ID     *int            `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// post dispatches one raw JSON-RPC body (single object or batch array)
// through the resilience client.
func (c *RPCClient) post(ctx context.Context, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, provider.Errorf(provider.ErrInternal, "stablefreeze: build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.cli.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	return res.Body, nil
}

func rpcError(method string, e struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}) *provider.LookupError {
	msg := strings.ToLower(e.Message)
	if e.Code == -32005 || e.Code == 429 || strings.Contains(msg, "rate") {
		return provider.Errorf(provider.ErrRateLimited, "stablefreeze %s: %s", method, e.Message)
	}
	return provider.Errorf(provider.ErrDegraded, "stablefreeze %s: rpc error %d: %s",
		method, e.Code, e.Message)
}

// call issues one request and returns its result.
func (c *RPCClient) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	body, err := json.Marshal(rpcReq{JSONRPC: "2.0", ID: 1, Method: method, Params: params})
	if err != nil {
		return nil, provider.Errorf(provider.ErrInternal, "stablefreeze: marshal: %v", err)
	}
	raw, err := c.post(ctx, body)
	if err != nil {
		return nil, err
	}
	var env rpcResp
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, provider.Errorf(provider.ErrMalformed, "stablefreeze %s: %v", method, err)
	}
	if env.Error != nil {
		return nil, rpcError(method, *env.Error)
	}
	if len(env.Result) == 0 {
		return nil, provider.Errorf(provider.ErrMalformed, "stablefreeze %s: empty result", method)
	}
	return env.Result, nil
}

// batch issues requests as a JSON-RPC array. Responses are matched back
// to requests by JSON-RPC id — the spec permits out-of-order batches and
// timestamps are evidence, so a positional misbind would silently corrupt
// provenance. A per-element error fails the whole batch (partial data is
// not publishable).
func (c *RPCClient) batch(ctx context.Context, reqs []rpcReq) ([]json.RawMessage, error) {
	body, err := json.Marshal(reqs)
	if err != nil {
		return nil, provider.Errorf(provider.ErrInternal, "stablefreeze: marshal: %v", err)
	}
	raw, err := c.post(ctx, body)
	if err != nil {
		return nil, err
	}
	var envs []rpcResp
	if err := json.Unmarshal(raw, &envs); err != nil {
		return nil, provider.Errorf(provider.ErrMalformed, "stablefreeze batch: %v", err)
	}
	if len(envs) != len(reqs) {
		return nil, provider.Errorf(provider.ErrMalformed,
			"stablefreeze batch: %d responses for %d requests", len(envs), len(reqs))
	}
	out := make([]json.RawMessage, len(reqs))
	seen := make([]bool, len(reqs))
	for _, env := range envs {
		if env.ID == nil || *env.ID < 0 || *env.ID >= len(reqs) {
			return nil, provider.Errorf(provider.ErrMalformed,
				"stablefreeze batch: response id %v out of range", env.ID)
		}
		i := *env.ID
		if seen[i] {
			return nil, provider.Errorf(provider.ErrMalformed,
				"stablefreeze batch: duplicate response id %d", i)
		}
		seen[i] = true
		if env.Error != nil {
			return nil, rpcError(reqs[i].Method, *env.Error)
		}
		out[i] = env.Result
	}
	return out, nil
}

// BlockNumber returns the node-observed head.
func (c *RPCClient) BlockNumber(ctx context.Context) (int64, error) {
	raw, err := c.call(ctx, "eth_blockNumber", []any{})
	if err != nil {
		return 0, err
	}
	n, ok := hexToBig(raw)
	if !ok {
		return 0, provider.Errorf(provider.ErrMalformed, "stablefreeze head: bad hex %s", string(raw))
	}
	return n.Int64(), nil
}

// GetLogs scans one contract for the given topic0 set over
// [fromBlock, toBlock] inclusive.
func (c *RPCClient) GetLogs(ctx context.Context, contract string,
	topics []string, fromBlock, toBlock int64) ([]RawLog, error) {

	// eth_getLogs takes a positional params ARRAY holding one filter
	// object — a bare object is rejected upstream ("missing field
	// `filter`"). topics[0] is an OR-list of the contract's event ids.
	params := map[string]any{
		"address":   contract,
		"fromBlock": fmt.Sprintf("0x%x", fromBlock),
		"toBlock":   fmt.Sprintf("0x%x", toBlock),
		"topics":    []any{topics},
	}
	raw, err := c.call(ctx, "eth_getLogs", []any{params})
	if err != nil {
		return nil, err
	}
	var logs []RawLog
	if err := json.Unmarshal(raw, &logs); err != nil {
		return nil, provider.Errorf(provider.ErrMalformed, "stablefreeze getLogs: %v", err)
	}
	return logs, nil
}

// BlockTimestamps resolves timestamps for the given distinct block
// numbers via chunked batched eth_getBlockByNumber calls.
func (c *RPCClient) BlockTimestamps(ctx context.Context, numbers []int64) (map[int64]time.Time, error) {
	out := make(map[int64]time.Time, len(numbers))
	const chunk = 64
	for lo := 0; lo < len(numbers); lo += chunk {
		hi := lo + chunk
		if hi > len(numbers) {
			hi = len(numbers)
		}
		reqs := make([]rpcReq, 0, hi-lo)
		for i, n := range numbers[lo:hi] {
			reqs = append(reqs, rpcReq{
				JSONRPC: "2.0", ID: i, Method: "eth_getBlockByNumber",
				Params: []any{fmt.Sprintf("0x%x", n), false},
			})
		}
		results, err := c.batch(ctx, reqs)
		if err != nil {
			return nil, err
		}
		for i, raw := range results {
			var blk struct {
				Timestamp string `json:"timestamp"`
			}
			if err := json.Unmarshal(raw, &blk); err != nil || blk.Timestamp == "" {
				return nil, provider.Errorf(provider.ErrMalformed,
					"stablefreeze block %d: %v", numbers[lo+i], err)
			}
			n, ok := hexToBig(json.RawMessage(`"` + blk.Timestamp + `"`))
			if !ok {
				return nil, provider.Errorf(provider.ErrMalformed,
					"stablefreeze block %d: bad ts %s", numbers[lo+i], blk.Timestamp)
			}
			out[numbers[lo+i]] = time.Unix(n.Int64(), 0).UTC()
		}
	}
	return out, nil
}

// hexToBig decodes a 0x-hex JSON quantity (with or without quotes).
func hexToBig(s json.RawMessage) (*big.Int, bool) {
	str := strings.Trim(strings.TrimSpace(string(s)), `"`)
	if !strings.HasPrefix(str, "0x") {
		return nil, false
	}
	n, ok := new(big.Int).SetString(str[2:], 16)
	return n, ok
}
