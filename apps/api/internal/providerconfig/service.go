package providerconfig

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"checkwallet/internal/db"
	"checkwallet/internal/provider"
)

// Admin write/read service for provider_configs (phase 4). Reads are
// masked — api_key never leaves in plaintext; writes are whitelisted to
// {api_key, base_url, enabled_chains, status}, column-scoped inside one
// tx with a masked audit row per changed field.
var (
	ErrUnknownProvider = errors.New("unknown provider")
	ErrInvalidStatus   = errors.New("status must be active|inactive")
	ErrInvalidChain    = errors.New("unknown network code in enabled_chains")
	ErrEncPrefix       = errors.New("api_key must not carry an enc: prefix")
	ErrInvalidCursor   = errors.New("invalid cursor")
)

// HostNotAllowedError carries the adapter allowlist so the 400 can echo it.
type HostNotAllowedError struct {
	Host    string
	Allowed []string
}

func (e *HostNotAllowedError) Error() string {
	return fmt.Sprintf("base_url host %q not in allowlist %v", e.Host, e.Allowed)
}

// MaskedKey is the api_key wire contract: {set, last4|null}. last4 is
// suppressed for keys shorter than 8 chars (critic R1 mask pin).
type MaskedKey struct {
	Set   bool    `json:"set"`
	Last4 *string `json:"last4"`
}

// requiresRestartFields are boot-effective only — adapters are built once
// at boot; status is the sole runtime-effective field (critic R1).
var requiresRestartFields = []string{"api_key", "base_url", "enabled_chains"}

// View is one provider's stored config on the wire.
type View struct {
	SourceCode      string     `json:"source_code"`
	Configured      bool       `json:"configured"` // provider_configs row exists
	APIKey          MaskedKey  `json:"api_key"`
	BaseURL         *string    `json:"base_url"`
	EnabledChains   *[]string  `json:"enabled_chains"`
	Status          string     `json:"status"`
	CatalogActive   bool       `json:"catalog_active"`   // data_sources.is_active
	EffectiveActive bool       `json:"effective_active"` // catalog AND operator status
	RequiresRestart []string   `json:"requires_restart"`
	UpdatedBy       *string    `json:"updated_by"`
	UpdatedAt       *time.Time `json:"updated_at"`
}

// Warning discloses a non-obvious effect of a write (greatify parity).
type Warning struct {
	Code    string `json:"code"`
	Field   string `json:"field,omitempty"`
	Network string `json:"network,omitempty"`
	Message string `json:"message"`
}

// Patch carries whitelisted PUT fields — pointers distinguish absent from
// explicit empty ([] and "" are meaningful overrides).
type Patch struct {
	APIKey        *string
	BaseURL       *string
	EnabledChains *[]string
	Status        *string
}

// empty reports the patch carries no fields.
func (p Patch) empty() bool {
	return p.APIKey == nil && p.BaseURL == nil &&
		p.EnabledChains == nil && p.Status == nil
}

// Actor is the authenticated client performing a write.
type Actor struct {
	ID   string // api_client name
	Type string // "api_client"
}

// UpdateResult is the post-write view plus disclosure warnings.
type UpdateResult struct {
	View     *View     `json:"view"`
	Warnings []Warning `json:"warnings"`
}

// ChangeItem is one audit row on the wire.
type ChangeItem struct {
	ID        string          `json:"id"`
	Field     string          `json:"field"`
	Previous  json.RawMessage `json:"previous_value"`
	New       json.RawMessage `json:"new_value"`
	ActorID   *string         `json:"actor_id"`
	ActorType string          `json:"actor_type"`
	ChangedAt time.Time       `json:"changed_at"`
}

// ChangesPage is the keyset-paginated audit listing.
type ChangesPage struct {
	Items      []ChangeItem `json:"items"`
	NextCursor string       `json:"next_cursor,omitempty"`
}

// Service owns admin reads/writes for provider_configs.
type Service struct {
	pool     *pgxpool.Pool
	q        *db.Queries
	keys     *Keystore
	resolver *Resolver // boot-time in-effect snapshot for drift warnings
	chains   map[string][]string
	netCodes map[string]bool
	hostsFor func(code string) []string
}

// NewService wires the admin service. chains maps each network code to its
// failover-chain provider codes (for the last-active-member warning);
// netCodes is the registered network set enabled_chains validates against.
func NewService(pool *pgxpool.Pool, q *db.Queries, keys *Keystore,
	resolver *Resolver, chains map[string][]string, netCodes []string,
	opts ...Option) *Service {

	deps := &resolverDeps{allowedHosts: provider.AllowedHostsFor}
	for _, o := range opts {
		o(deps)
	}
	nets := make(map[string]bool, len(netCodes))
	for _, c := range netCodes {
		nets[c] = true
	}
	return &Service{pool: pool, q: q, keys: keys, resolver: resolver,
		chains: chains, netCodes: nets, hostsFor: deps.allowedHosts}
}

// List returns one view per registered tx provider — absent rows surface
// as configured:false so every provider is PUT-discoverable.
func (s *Service) List(ctx context.Context) ([]View, error) {
	out := make([]View, 0, len(descriptors))
	for _, code := range Codes() {
		v, err := s.view(ctx, code)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, nil
}

// Get returns one provider's view; unknown codes are 404-material.
func (s *Service) Get(ctx context.Context, code string) (*View, error) {
	if descriptorFor(code) == nil {
		return nil, ErrUnknownProvider
	}
	return s.view(ctx, code)
}

func (s *Service) view(ctx context.Context, code string) (*View, error) {
	v := &View{SourceCode: code, Status: "active",
		RequiresRestart: requiresRestartFields}
	status, err := s.effectiveStatus(ctx, code)
	if err != nil {
		return nil, err
	}
	v.CatalogActive = status.IsActive
	v.Status = status.ConfigStatus
	v.EffectiveActive = status.IsActive && status.ConfigStatus == "active"

	row, err := s.q.GetProviderConfig(ctx, code)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return v, nil
	case err != nil:
		return nil, err
	}
	v.Configured = true
	v.APIKey = s.maskStored(row.ApiKey)
	if row.BaseUrl.Valid {
		v.BaseURL = &row.BaseUrl.String
	}
	if row.EnabledChains != nil {
		var chains []string
		if err := json.Unmarshal(row.EnabledChains, &chains); err == nil {
			v.EnabledChains = &chains
		}
	}
	if row.UpdatedBy.Valid {
		v.UpdatedBy = &row.UpdatedBy.String
	}
	if row.UpdatedAt.Valid {
		t := row.UpdatedAt.Time.UTC()
		v.UpdatedAt = &t
	}
	return v, nil
}

func (s *Service) effectiveStatus(ctx context.Context, code string) (db.GetProviderEffectiveStatusRow, error) {
	row, err := s.q.GetProviderEffectiveStatus(ctx, code)
	if errors.Is(err, pgx.ErrNoRows) {
		// Catalog row absent — not active at the catalog level.
		return db.GetProviderEffectiveStatusRow{IsActive: false, ConfigStatus: "active"}, nil
	}
	return row, err
}

// maskStored builds the masked api_key view: decrypts enc:v1 values to
// recover last4, degrades to last4:null on undecryptable data — never
// emits the plaintext or ciphertext itself.
func (s *Service) maskStored(t pgtype.Text) MaskedKey {
	if !t.Valid || t.String == "" {
		return MaskedKey{Set: false}
	}
	plain, err := s.keys.Decrypt(t.String)
	if err != nil {
		return MaskedKey{Set: true}
	}
	return maskPlain(plain)
}

func maskPlain(plain string) MaskedKey {
	if plain == "" {
		return MaskedKey{Set: false}
	}
	m := MaskedKey{Set: true}
	if len(plain) >= 8 {
		l := plain[len(plain)-4:]
		m.Last4 = &l
	}
	return m
}

// Update validates the patch, writes changed fields column-scoped inside
// one tx with masked audit rows, then returns the post-write view and
// warnings (requires_restart drift, empty-chains kill, last-active).
func (s *Service) Update(ctx context.Context, code string, actor Actor, p Patch) (*UpdateResult, error) {
	if descriptorFor(code) == nil {
		return nil, ErrUnknownProvider
	}
	if err := s.validate(code, p); err != nil {
		return nil, err
	}
	if p.empty() {
		// PUT {} is a no-op read — never materializes an all-NULL row.
		v, err := s.Get(ctx, code)
		if err != nil {
			return nil, err
		}
		return &UpdateResult{View: v}, nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	row, err := qtx.GetProviderConfigForUpdate(ctx, code)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// ON CONFLICT DO NOTHING keeps a concurrent first-write from
		// erroring the tx: the loser blocks until the winner commits,
		// then locks the winner's row below — serialized, never a 500.
		if err := qtx.InsertProviderConfigRow(ctx, db.InsertProviderConfigRowParams{
			SourceCode: code, UpdatedBy: pgtype.Text{String: actor.ID, Valid: actor.ID != ""}}); err != nil {
			return nil, err
		}
		row, err = qtx.GetProviderConfigForUpdate(ctx, code)
		if err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	}

	var warnings []Warning
	record := func(field string, prev, next any) error {
		pj, err := json.Marshal(prev)
		if err != nil {
			return err
		}
		nj, err := json.Marshal(next)
		if err != nil {
			return err
		}
		_, err = qtx.InsertProviderConfigChange(ctx, db.InsertProviderConfigChangeParams{
			SourceCode:    code,
			ActorID:       pgtype.Text{String: actor.ID, Valid: actor.ID != ""},
			ActorType:     actor.Type,
			Field:         field,
			PreviousValue: pj,
			NewValue:      nj,
		})
		return err
	}

	if p.APIKey != nil {
		// Changed-detection compares stored representation, not Go
		// values: NULL ⇒ always a change ("" is an explicit override,
		// never NULL-equal); an undecryptable stored value is changed and
		// audits {set:true} — it existed even if unreadable.
		oldMask := MaskedKey{Set: false}
		var oldPlain string
		readable := true
		if row.ApiKey.Valid && row.ApiKey.String != "" {
			dec, derr := s.keys.Decrypt(row.ApiKey.String)
			if derr != nil {
				oldMask = MaskedKey{Set: true}
				readable = false
			} else {
				oldPlain = dec
				oldMask = maskPlain(dec)
			}
		}
		if !row.ApiKey.Valid || !readable || *p.APIKey != oldPlain {
			stored, err := s.keys.Encrypt(*p.APIKey)
			if err != nil {
				return nil, err
			}
			if err := qtx.UpdateProviderConfigAPIKey(ctx, db.UpdateProviderConfigAPIKeyParams{
				SourceCode: code,
				ApiKey:     pgtype.Text{String: stored, Valid: true},
				UpdatedBy:  pgtype.Text{String: actor.ID, Valid: actor.ID != ""},
			}); err != nil {
				return nil, err
			}
			if err := record("api_key", oldMask, maskPlain(*p.APIKey)); err != nil {
				return nil, err
			}
		}
	}

	if p.BaseURL != nil {
		// NULL stored ⇒ always a change; "" is an explicit override.
		if !row.BaseUrl.Valid || row.BaseUrl.String != *p.BaseURL {
			if err := qtx.UpdateProviderConfigBaseURL(ctx, db.UpdateProviderConfigBaseURLParams{
				SourceCode: code,
				BaseUrl:    pgtype.Text{String: *p.BaseURL, Valid: true},
				UpdatedBy:  pgtype.Text{String: actor.ID, Valid: actor.ID != ""},
			}); err != nil {
				return nil, err
			}
			var prev any
			if row.BaseUrl.Valid {
				prev = row.BaseUrl.String
			}
			if err := record("base_url", prev, *p.BaseURL); err != nil {
				return nil, err
			}
		}
	}

	if p.EnabledChains != nil {
		// NULL stored ⇒ always a change; [] is an explicit override —
		// slices.Equal alone would conflate nil and empty.
		var old []string
		changed := true
		if row.EnabledChains != nil {
			if err := json.Unmarshal(row.EnabledChains, &old); err != nil {
				return nil, err
			}
			changed = !slices.Equal(*p.EnabledChains, old)
		}
		if changed {
			raw, err := json.Marshal(*p.EnabledChains)
			if err != nil {
				return nil, err
			}
			if err := qtx.UpdateProviderConfigEnabledChains(ctx, db.UpdateProviderConfigEnabledChainsParams{
				SourceCode:    code,
				EnabledChains: raw,
				UpdatedBy:     pgtype.Text{String: actor.ID, Valid: actor.ID != ""},
			}); err != nil {
				return nil, err
			}
			if err := record("enabled_chains", old, *p.EnabledChains); err != nil {
				return nil, err
			}
			if len(*p.EnabledChains) == 0 {
				warnings = append(warnings, Warning{
					Code:    "no_enabled_chains",
					Field:   "enabled_chains",
					Message: "enabled_chains=[] disables the provider on every network",
				})
			}
		}
	}

	if p.Status != nil && *p.Status != row.Status {
		if *p.Status == "inactive" {
			warnings = append(warnings, s.lastActiveWarnings(ctx, code)...)
		}
		if err := qtx.UpdateProviderConfigStatus(ctx, db.UpdateProviderConfigStatusParams{
			SourceCode: code,
			Status:     *p.Status,
			UpdatedBy:  pgtype.Text{String: actor.ID, Valid: actor.ID != ""},
		}); err != nil {
			return nil, err
		}
		if err := record("status", row.Status, *p.Status); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	warnings = append(warnings, s.driftWarnings(code, p)...)
	v, err := s.Get(ctx, code)
	if err != nil {
		return nil, err
	}
	return &UpdateResult{View: v, Warnings: warnings}, nil
}

// validate runs every patch check before any write — bad input is a 400,
// never a partial write.
func (s *Service) validate(code string, p Patch) error {
	if p.APIKey != nil && strings.HasPrefix(*p.APIKey, encPrefix) {
		return ErrEncPrefix
	}
	if p.BaseURL != nil && *p.BaseURL != "" {
		u, err := url.Parse(*p.BaseURL)
		if err != nil || u.Host == "" || u.Scheme != "https" {
			return &HostNotAllowedError{Host: *p.BaseURL, Allowed: s.hostsFor(code)}
		}
		allowed := s.hostsFor(code)
		ok := false
		for _, h := range allowed {
			if strings.EqualFold(u.Host, h) {
				ok = true
			}
		}
		if !ok {
			return &HostNotAllowedError{Host: u.Host, Allowed: allowed}
		}
	}
	if p.Status != nil && *p.Status != "active" && *p.Status != "inactive" {
		return ErrInvalidStatus
	}
	if p.EnabledChains != nil {
		for _, c := range *p.EnabledChains {
			if !s.netCodes[c] {
				return fmt.Errorf("%w: %q", ErrInvalidChain, c)
			}
		}
	}
	return nil
}

// driftWarnings compares the newly stored value to the boot-time
// in-effect config — stored≠in-effect ⇒ the change needs a restart
// (status is the only runtime-effective field and is excluded).
func (s *Service) driftWarnings(code string, p Patch) []Warning {
	if s.resolver == nil {
		return nil
	}
	snap := s.resolver.Get(code)
	if snap == nil {
		return nil
	}
	warn := func(field, stored, inEffect string) *Warning {
		if stored == inEffect {
			return nil
		}
		return &Warning{Code: "requires_restart", Field: field,
			Message: fmt.Sprintf("%s is stored but the running adapters use the boot-resolved value until restart", field)}
	}
	var out []Warning
	if p.APIKey != nil {
		if w := warn("api_key", *p.APIKey, snap.APIKey); w != nil {
			out = append(out, *w)
		}
	}
	if p.BaseURL != nil {
		if w := warn("base_url", *p.BaseURL, snap.BaseURL); w != nil {
			out = append(out, *w)
		}
	}
	if p.EnabledChains != nil && !slices.Equal(*p.EnabledChains, snap.EnabledChains) {
		out = append(out, Warning{Code: "requires_restart", Field: "enabled_chains",
			Message: "enabled_chains is stored but the running adapters use the boot-resolved value until restart"})
	}
	return out
}

// lastActiveWarnings flags a status=inactive write that leaves a failover
// chain with zero effective-active members (greatify parity).
func (s *Service) lastActiveWarnings(ctx context.Context, code string) []Warning {
	var out []Warning
	for net, members := range s.chains {
		if !slices.Contains(members, code) {
			continue
		}
		lastActive := true
		for _, m := range members {
			if m == code {
				continue
			}
			active, err := loadStatus(ctx, s.q, m)
			if err != nil {
				continue
			}
			if active {
				lastActive = false
				break
			}
		}
		if lastActive {
			out = append(out, Warning{
				Code:    "last_active_member",
				Network: net,
				Message: fmt.Sprintf("deactivating %s leaves the %s failover chain with no active member", code, net),
			})
		}
	}
	return out
}

// ListChanges pages the audit trail newest-first with a keyset cursor on
// (changed_at, id) — repo cursor convention.
func (s *Service) ListChanges(ctx context.Context, code, cursor string, limit int) (*ChangesPage, error) {
	if descriptorFor(code) == nil {
		return nil, ErrUnknownProvider
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	var curTs pgtype.Timestamptz
	var curID pgtype.UUID
	if cursor != "" {
		ts, id, err := decodeChangeCursor(cursor)
		if err != nil {
			return nil, ErrInvalidCursor
		}
		curTs = pgtype.Timestamptz{Time: ts, Valid: true}
		curID = pgtype.UUID{Bytes: id, Valid: true}
	}
	rows, err := s.q.ListProviderConfigChanges(ctx, db.ListProviderConfigChangesParams{
		SourceCode: code, CursorTs: curTs, CursorID: curID, Lim: int64(limit + 1),
	})
	if err != nil {
		return nil, err
	}
	page := &ChangesPage{Items: []ChangeItem{}}
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		page.NextCursor = encodeChangeCursor(last.ChangedAt.Time, last.ID.Bytes)
	}
	for _, r := range rows {
		item := ChangeItem{
			ID:        uuid.UUID(r.ID.Bytes).String(),
			Field:     r.Field,
			ActorType: r.ActorType,
			ChangedAt: r.ChangedAt.Time.UTC(),
		}
		if r.ActorID.Valid {
			item.ActorID = &r.ActorID.String
		}
		if r.PreviousValue != nil {
			item.Previous = json.RawMessage(r.PreviousValue)
		} else {
			item.Previous = json.RawMessage("null")
		}
		if r.NewValue != nil {
			item.New = json.RawMessage(r.NewValue)
		} else {
			item.New = json.RawMessage("null")
		}
		page.Items = append(page.Items, item)
	}
	return page, nil
}

// changeCursor mirrors the repo's base64 keyset token ({t, i}) — id is a
// uuid here, not an int64.
type changeCursor struct {
	Ts time.Time `json:"t"`
	ID string    `json:"i"`
}

func encodeChangeCursor(ts time.Time, id uuid.UUID) string {
	b, _ := json.Marshal(changeCursor{Ts: ts.UTC(), ID: id.String()})
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeChangeCursor(s string) (time.Time, uuid.UUID, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return time.Time{}, uuid.Nil, err
	}
	var c changeCursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return time.Time{}, uuid.Nil, err
	}
	id, err := uuid.Parse(c.ID)
	if err != nil {
		return time.Time{}, uuid.Nil, err
	}
	return c.Ts, id, nil
}
