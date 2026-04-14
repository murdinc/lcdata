package lcdata

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Store persists run history to SQLite.
type Store struct {
	db *sql.DB
}

// OpenStore opens (or creates) the SQLite database at the given path.
// Use ":memory:" for a non-persistent in-process store.
func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite is single-writer
	if err := migrate(db); err != nil {
		return nil, fmt.Errorf("store migration: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS runs (
			id          TEXT PRIMARY KEY,
			node        TEXT NOT NULL,
			env         TEXT NOT NULL,
			status      TEXT NOT NULL,
			input       TEXT NOT NULL DEFAULT '{}',
			output      TEXT,
			steps       TEXT,
			error       TEXT,
			started_at  TEXT NOT NULL,
			ended_at    TEXT,
			duration_ms INTEGER DEFAULT 0,
			input_tokens  INTEGER DEFAULT 0,
			output_tokens INTEGER DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS runs_started_at ON runs(started_at DESC);
	`)
	return err
}

// SaveRun persists a completed run record.
func (s *Store) SaveRun(run *Run) error {
	inputJSON, _ := json.Marshal(run.Input)
	outputJSON, _ := json.Marshal(run.Output)
	stepsJSON, _ := json.Marshal(run.Steps)

	endedAt := ""
	if !run.EndedAt.IsZero() {
		endedAt = run.EndedAt.UTC().Format(time.RFC3339Nano)
	}

	_, err := s.db.Exec(`
		INSERT INTO runs (id, node, env, status, input, output, steps, error, started_at, ended_at, duration_ms, input_tokens, output_tokens)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			status      = excluded.status,
			output      = excluded.output,
			steps       = excluded.steps,
			error       = excluded.error,
			ended_at    = excluded.ended_at,
			duration_ms = excluded.duration_ms,
			input_tokens  = excluded.input_tokens,
			output_tokens = excluded.output_tokens
	`,
		run.ID,
		run.Node,
		run.Env,
		string(run.Status),
		string(inputJSON),
		string(outputJSON),
		string(stepsJSON),
		run.Error,
		run.StartedAt.UTC().Format(time.RFC3339Nano),
		endedAt,
		run.DurationMS,
		run.InputTokens,
		run.OutputTokens,
	)
	return err
}

// GetRun retrieves a run by ID. Returns sql.ErrNoRows if not found.
func (s *Store) GetRun(id string) (*Run, error) {
	row := s.db.QueryRow(`
		SELECT id, node, env, status, input, output, steps, error, started_at, ended_at, duration_ms, input_tokens, output_tokens
		FROM runs WHERE id = ?
	`, id)
	return scanRun(row)
}

// ListRuns returns the most recent runs, up to limit.
func (s *Store) ListRuns(limit int) ([]*Run, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(`
		SELECT id, node, env, status, input, output, steps, error, started_at, ended_at, duration_ms, input_tokens, output_tokens
		FROM runs ORDER BY started_at DESC LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []*Run
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

// scanner covers both *sql.Row and *sql.Rows
type scanner interface {
	Scan(dest ...any) error
}

func scanRun(s scanner) (*Run, error) {
	var (
		run              Run
		inputJSON        string
		outputJSON       sql.NullString
		stepsJSON        sql.NullString
		startedAtStr     string
		endedAtStr       sql.NullString
		statusStr        string
	)

	err := s.Scan(
		&run.ID,
		&run.Node,
		&run.Env,
		&statusStr,
		&inputJSON,
		&outputJSON,
		&stepsJSON,
		&run.Error,
		&startedAtStr,
		&endedAtStr,
		&run.DurationMS,
		&run.InputTokens,
		&run.OutputTokens,
	)
	if err != nil {
		return nil, err
	}

	run.Status = RunStatus(statusStr)

	if err := json.Unmarshal([]byte(inputJSON), &run.Input); err != nil {
		run.Input = map[string]any{}
	}
	if outputJSON.Valid {
		json.Unmarshal([]byte(outputJSON.String), &run.Output)
	}
	if stepsJSON.Valid {
		json.Unmarshal([]byte(stepsJSON.String), &run.Steps)
	}

	run.StartedAt, _ = time.Parse(time.RFC3339Nano, startedAtStr)
	if endedAtStr.Valid {
		run.EndedAt, _ = time.Parse(time.RFC3339Nano, endedAtStr.String)
	}

	return &run, nil
}
