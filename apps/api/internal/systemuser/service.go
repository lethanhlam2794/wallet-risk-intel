// Package systemuser owns human admin identities (system_users) for the
// admin console: username+password authentication (bcrypt) and user
// provisioning. API clients (api_clients / X-API-Key) are a separate
// principal type and unchanged.
package systemuser

import (
	"context"
	"errors"
	"log/slog"
	"regexp"

	"golang.org/x/crypto/bcrypt"

	"checkwallet/internal/db"
)

var (
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrInvalidUsername    = errors.New("username must be 3-80 chars of [a-zA-Z0-9_.-]")
	ErrInvalidPassword    = errors.New("password must be 12-72 characters")
	ErrInvalidRole        = errors.New("role must be superadmin|admin")
	ErrUsernameTaken      = errors.New("username already exists")
)

var usernameRe = regexp.MustCompile(`^[a-zA-Z0-9_.-]{3,80}$`)

type Service struct {
	q   *db.Queries
	log *slog.Logger
}

func New(q *db.Queries, log *slog.Logger) *Service {
	return &Service{q: q, log: log}
}

// Authenticate verifies username+password and returns the user row on
// success. Bad username, bad password, and inactive users all collapse to
// ErrInvalidCredentials — no account-enumeration signal.
func (s *Service) Authenticate(ctx context.Context, username, password string) (db.SystemUser, error) {
	u, err := s.q.GetSystemUserByUsername(ctx, username)
	if err != nil {
		return db.SystemUser{}, ErrInvalidCredentials
	}
	if !u.IsActive || bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) != nil {
		return db.SystemUser{}, ErrInvalidCredentials
	}
	// Best-effort audit touch; failure must not break the login.
	_ = s.q.TouchSystemUserLogin(ctx, u.ID)
	return u, nil
}

// Create validates and inserts a system user. Passwords hash with bcrypt
// (72-byte input cap enforced by ErrInvalidPassword).
func (s *Service) Create(ctx context.Context, username, password, role string) (db.SystemUser, error) {
	if err := validate(username, password, role); err != nil {
		return db.SystemUser{}, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return db.SystemUser{}, err
	}
	u, err := s.q.CreateSystemUser(ctx, db.CreateSystemUserParams{
		Username: username, PasswordHash: string(hash), Role: role})
	if err != nil {
		return db.SystemUser{}, ErrUsernameTaken
	}
	return u, nil
}

// EnsureSuperAdmin seeds the bootstrap superadmin exactly once (first boot
// or after all superadmins are removed). The random password is logged —
// never stored — so it is shown precisely one time.
func (s *Service) EnsureSuperAdmin(ctx context.Context, genPassword func() string) {
	n, err := s.q.CountSuperAdmins(ctx)
	if err != nil || n > 0 {
		return
	}
	pw := genPassword()
	u, err := s.Create(ctx, "superadmin", pw, "superadmin")
	if err != nil {
		s.log.Error("bootstrap superadmin failed", "error", err)
		return
	}
	s.log.Warn("BOOTSTRAP superadmin created — store this password, it is shown once",
		"username", u.Username, "password", pw)
}

func (s *Service) List(ctx context.Context) ([]db.ListSystemUsersRow, error) {
	return s.q.ListSystemUsers(ctx)
}

func validate(username, password, role string) error {
	if !usernameRe.MatchString(username) {
		return ErrInvalidUsername
	}
	if l := len(password); l < 12 || l > 72 {
		return ErrInvalidPassword
	}
	if role != "superadmin" && role != "admin" {
		return ErrInvalidRole
	}
	return nil
}

// IsUniqueViolation reports whether err is a username-conflict sentinel —
// kept for callers distinguishing validation from storage races.
func IsUniqueViolation(err error) bool {
	return err != nil && errors.Is(err, ErrUsernameTaken)
}
