package main

import (
	"database/sql"
	_ "github.com/mattn/go-sqlite3"
	"time"
)

type FailureStore struct {
	db *sql.DB
}

func NewFailureStore(path string) (*FailureStore, error) {
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, err
	}
	if _, err = db.Exec(`CREATE TABLE IF NOT EXISTS failures (model TEXT PRIMARY KEY, failed_at INTEGER)`); err != nil {
		db.Close()
		return nil, err
	}
	return &FailureStore{db: db}, nil
}

func (s *FailureStore) Close() error { return s.db.Close() }

func (s *FailureStore) MarkFailure(model string) error {
	_, err := s.db.Exec(`INSERT INTO failures(model, failed_at) VALUES(?, ?) ON CONFLICT(model) DO UPDATE SET failed_at=excluded.failed_at`, model, time.Now().Unix())
	return err
}

func (s *FailureStore) ShouldSkip(model string) (bool, error) {
	var ts int64
	err := s.db.QueryRow(`SELECT failed_at FROM failures WHERE model=?`, model).Scan(&ts)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// Reduced cooldown from 15 minutes to 5 minutes for faster recovery
	if time.Since(time.Unix(ts, 0)) < 5*time.Minute {
		return true, nil
	}
	return false, nil
}

// ClearFailure removes a model from the failure store (for successful requests)
func (s *FailureStore) ClearFailure(model string) error {
	_, err := s.db.Exec(`DELETE FROM failures WHERE model=?`, model)
	return err
}

// ResetAllFailures clears all failure records (useful for testing or manual reset)
func (s *FailureStore) ResetAllFailures() error {
	_, err := s.db.Exec(`DELETE FROM failures`)
	return err
}
