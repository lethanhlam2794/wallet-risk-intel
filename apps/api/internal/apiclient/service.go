package apiclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"checkwallet/internal/db"
	"checkwallet/internal/middleware"
)

var (
	ErrNotFound      = errors.New("api client not found")
	ErrAdminLockout  = errors.New("operation would remove the last active admin")
	ErrSelfLockout   = errors.New("self-demotion or self-deactivation is not permitted")
	ErrInvalidCursor = errors.New("invalid cursor")
	ErrInvalidName   = errors.New("name must be 1-150 characters")
	ErrInvalidRole   = errors.New("role must be reader|admin")
)

const (
	defaultListLimit = 50
	maxListLimit     = 200
)

// Service owns admin reads/writes for api_clients. Reads never carry key
// material; writes are whitelisted {name, role, is_active}, column-scoped
// inside one tx guarded by the namespaced advisory lock.
type Service struct {
	pool *pgxpool.Pool
	q    *db.Queries
	// bootstrapHashes marks rows seeded by API_BOOTSTRAP_* — the boot
	// upsert resurrects their name/role/is_active on every restart.
	bootstrapHashes map[string]bool
	log             *slog.Logger
}

// New wires the service. bootstrapKeyHashes are the SHA-256 digests of
// the configured bootstrap keys (via middleware.HashAPIKey) — an empty
// slice means no env-managed rows exist.
func New(pool *pgxpool.Pool, q *db.Queries, bootstrapKeyHashes []string, log *slog.Logger) *Service {
	hashes := make(map[string]bool, len(bootstrapKeyHashes))
	for _, h := range bootstrapKeyHashes {
		hashes[h] = true
	}
	return &Service{pool: pool, q: q, bootstrapHashes: hashes, log: log}
}

// List pages clients newest-first with a keyset cursor on
// (created_at, id) — repo convention. limit defaults to 50 and clamps
// to 200; empty pages return Items:[] never nil.
func (s *Service) List(ctx context.Context, cursor string, limit int) (*Page, error) {
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	var curTs pgtype.Timestamptz
	var curID pgtype.Int8
	if cursor != "" {
		ts, id, err := decodeCursor(cursor)
		if err != nil {
			return nil, ErrInvalidCursor
		}
		curTs = pgtype.Timestamptz{Time: ts, Valid: true}
		curID = pgtype.Int8{Int64: id, Valid: true}
	}
	rows, err := s.q.ListAPIClients(ctx, db.ListAPIClientsParams{
		CursorTs: curTs, CursorID: curID, Lim: int64(limit + 1),
	})
	if err != nil {
		return nil, err
	}
	page := &Page{Items: []View{}}
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		page.NextCursor = encodeCursor(last.CreatedAt.Time, last.ID)
	}
	for _, r := range rows {
		page.Items = append(page.Items, *viewFrom(r))
	}
	return page, nil
}

// Create validates input, mints a "cw_" key, stores only its SHA-256
// digest, and returns the view plus the plaintext — the plaintext is
// disclosed exactly once here and never stored, logged, or returned
// again. Duplicate names are allowed; id is identity.
func (s *Service) Create(ctx context.Context, name, role string, actor Actor) (*Created, error) {
	name = strings.TrimSpace(name)
	if err := validateName(name); err != nil {
		return nil, err
	}
	if role != RoleReader && role != RoleAdmin {
		return nil, ErrInvalidRole
	}
	raw, err := GenerateKey()
	if err != nil {
		return nil, err
	}
	row, err := s.q.InsertAPIClient(ctx, db.InsertAPIClientParams{
		Name: name, KeyHash: middleware.HashAPIKey(raw), Role: role,
	})
	if err != nil {
		return nil, err
	}
	s.audit(actor, row.ID, []string{"name", "role", "key_hash"})
	return &Created{View: viewFrom(row), APIKey: raw}, nil
}

// Update applies a whitelisted patch inside one tx: namespaced advisory
// lock → row FOR UPDATE → post-state lockout guards → column-scoped
// writes. Guard (a) unconditionally rejects self-demotion/self-
// deactivation (self no-ops and self-renames pass); guard (b) rejects
// removing an active admin while zero other active admins remain —
// evaluated post-state, so touching an already-inactive admin succeeds.
func (s *Service) Update(ctx context.Context, id int64, p Patch, actor Actor) (*UpdateResult, error) {
	// Validate patch values before taking the lock — bad input is a 400,
	// never a partial write.
	var name string
	if p.Name != nil {
		name = strings.TrimSpace(*p.Name)
		if err := validateName(name); err != nil {
			return nil, err
		}
	}
	if p.Role != nil && *p.Role != RoleReader && *p.Role != RoleAdmin {
		return nil, ErrInvalidRole
	}

	if p.empty() {
		// PATCH {} is a no-op read — no tx needed, but env-managed rows
		// still disclose the resurrection warning.
		row, err := s.q.GetAPIClientByID(ctx, id)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return nil, ErrNotFound
		case err != nil:
			return nil, err
		}
		return &UpdateResult{View: viewFrom(row), Warnings: s.warningsFor(row)}, nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	// Namespaced xact advisory lock serializes every lockout-guarded
	// writer (ingestion's session lock space is raw source_id).
	if err := qtx.LockClientAdminTransitions(ctx); err != nil {
		return nil, err
	}

	row, err := qtx.GetAPIClientForUpdate(ctx, id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, ErrNotFound
	case err != nil:
		return nil, err
	}

	// Post-state the row reaches under this patch.
	newRole := row.Role
	if p.Role != nil {
		newRole = *p.Role
	}
	newActive := row.IsActive
	if p.IsActive != nil {
		newActive = *p.IsActive
	}

	// Guard (a): an admin never demotes or deactivates its own row —
	// footgun protection, never a legitimate op.
	if actor.ID == row.ID {
		selfDemote := row.Role == RoleAdmin && newRole != RoleAdmin
		selfDeactivate := row.IsActive && !newActive
		if selfDemote || selfDeactivate {
			return nil, ErrSelfLockout
		}
	}

	// Guard (b): removing an active admin (post-state) is rejected when
	// zero other role=admin AND is_active rows would remain.
	if row.Role == RoleAdmin && row.IsActive && !(newRole == RoleAdmin && newActive) {
		others, err := qtx.CountOtherActiveAdmins(ctx, row.ID)
		if err != nil {
			return nil, err
		}
		if others == 0 {
			return nil, ErrAdminLockout
		}
	}

	// Column-scoped writes: a concurrent PATCH on another field never
	// clobbers it; last_seen_at (mutated by TouchAPIClient) is never
	// reset.
	var changed []string
	if p.Name != nil && name != row.Name {
		if err := qtx.UpdateAPIClientName(ctx, db.UpdateAPIClientNameParams{
			ID: row.ID, Name: name,
		}); err != nil {
			return nil, err
		}
		changed = append(changed, "name")
	}
	if p.Role != nil && *p.Role != row.Role {
		if err := qtx.UpdateAPIClientRole(ctx, db.UpdateAPIClientRoleParams{
			ID: row.ID, Role: *p.Role,
		}); err != nil {
			return nil, err
		}
		changed = append(changed, "role")
	}
	if p.IsActive != nil && *p.IsActive != row.IsActive {
		if err := qtx.UpdateAPIClientActive(ctx, db.UpdateAPIClientActiveParams{
			ID: row.ID, IsActive: *p.IsActive,
		}); err != nil {
			return nil, err
		}
		changed = append(changed, "is_active")
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	if len(changed) > 0 {
		s.audit(actor, row.ID, changed)
	}
	fresh, err := s.q.GetAPIClientByID(ctx, row.ID)
	if err != nil {
		return nil, err
	}
	return &UpdateResult{View: viewFrom(fresh), Warnings: s.warningsFor(fresh)}, nil
}

// warningsFor discloses that a patched row is env-managed: the bootstrap
// upsert re-sets name/role/is_active at every boot, so a revocation only
// persists after the API_BOOTSTRAP_* var is unset.
func (s *Service) warningsFor(row db.ApiClient) []Warning {
	if !s.bootstrapHashes[row.KeyHash] {
		return nil
	}
	return []Warning{{
		Code: "env_managed",
		Message: "this client is managed by API_BOOTSTRAP_* env vars — the change is " +
			"reverted at restart while the env var is set; unset API_BOOTSTRAP_* to revoke permanently",
	}}
}

// audit emits the one structured domain log per mutation — the concrete
// target id and changed fields are what AccessLog's route pattern lacks.
func (s *Service) audit(actor Actor, targetID int64, fields []string) {
	if s.log == nil {
		return
	}
	s.log.Info("api client mutation",
		"actor_id", actor.ID,
		"actor_name", actor.Name,
		"target_id", targetID,
		"changed_fields", fields,
		"request_id", actor.RequestID)
}

// validateName enforces trim + 1..150 characters — VARCHAR(150) counts
// characters, so runes are the unit (len() counts bytes).
func validateName(name string) error {
	if n := utf8.RuneCountInString(name); n < 1 || n > 150 {
		return ErrInvalidName
	}
	return nil
}

func viewFrom(r db.ApiClient) *View {
	v := &View{
		ID:        r.ID,
		Name:      r.Name,
		Role:      r.Role,
		IsActive:  r.IsActive,
		CreatedAt: r.CreatedAt.Time.UTC(),
	}
	if r.LastSeenAt.Valid {
		t := r.LastSeenAt.Time.UTC()
		v.LastSeenAt = &t
	}
	return v
}

// listCursor mirrors the repo's base64 keyset token ({t, i}) — id is an
// int64 here, not a uuid.
type listCursor struct {
	Ts time.Time `json:"t"`
	ID int64     `json:"i"`
}

func encodeCursor(ts time.Time, id int64) string {
	b, _ := json.Marshal(listCursor{Ts: ts.UTC(), ID: id})
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(s string) (time.Time, int64, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return time.Time{}, 0, err
	}
	var c listCursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return time.Time{}, 0, err
	}
	if c.Ts.IsZero() {
		return time.Time{}, 0, ErrInvalidCursor
	}
	return c.Ts, c.ID, nil
}
