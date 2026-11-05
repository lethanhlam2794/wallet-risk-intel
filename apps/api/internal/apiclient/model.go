// Package apiclient owns the admin client-management domain: key
// issuance, list/create/patch with admin-lockout guards, env-managed
// disclosures, and a structured mutation audit log.
package apiclient

import "time"

// Roles — both are creatable; named admin keys replace a shared
// bootstrap-admin.
const (
	RoleReader = "reader"
	RoleAdmin  = "admin"
)

// View is one api_clients row on the wire. Enumerated fields only —
// key_hash and raw key material never leave this surface.
type View struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Role       string     `json:"role"`
	IsActive   bool       `json:"is_active"`
	CreatedAt  time.Time  `json:"created_at"`
	LastSeenAt *time.Time `json:"last_seen_at"`
}

// Page is a keyset-paginated list of client views.
type Page struct {
	Items      []View `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// Warning discloses a non-obvious effect of a write.
type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Created carries the post-insert view plus the only-ever-shown
// plaintext key.
type Created struct {
	View   *View
	APIKey string
}

// Patch carries whitelisted PATCH fields — pointers distinguish absent
// from explicit values.
type Patch struct {
	Name     *string
	Role     *string
	IsActive *bool
}

// empty reports the patch carries no fields (PATCH {} is a no-op read).
func (p Patch) empty() bool {
	return p.Name == nil && p.Role == nil && p.IsActive == nil
}

// UpdateResult is the post-write view plus disclosure warnings.
type UpdateResult struct {
	View     *View
	Warnings []Warning
}

// Actor is the authenticated client performing a mutation. RequestID is
// passed in from the handler (middleware.GetRequestID) — never
// re-derived inside the service.
type Actor struct {
	ID        int64 // api_clients.id of the acting client
	Name      string
	RequestID string
}
