package main

import (
	"database/sql"
	"fmt"

	_ "github.com/mattn/go-sqlite3"
)

// AgentEntry maps an agent name to its orchestrator (backend).
type AgentEntry struct {
	Name         string `json:"name"`
	Orchestrator string `json:"orchestrator"`
	Description  string `json:"description,omitempty"`
	Enabled      bool   `json:"enabled"`
}

// AgentRegistry is a SQLite-backed agent → orchestrator mapping.
type AgentRegistry struct {
	db *sql.DB
}

// OpenRegistry opens (or creates) the agent registry database.
func OpenRegistry(path string) (*AgentRegistry, error) {
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL")
	if err != nil {
		return nil, fmt.Errorf("open registry: %w", err)
	}

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS agents (
		name TEXT PRIMARY KEY,
		orchestrator TEXT NOT NULL,
		description TEXT DEFAULT '',
		enabled INTEGER DEFAULT 1,
		created_at TEXT DEFAULT (datetime('now'))
	)`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("create agents table: %w", err)
	}

	return &AgentRegistry{db: db}, nil
}

func (r *AgentRegistry) Close() error {
	return r.db.Close()
}

// List returns all registered agents.
func (r *AgentRegistry) List() ([]AgentEntry, error) {
	rows, err := r.db.Query(
		"SELECT name, orchestrator, COALESCE(description,''), enabled FROM agents ORDER BY orchestrator, name")
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

// Get returns a single agent entry.
func (r *AgentRegistry) Get(name string) (*AgentEntry, error) {
	var a AgentEntry
	err := r.db.QueryRow(
		"SELECT name, orchestrator, COALESCE(description,''), enabled FROM agents WHERE name = ?", name).
		Scan(&a.Name, &a.Orchestrator, &a.Description, &a.Enabled)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// Set adds or updates an agent entry (upsert).
func (r *AgentRegistry) Set(a AgentEntry) error {
	enabled := 0
	if a.Enabled {
		enabled = 1
	}
	_, err := r.db.Exec(`INSERT INTO agents (name, orchestrator, description, enabled)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			orchestrator=excluded.orchestrator,
			description=excluded.description,
			enabled=excluded.enabled`,
		a.Name, a.Orchestrator, a.Description, enabled)
	return err
}

// Delete removes an agent entry.
func (r *AgentRegistry) Delete(name string) error {
	res, err := r.db.Exec("DELETE FROM agents WHERE name = ?", name)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("agent %q not found", name)
	}
	return nil
}

// Resolve returns the orchestrator for an enabled agent.
func (r *AgentRegistry) Resolve(name string) (string, bool) {
	var orchestrator string
	err := r.db.QueryRow(
		"SELECT orchestrator FROM agents WHERE name = ? AND enabled = 1", name).
		Scan(&orchestrator)
	if err != nil {
		return "", false
	}
	return orchestrator, true
}
