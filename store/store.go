// Package store keeps kitchensink's event log, latest snapshots and monitor
// state in SQLite.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/seventy-two/kitchensink/monitor"
	_ "modernc.org/sqlite"
)

const schema = `
-- No AUTOINCREMENT: it spends an ID on every insert skipped as a repeat.
-- Events are never deleted, so IDs still only go up.
CREATE TABLE IF NOT EXISTS events (
	id      INTEGER PRIMARY KEY,
	monitor TEXT NOT NULL,
	key     TEXT NOT NULL,
	time    INTEGER NOT NULL,
	data    TEXT NOT NULL,
	updated INTEGER NOT NULL DEFAULT 0, -- when last updated, Unix ms; 0 if never
	UNIQUE (monitor, key)
);
CREATE TABLE IF NOT EXISTS snapshots (
	monitor TEXT PRIMARY KEY,
	data    TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS state (
	monitor TEXT NOT NULL,
	key     TEXT NOT NULL,
	value   TEXT NOT NULL,
	PRIMARY KEY (monitor, key)
);`

// Store is a SQLite database. It's safe for concurrent use.
type Store struct {
	db *sql.DB
}

// Open opens or creates the database at path. Use ":memory:" in tests.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	// One connection keeps writes serialised and makes :memory: databases
	// shared across calls.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating schema: %w", err)
	}
	// Databases made before events could be updated lack the column.
	if _, err := db.Exec(`ALTER TABLE events ADD COLUMN updated INTEGER NOT NULL DEFAULT 0`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		db.Close()
		return nil, fmt.Errorf("adding updated column: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// AddEvent records e unless its monitor has already recorded an event with
// the same key. It returns the stored event, with its ID, and whether it was
// new.
func (s *Store) AddEvent(e monitor.Event) (monitor.Event, bool, error) {
	if e.Monitor == "" || e.Key == "" {
		return e, false, errors.New("event needs a monitor and key")
	}
	// The ID isn't known until the insert, so the stored JSON leaves it out
	// and reads fill it in from the column.
	e.ID = 0
	data, err := json.Marshal(e)
	if err != nil {
		return e, false, err
	}
	res, err := s.db.Exec(`INSERT INTO events (monitor, key, time, data) VALUES (?, ?, ?, ?)
		ON CONFLICT (monitor, key) DO NOTHING`, e.Monitor, e.Key, e.Time.UnixMilli(), data)
	if err != nil {
		return e, false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return e, false, nil
	}
	e.ID, err = res.LastInsertId()
	return e, true, err
}

// UpdateEvent applies an update (see monitor.Event.Update) to the event
// its monitor recorded with the same key, at the update's Updated time. It
// returns the event as it now is, and whether it changed: false if there's
// no such event or it already reads that way.
func (s *Store) UpdateEvent(u monitor.Event) (monitor.Event, bool, error) {
	var id int64
	var data []byte
	err := s.db.QueryRow(`SELECT id, data FROM events WHERE monitor = ? AND key = ?`, u.Monitor, u.Key).Scan(&id, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return u, false, nil
	}
	if err != nil {
		return u, false, err
	}
	var e monitor.Event
	if err := json.Unmarshal(data, &e); err != nil {
		return u, false, fmt.Errorf("event %d: %w", id, err)
	}
	until := e.Until
	if !u.Until.IsZero() {
		until = u.Until
	}
	if e.Title == u.Title && e.Body == u.Body && e.Quiet == u.Quiet && e.Until.Equal(until) {
		e.ID = id
		return e, false, nil
	}
	e.Title, e.Body, e.Quiet, e.Until, e.Updated = u.Title, u.Body, u.Quiet, until, u.Updated
	if data, err = json.Marshal(e); err != nil {
		return u, false, err
	}
	if _, err := s.db.Exec(`UPDATE events SET data = ?, updated = ? WHERE id = ?`, data, u.Updated.UnixMilli(), id); err != nil {
		return u, false, err
	}
	e.ID = id
	return e, true, nil
}

// UpdatedSince returns the events up to and including since that have
// been updated after event since was recorded, oldest first: what a client
// that saw up to since may have missed besides the events after it.
func (s *Store) UpdatedSince(since int64, limit int) ([]monitor.Event, error) {
	return s.events(`SELECT id, data FROM events WHERE id <= ?1 AND updated > 0 AND updated > COALESCE((SELECT time FROM events WHERE id = ?1), 0)
		ORDER BY id LIMIT ?2`, since, limit)
}

// EventsSince returns up to limit events with IDs above since, oldest first.
func (s *Store) EventsSince(since int64, limit int) ([]monitor.Event, error) {
	return s.events(`SELECT id, data FROM events WHERE id > ? ORDER BY id LIMIT ?`, since, limit)
}

// LatestEvents returns the newest limit events, oldest first.
func (s *Store) LatestEvents(limit int) ([]monitor.Event, error) {
	return s.events(`SELECT id, data FROM (SELECT id, data FROM events ORDER BY id DESC LIMIT ?) ORDER BY id`, limit)
}

func (s *Store) events(query string, args ...any) ([]monitor.Event, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []monitor.Event{}
	for rows.Next() {
		var id int64
		var data []byte
		if err := rows.Scan(&id, &data); err != nil {
			return nil, err
		}
		var e monitor.Event
		if err := json.Unmarshal(data, &e); err != nil {
			return nil, fmt.Errorf("event %d: %w", id, err)
		}
		e.ID = id
		events = append(events, e)
	}
	return events, rows.Err()
}

// SaveSnapshot stores a monitor's latest snapshot.
func (s *Store) SaveSnapshot(snap monitor.Snapshot) error {
	data, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO snapshots (monitor, data) VALUES (?, ?)
		ON CONFLICT (monitor) DO UPDATE SET data = excluded.data`, snap.Monitor, data)
	return err
}

// Snapshot loads a monitor's last saved snapshot.
func (s *Store) Snapshot(monitorID string) (monitor.Snapshot, bool, error) {
	var snap monitor.Snapshot
	var data []byte
	err := s.db.QueryRow(`SELECT data FROM snapshots WHERE monitor = ?`, monitorID).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return snap, false, nil
	}
	if err != nil {
		return snap, false, err
	}
	return snap, true, json.Unmarshal(data, &snap)
}

// State returns the key-value store for one monitor.
func (s *Store) State(monitorID string) monitor.State {
	return &state{db: s.db, monitor: monitorID}
}

type state struct {
	db      *sql.DB
	monitor string
}

func (st *state) Get(key string, v any) (bool, error) {
	var data []byte
	err := st.db.QueryRow(`SELECT value FROM state WHERE monitor = ? AND key = ?`, st.monitor, key).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(data, v)
}

func (st *state) Set(key string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = st.db.Exec(`INSERT INTO state (monitor, key, value) VALUES (?, ?, ?)
		ON CONFLICT (monitor, key) DO UPDATE SET value = excluded.value`, st.monitor, key, data)
	return err
}
