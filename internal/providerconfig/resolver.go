package providerconfig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"

	"checkwallet/internal/config"
	"checkwallet/internal/db"
	"checkwallet/internal/provider"
)

// Origin reports where a resolved field's value came from.
type Origin string

const (
	OriginDB      Origin = "db"      // provider_configs row supplied the value
	OriginEnv     Origin = "env"     // env var set; DB field absent/NULL
	OriginDefault Origin = "default" // env var unset; compiled default
)

// Resolved is the effective boot-time config for one provider — the
// DB-over-env merge. The resolver retains it as the in-effect snapshot the
// admin API diffs stored values against for requires_restart warnings.
type Resolved struct {
	SourceCode    string
	APIKey        string
	BaseURL       string
	EnabledChains []string
	Status        string            // provider_configs.status; "active" when no row
	Origins       map[string]Origin // field → db|env|default
	RowPresent    bool              // whether a provider_configs row exists
}

// Error is a boot-fatal resolution failure naming provider+field — env
// fallback happens only on absent values, never on errors (critic R1).
type Error struct {
	Provider string
	Field    string
	Err      error
}

func (e *Error) Error() string {
	return fmt.Sprintf("providerconfig: %s: %s: %v", e.Provider, e.Field, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

// Store is the slice of db.Queries the resolver needs — the same contract
// the real queries honour (absent row ⇒ pgx.ErrNoRows).
type Store interface {
	GetProviderConfig(ctx context.Context, code string) (db.ProviderConfig, error)
	ListProviderConfigs(ctx context.Context) ([]db.ProviderConfig, error)
}

// Resolver holds the boot-time resolved config snapshot per provider.
type Resolver struct {
	resolved map[string]*Resolved
}

// Get returns the resolved config for a registered provider, nil for an
// unknown code.
func (r *Resolver) Get(code string) *Resolved {
	if r == nil {
		return nil
	}
	return r.resolved[code]
}

type resolverDeps struct {
	log          *slog.Logger
	allowedHosts func(code string) []string
	getenv       func(string) string
	statusLoader func(ctx context.Context, code string) (bool, error)
}

// Option customizes resolution (tests).
type Option func(*resolverDeps)

// WithLogger overrides the boot logger (origins are logged, never values).
func WithLogger(log *slog.Logger) Option {
	return func(d *resolverDeps) { d.log = log }
}

// WithAllowedHosts overrides the SSRF allowlist lookup (tests).
func WithAllowedHosts(f func(code string) []string) Option {
	return func(d *resolverDeps) { d.allowedHosts = f }
}

// WithGetenv overrides env-var presence lookup for origin tracking (tests).
func WithGetenv(f func(string) string) Option {
	return func(d *resolverDeps) { d.getenv = f }
}

// ResolveAll resolves every descriptor's provider against its
// provider_configs row over env, returning the snapshot map. Any error is
// boot-fatal and names provider+field. Rows for unregistered source codes
// are ignored with a warn — never resolved, never fatal.
func ResolveAll(ctx context.Context, cfg *config.Config, store Store,
	keys *Keystore, opts ...Option) (*Resolver, error) {

	deps := &resolverDeps{
		log:          slog.New(slog.DiscardHandler),
		allowedHosts: provider.AllowedHostsFor,
		getenv:       os.Getenv,
	}
	for _, o := range opts {
		o(deps)
	}

	r := &Resolver{resolved: map[string]*Resolved{}}
	for _, code := range Codes() {
		res, err := resolveOne(ctx, cfg, store, keys, deps, code)
		if err != nil {
			return nil, err
		}
		r.resolved[code] = res
		deps.log.Info("provider config resolved",
			"provider", code, "api_key", res.Origins["api_key"],
			"base_url", res.Origins["base_url"],
			"enabled_chains", res.Origins["enabled_chains"],
			"status", res.Status)
	}

	// Rows for codes outside the descriptor set are stale leftovers — the
	// resolver never reads them; surfacing is a warn, not a boot failure.
	if rows, err := store.ListProviderConfigs(ctx); err != nil {
		deps.log.Warn("providerconfig: could not list rows for stale-row check",
			"error", err)
	} else {
		for _, row := range rows {
			if descriptorFor(row.SourceCode) == nil {
				deps.log.Warn("providerconfig: ignoring row for unregistered source code",
					"source_code", row.SourceCode)
			}
		}
	}
	return r, nil
}

func resolveOne(ctx context.Context, cfg *config.Config, store Store,
	keys *Keystore, deps *resolverDeps, code string) (*Resolved, error) {

	d := descriptorFor(code)
	res := &Resolved{SourceCode: code, Status: "active",
		Origins: map[string]Origin{}}

	row, err := store.GetProviderConfig(ctx, code)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return nil, &Error{Provider: code, Field: "config", Err: err}
	default:
		res.RowPresent = true
		res.Status = row.Status
	}

	// api_key: NULL ⇒ env; non-NULL (including "") ⇒ the DB value,
	// decrypted when it carries the enc:v1 envelope.
	if !res.RowPresent || !row.ApiKey.Valid {
		res.APIKey = d.apiKey(cfg)
		res.Origins["api_key"] = deps.origin(d.KeyEnv)
	} else {
		v, err := keys.Decrypt(row.ApiKey.String)
		if err != nil {
			return nil, &Error{Provider: code, Field: "api_key", Err: err}
		}
		res.APIKey = v
		res.Origins["api_key"] = OriginDB
	}

	// base_url: NULL ⇒ env; "" ⇒ explicit override. A stored non-empty
	// URL must satisfy the adapter's own SSRF allowlist.
	if !res.RowPresent || !row.BaseUrl.Valid {
		res.BaseURL = d.base(cfg)
		res.Origins["base_url"] = deps.origin(d.BaseURLEnv)
	} else {
		v := row.BaseUrl.String
		if v != "" {
			if err := checkAllowedHost(deps.allowedHosts(code), v); err != nil {
				return nil, &Error{Provider: code, Field: "base_url", Err: err}
			}
		}
		res.BaseURL = v
		res.Origins["base_url"] = OriginDB
	}

	// enabled_chains: NULL ⇒ env comma list; [] ⇒ explicit kill channel.
	if !res.RowPresent || row.EnabledChains == nil {
		res.EnabledChains = strings.Split(d.chains(cfg), ",")
		res.Origins["enabled_chains"] = deps.origin(d.ChainsEnv)
	} else {
		var chains []string
		if err := json.Unmarshal(row.EnabledChains, &chains); err != nil {
			return nil, &Error{Provider: code, Field: "enabled_chains",
				Err: fmt.Errorf("malformed chains json: %w", err)}
		}
		res.EnabledChains = chains
		if res.EnabledChains == nil {
			res.EnabledChains = []string{}
		}
		res.Origins["enabled_chains"] = OriginDB
	}
	return res, nil
}

// origin reports env when the var is set in the process env, default when
// the compiled fallback carried the value.
func (d *resolverDeps) origin(envName string) Origin {
	if envName != "" && d.getenv(envName) != "" {
		return OriginEnv
	}
	return OriginDefault
}

// checkAllowedHost applies the same scheme+host allowlist adapters
// enforce on their own base URLs (https only; host equality,
// case-insensitive) — a stored http:// URL is a resolution error naming
// provider+field, not a deferred adapter failure.
func checkAllowedHost(allowed []string, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid base url %q", raw)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("base url %q must use https scheme", raw)
	}
	for _, h := range allowed {
		if strings.EqualFold(u.Host, h) {
			return nil
		}
	}
	return fmt.Errorf("host %q not in allowlist %v", u.Host, allowed)
}
