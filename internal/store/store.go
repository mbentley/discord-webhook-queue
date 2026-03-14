package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Message represents a queued Discord webhook payload.
type Message struct {
	ID           int64
	ReceivedAt   time.Time
	WebhookID    string
	WebhookToken string
	ContentType  string
	Payload      []byte
	RetryCount   int
	LastError    string
	LastAttempt  *time.Time
}

// Store is the durable queue backed by SQLite.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) the SQLite database at path and initializes the schema.
func Open(path string) (*Store, error) {
	dir := filepath.Dir(path)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, fmt.Errorf("data directory %q does not exist (running as uid:gid %d:%d): create it and set ownership accordingly", dir, os.Getuid(), os.Getgid())
	}
	tmp, err := os.CreateTemp(dir, ".write-test-*")
	if err != nil {
		return nil, fmt.Errorf("data directory %q is not writable (running as uid:gid %d:%d): check ownership and permissions", dir, os.Getuid(), os.Getgid())
	}
	tmp.Close()
	os.Remove(tmp.Name())

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	// Single connection: serializes all reads and writes, avoids SQLITE_BUSY.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	s := &Store{db: db}
	if err := s.init(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) init() error {
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA foreign_keys=ON",
	}
	for _, p := range pragmas {
		if _, err := s.db.Exec(p); err != nil {
			return fmt.Errorf("pragma %q: %w", p, err)
		}
	}

	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS messages (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			received_at   DATETIME NOT NULL DEFAULT (datetime('now')),
			webhook_id    TEXT NOT NULL,
			webhook_token TEXT NOT NULL,
			content_type  TEXT NOT NULL,
			payload       BLOB NOT NULL,
			status        TEXT NOT NULL DEFAULT 'pending',
			retry_count   INTEGER NOT NULL DEFAULT 0,
			last_error    TEXT,
			last_attempt  DATETIME
		);
		CREATE INDEX IF NOT EXISTS idx_messages_status_received
			ON messages (status, received_at);
	`)
	if err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	return nil
}

// ResetInFlight resets any messages stuck in_flight (from a previous crash) back to pending.
// Returns the number of messages reset.
func (s *Store) ResetInFlight() (int64, error) {
	res, err := s.db.Exec(`UPDATE messages SET status = 'pending' WHERE status = 'in_flight'`)
	if err != nil {
		return 0, fmt.Errorf("reset in_flight: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// Enqueue adds a new message to the queue and returns its ID.
func (s *Store) Enqueue(webhookID, webhookToken, contentType string, payload []byte) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO messages (webhook_id, webhook_token, content_type, payload) VALUES (?, ?, ?, ?)`,
		webhookID, webhookToken, contentType, payload,
	)
	if err != nil {
		return 0, fmt.Errorf("enqueue: %w", err)
	}
	id, _ := res.LastInsertId()
	return id, nil
}

// NextPending returns the oldest pending message, or nil if the queue is empty.
func (s *Store) NextPending() (*Message, error) {
	row := s.db.QueryRow(`
		SELECT id, received_at, webhook_id, webhook_token, content_type, payload,
		       retry_count, COALESCE(last_error, ''), last_attempt
		FROM messages
		WHERE status = 'pending'
		ORDER BY received_at ASC
		LIMIT 1
	`)

	var m Message
	var lastAttempt sql.NullTime
	err := row.Scan(
		&m.ID, &m.ReceivedAt, &m.WebhookID, &m.WebhookToken,
		&m.ContentType, &m.Payload,
		&m.RetryCount, &m.LastError, &lastAttempt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("next pending: %w", err)
	}
	if lastAttempt.Valid {
		m.LastAttempt = &lastAttempt.Time
	}
	return &m, nil
}

// MarkInFlight marks a message as currently being sent.
func (s *Store) MarkInFlight(id int64) error {
	_, err := s.db.Exec(
		`UPDATE messages SET status = 'in_flight', last_attempt = datetime('now') WHERE id = ?`,
		id,
	)
	return err
}

// MarkFailed returns a message to pending after a failed delivery attempt,
// incrementing its retry count and recording the error.
func (s *Store) MarkFailed(id int64, errMsg string) error {
	_, err := s.db.Exec(
		`UPDATE messages SET status = 'pending', retry_count = retry_count + 1, last_error = ? WHERE id = ?`,
		errMsg, id,
	)
	return err
}

// MarkSent deletes a successfully delivered message from the queue.
func (s *Store) MarkSent(id int64) error {
	_, err := s.db.Exec(`DELETE FROM messages WHERE id = ?`, id)
	return err
}

// QueueDepth returns the total number of messages not yet delivered.
func (s *Store) QueueDepth() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&n)
	return n, err
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}
