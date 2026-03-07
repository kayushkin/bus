package main

import (
	"database/sql"
	"fmt"

	_ "github.com/mattn/go-sqlite3"
)

// AgentEntry maps an agent name + orchestrator pair.
type AgentEntry struct {
	Name         string `json:"name"`
	Orchestrator string `json:"orchestrator"`
	Description  string `json:"description,omitempty"`
	Enabled      bool   `json:"enabled"`
}

// AgentRegistry is a SQLite-backed agent → orchestrator mapping.
// Primary key is (name, orchestrator) — same agent can exist on multiple backends.
type AgentRegistry struct {
	db *sql.DB
}

// OpenRegistry opens (or creates) the agent registry database.
func OpenRegistry(path string) (*AgentRegistry, error) {
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL")
	if err != nil {
		return nil, fmt.Errorf("open registry: %w", err)
	}

	// Migrate: if old single-PK table exists, recreate with composite key.
	var pkCount int
	err = db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('agents') WHERE pk > 0`).Scan(&pkCount)
	if err == nil && pkCount == 1 {
		// Old schema with single PK on name — migrate.
		_, _ = db.Exec(`ALTER TABLE agents RENAME TO agents_old`)
		_, err = db.Exec(`CREATE TABLE agents (
			name TEXT NOT NULL,
			orchestrator TEXT NOT NULL,
			description TEXT DEFAULT '',
			enabled INTEGER DEFAULT 1,
			created_at TEXT DEFAULT (datetime('now')),
			PRIMARY KEY (name, orchestrator)
		)`)
		if err == nil {
			_, _ = db.Exec(`INSERT OR IGNORE INTO agents (name, orchestrator, description, enabled, created_at)
				SELECT name, orchestrator, description, enabled, created_at FROM agents_old`)
			_, _ = db.Exec(`DROP TABLE agents_old`)
		}
	} else {
		// Fresh or already migrated — ensure table exists.
		_, err = db.Exec(`CREATE TABLE IF NOT EXISTS agents (
			name TEXT NOT NULL,
			orchestrator TEXT NOT NULL,
			description TEXT DEFAULT '',
			enabled INTEGER DEFAULT 1,
			created_at TEXT DEFAULT (datetime('now')),
			PRIMARY KEY (name, orchestrator)
		)`)
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("create agents table: %w", err)
		}
	}

	return &AgentRegistry{db: db}, nil
}

func (r *AgentRegistry) Close() error {
	return r.db.Close()
}

// List returns all registered agents.
func (r *AgentRegistry) List() ([]AgentEntry, error) {
	rows, err := r.db.Query(
		"SELECT name, orchestrator, COALESCE(description,''), enabled FROM agents ORDER BY name, orchestrator")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var agents []AgentEntry
	for rows.Next() {
		var a AgentEntry
		if err := rows.Scan(&a.Name, &a.Orchestrator, &a.Description, &a.Enabled); err != nil {
			continue
		}
		agents = append(agents, a)
	}
	return agents, nil
}

// Get returns a single agent entry by (name, orchestrator).
func (r *AgentRegistry) Get(name, orchestrator string) (*AgentEntry, error) {
	var a AgentEntry
	err := r.db.QueryRow(
		"SELECT name, orchestrator, COALESCE(description,''), enabled FROM agents WHERE name = ? AND orchestrator = ?",
		name, orchestrator).
		Scan(&a.Name, &a.Orchestrator, &a.Description, &a.Enabled)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// Set adds or updates an agent entry (upsert by name+orchestrator).
func (r *AgentRegistry) Set(a AgentEntry) error {
	enabled := 0
	if a.Enabled {
		enabled = 1
	}
	_, err := r.db.Exec(`INSERT INTO agents (name, orchestrator, description, enabled)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(name, orchestrator) DO UPDATE SET
			description=excluded.description,
			enabled=excluded.enabled`,
		a.Name, a.Orchestrator, a.Description, enabled)
	return err
}

// Delete removes an agent entry by (name, orchestrator).
func (r *AgentRegistry) Delete(name, orchestrator string) error {
	res, err := r.db.Exec("DELETE FROM agents WHERE name = ? AND orchestrator = ?", name, orchestrator)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("agent %q/%s not found", name, orchestrator)
	}
	return nil
}

// Resolve returns the orchestrator for an enabled agent.
// If the agent exists on multiple backends, returns the first enabled one.
func (r *AgentRegistry) Resolve(name string) (string, bool) {
	var orchestrator string
	err := r.db.QueryRow(
		"SELECT orchestrator FROM agents WHERE name = ? AND enabled = 1 ORDER BY orchestrator LIMIT 1", name).
		Scan(&orchestrator)
	if err != nil {
		return "", false
	}
	return orchestrator, true
}

// ResolveExact checks if a specific (name, orchestrator) pair is enabled.
func (r *AgentRegistry) ResolveExact(name, orchestrator string) bool {
	var enabled int
	err := r.db.QueryRow(
		"SELECT enabled FROM agents WHERE name = ? AND orchestrator = ?", name, orchestrator).
		Scan(&enabled)
	return err == nil && enabled == 1
}
