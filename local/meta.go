package local

import (
	"database/sql"
	"errors"
	"fmt"
)

// PutMeta stores a vault-level setting.
func (s *Store) PutMeta(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO meta (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("store meta %q: %w", key, err)
	}
	return nil
}

// DeleteMeta removes a vault-level setting. Removing one that is not there is
// not an error, since what the caller wanted is already true.
func (s *Store) DeleteMeta(key string) error {
	if _, err := s.db.Exec(`DELETE FROM meta WHERE key = ?`, key); err != nil {
		return fmt.Errorf("remove meta %q: %w", key, err)
	}
	return nil
}

// GetMeta reads a vault-level setting.
func (s *Store) GetMeta(key string) (string, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("meta %q: %w", key, ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("read meta %q: %w", key, err)
	}
	return value, nil
}
