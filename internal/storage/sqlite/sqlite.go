// Package sqlite is the SQLite implementation of internal/storage.Repository,
// using the pure-Go modernc.org/sqlite driver (design.md §6, §8).
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"

	"github.com/syscod3/bambu-profile-manager/internal/domain"
	"github.com/syscod3/bambu-profile-manager/internal/storage"
)

const schema = `
CREATE TABLE IF NOT EXISTS profiles (
	id   TEXT PRIMARY KEY,
	name TEXT UNIQUE NOT NULL
);

CREATE TABLE IF NOT EXISTS profile_versions (
	id            TEXT PRIMARY KEY,
	profile_id    TEXT NOT NULL REFERENCES profiles(id),
	revision      INTEGER NOT NULL,
	source_json   BLOB NOT NULL,
	resolved_json BLOB NOT NULL,
	semantic_hash TEXT NOT NULL,
	created_at    INTEGER NOT NULL,
	UNIQUE(profile_id, revision)
);

CREATE TABLE IF NOT EXISTS domain_events (
	sequence   INTEGER PRIMARY KEY AUTOINCREMENT,
	id         TEXT UNIQUE NOT NULL,
	type       TEXT NOT NULL,
	entity_id  TEXT NOT NULL,
	revision   INTEGER NOT NULL,
	payload    BLOB NOT NULL,
	created_at INTEGER NOT NULL
);
`

// pragmas per design.md §8.
const pragmas = `
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;
PRAGMA busy_timeout = 5000;
PRAGMA synchronous = NORMAL;
`

// executor is the subset of *sql.DB / *sql.Tx this package uses, letting the
// repository implementations run unmodified against either.
type executor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Repository is the top-level SQLite-backed storage.Repository.
type Repository struct {
	db *sql.DB
}

// Open opens (creating if needed) a SQLite database at path and applies the
// schema. Use ":memory:" for an in-process database (used by tests).
func Open(path string) (*Repository, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open: %w", err)
	}
	if _, err := db.Exec(pragmas); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite: pragmas: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite: schema: %w", err)
	}
	return &Repository{db: db}, nil
}

func (r *Repository) Close() error { return r.db.Close() }

func (r *Repository) Profiles() storage.ProfileRepository { return profileRepo{r.db} }
func (r *Repository) Versions() storage.VersionRepository { return versionRepo{r.db} }
func (r *Repository) Events() storage.EventRepository     { return eventRepo{r.db} }

func (r *Repository) WithinTransaction(ctx context.Context, fn func(ctx context.Context, tx storage.Repository) error) error {
	sqlTx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin tx: %w", err)
	}
	tx := &txRepository{tx: sqlTx}
	if err := fn(ctx, tx); err != nil {
		if rbErr := sqlTx.Rollback(); rbErr != nil {
			return fmt.Errorf("sqlite: tx failed (%w) and rollback failed: %v", err, rbErr)
		}
		return err
	}
	if err := sqlTx.Commit(); err != nil {
		return fmt.Errorf("sqlite: commit: %w", err)
	}
	return nil
}

// txRepository is a Repository scoped to a single in-flight transaction.
type txRepository struct {
	tx *sql.Tx
}

func (t *txRepository) Close() error { return nil } // lifecycle owned by the outer Repository
func (t *txRepository) Profiles() storage.ProfileRepository { return profileRepo{t.tx} }
func (t *txRepository) Versions() storage.VersionRepository { return versionRepo{t.tx} }
func (t *txRepository) Events() storage.EventRepository     { return eventRepo{t.tx} }
func (t *txRepository) WithinTransaction(ctx context.Context, fn func(ctx context.Context, tx storage.Repository) error) error {
	return fn(ctx, t) // already inside a transaction; nesting just reuses it
}

// --- profiles ---

type profileRepo struct{ ex executor }

func (r profileRepo) Create(ctx context.Context, name string) (domain.Profile, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return domain.Profile{}, fmt.Errorf("sqlite: new id: %w", err)
	}
	p := domain.Profile{ID: id.String(), Name: name}
	_, err = r.ex.ExecContext(ctx, `INSERT INTO profiles (id, name) VALUES (?, ?)`, p.ID, p.Name)
	if err != nil {
		return domain.Profile{}, fmt.Errorf("sqlite: create profile: %w", err)
	}
	return p, nil
}

func (r profileRepo) Get(ctx context.Context, id string) (domain.Profile, error) {
	return r.scanOne(ctx, `SELECT id, name FROM profiles WHERE id = ?`, id)
}

func (r profileRepo) GetByName(ctx context.Context, name string) (domain.Profile, error) {
	return r.scanOne(ctx, `SELECT id, name FROM profiles WHERE name = ?`, name)
}

func (r profileRepo) scanOne(ctx context.Context, query string, arg any) (domain.Profile, error) {
	var p domain.Profile
	err := r.ex.QueryRowContext(ctx, query, arg).Scan(&p.ID, &p.Name)
	if err == sql.ErrNoRows {
		return domain.Profile{}, storage.ErrNotFound
	}
	if err != nil {
		return domain.Profile{}, fmt.Errorf("sqlite: get profile: %w", err)
	}
	return p, nil
}

func (r profileRepo) List(ctx context.Context) ([]domain.Profile, error) {
	rows, err := r.ex.QueryContext(ctx, `SELECT id, name FROM profiles ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list profiles: %w", err)
	}
	defer rows.Close()
	var out []domain.Profile
	for rows.Next() {
		var p domain.Profile
		if err := rows.Scan(&p.ID, &p.Name); err != nil {
			return nil, fmt.Errorf("sqlite: scan profile: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// --- versions ---

type versionRepo struct{ ex executor }

func (r versionRepo) Create(ctx context.Context, v domain.ProfileVersion) (domain.ProfileVersion, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return domain.ProfileVersion{}, fmt.Errorf("sqlite: new id: %w", err)
	}
	v.ID = id.String()
	if v.CreatedAt.IsZero() {
		v.CreatedAt = time.Now().UTC()
	}
	_, err = r.ex.ExecContext(ctx,
		`INSERT INTO profile_versions (id, profile_id, revision, source_json, resolved_json, semantic_hash, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		v.ID, v.ProfileID, v.Revision, v.SourceJSON, v.ResolvedJSON, v.SemanticHash, v.CreatedAt.Unix())
	if err != nil {
		return domain.ProfileVersion{}, fmt.Errorf("sqlite: create version: %w", err)
	}
	return v, nil
}

func (r versionRepo) Latest(ctx context.Context, profileID string) (domain.ProfileVersion, error) {
	row := r.ex.QueryRowContext(ctx,
		`SELECT id, profile_id, revision, source_json, resolved_json, semantic_hash, created_at
		 FROM profile_versions WHERE profile_id = ? ORDER BY revision DESC LIMIT 1`, profileID)
	v, err := scanVersion(row)
	if err == sql.ErrNoRows {
		return domain.ProfileVersion{}, storage.ErrNotFound
	}
	return v, err
}

func (r versionRepo) List(ctx context.Context, profileID string) ([]domain.ProfileVersion, error) {
	rows, err := r.ex.QueryContext(ctx,
		`SELECT id, profile_id, revision, source_json, resolved_json, semantic_hash, created_at
		 FROM profile_versions WHERE profile_id = ? ORDER BY revision ASC`, profileID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list versions: %w", err)
	}
	defer rows.Close()
	var out []domain.ProfileVersion
	for rows.Next() {
		v, err := scanVersionRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanVersion(row rowScanner) (domain.ProfileVersion, error) {
	var v domain.ProfileVersion
	var createdAt int64
	err := row.Scan(&v.ID, &v.ProfileID, &v.Revision, &v.SourceJSON, &v.ResolvedJSON, &v.SemanticHash, &createdAt)
	if err != nil {
		return domain.ProfileVersion{}, err
	}
	v.CreatedAt = time.Unix(createdAt, 0).UTC()
	return v, nil
}

func scanVersionRow(rows *sql.Rows) (domain.ProfileVersion, error) {
	v, err := scanVersion(rows)
	if err != nil {
		return domain.ProfileVersion{}, fmt.Errorf("sqlite: scan version: %w", err)
	}
	return v, nil
}

// --- events ---

type eventRepo struct{ ex executor }

func (r eventRepo) Append(ctx context.Context, e domain.DomainEvent) (domain.DomainEvent, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return domain.DomainEvent{}, fmt.Errorf("sqlite: new id: %w", err)
	}
	e.ID = id.String()
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	res, err := r.ex.ExecContext(ctx,
		`INSERT INTO domain_events (id, type, entity_id, revision, payload, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		e.ID, e.Type, e.EntityID, e.Revision, e.Payload, e.CreatedAt.Unix())
	if err != nil {
		return domain.DomainEvent{}, fmt.Errorf("sqlite: append event: %w", err)
	}
	seq, err := res.LastInsertId()
	if err != nil {
		return domain.DomainEvent{}, fmt.Errorf("sqlite: event sequence: %w", err)
	}
	e.Sequence = seq
	return e, nil
}

func (r eventRepo) ListSince(ctx context.Context, sequence int64) ([]domain.DomainEvent, error) {
	rows, err := r.ex.QueryContext(ctx,
		`SELECT sequence, id, type, entity_id, revision, payload, created_at
		 FROM domain_events WHERE sequence > ? ORDER BY sequence ASC`, sequence)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list events: %w", err)
	}
	defer rows.Close()
	var out []domain.DomainEvent
	for rows.Next() {
		var e domain.DomainEvent
		var createdAt int64
		if err := rows.Scan(&e.Sequence, &e.ID, &e.Type, &e.EntityID, &e.Revision, &e.Payload, &createdAt); err != nil {
			return nil, fmt.Errorf("sqlite: scan event: %w", err)
		}
		e.CreatedAt = time.Unix(createdAt, 0).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}
