package storage

import "database/sql"
import "errors"

// SettingPinnedPeer is the host:port of the peer the user pinned in the
// settings, used when no peer is given on the command line.
const SettingPinnedPeer = "pinnedPeer"

// Setting returns the value stored for the key, or "" when it is not set.
func (s *Store) Setting(key string) (string, error) {
	var value string
	var err = s.db.QueryRow(`select value from settings where key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) { return "", nil }
	return value, err
}

// SetSetting stores the value for the key, replacing the old one. An empty
// value removes the setting.
func (s *Store) SetSetting(key, value string) error {
	if value == "" {
		var _, err = s.db.Exec(`delete from settings where key = ?`, key)
		return err
	}
	var _, err = s.db.Exec(`insert or replace into settings (key, value) values (?, ?)`, key, value)
	return err
}
