package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3"
)

// BackendEntry stores backend config in the DB.
type BackendEntry struct {
	Name     string        `json:"name"`
	Type     string        `json:"type"`
	Config   BackendConfig `json:"config"`
	Priority int           `json:"priority"`
	Enabled  bool          `json:"enabled"`
}

// AgentEntry maps an agent to its orchestrator. Identity = (Name, Orchestrator).
type AgentEntry struct {
	Name         string `json:"name"`
	Orchestrator string `json:"orchestrator"`
	Description  string `json:"description,omitempty"`
	Enabled      bool   `json:"enabled"`
}

// RouteEntry maps a channel to an agent+orchestrator pair.
type RouteEntry struct {
	Channel      string `json:"channel"`
	AgentName    string `json:"agent_name"`
	Orchestrator string `json:"orchestrator"`
}

// AgentTarget is the resolved routing target — always two clean fields.
type AgentTarget struct {
	Name         string
	Orchestrator string
}

// QueueKey is a typed map key for per-agent queues. Never serialized.
type QueueKey struct {
	Name         string
	Orchestrator string
}

// AgentRegistry is a SQLite-backed registry for backends, agents, and routes.
type AgentRegistry struct {
	db *sql.DB
}

// OpenRegistry opens (or creates) the agent registry database.
func OpenRegistry(path string) (*AgentRegistry, error) {
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL")
	if err != nil {
		return nil, fmt.Errorf("open registry: %w", err)
	}

	r := &AgentRegistry{db: db}
	if err := r.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return r, nil
}

func (r *AgentRegistry) migrate() error {
	// Create backends table (unchanged).
	_, err := r.db.Exec(`
		CREATE TABLE IF NOT EXISTS backends (
			name TEXT PRIMARY KEY,
			type TEXT NOT NULL,
			config TEXT NOT NULL,
			priority INTEGER DEFAULT 0,
			enabled INTEGER DEFAULT 1,
			created_at TEXT DEFAULT (datetime('now'))
		);
	`)
	if err != nil {
		return err
	}

	// Check if we need to migrate from old schema (agents with "id" TEXT PK).
	var hasIDCol int
	r.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('agents') WHERE name = 'id'`).Scan(&hasIDCol)

	if hasIDCol > 0 {
		// Old schema — migrate.
		log.Println("[registry] migrating agents table from id-based to composite key...")
		if err := r.migrateAgentsTable(); err != nil {
			return fmt.Errorf("migrate agents: %w", err)
		}
	} else {
		// Create new schema (idempotent).
		_, err = r.db.Exec(`
			CREATE TABLE IF NOT EXISTS agents (
				name TEXT NOT NULL,
				orchestrator TEXT NOT NULL,
				description TEXT DEFAULT '',
				enabled INTEGER DEFAULT 1,
				created_at TEXT DEFAULT (datetime('now')),
				PRIMARY KEY (name, orchestrator),
				FOREIGN KEY (orchestrator) REFERENCES backends(name)
			);

			CREATE TABLE IF NOT EXISTS routes (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				channel TEXT NOT NULL,
				agent_name TEXT NOT NULL,
				orchestrator TEXT NOT NULL,
				UNIQUE(channel, agent_name, orchestrator),
				FOREIGN KEY (agent_name, orchestrator) REFERENCES agents(name, orchestrator)
			);

			CREATE INDEX IF NOT EXISTS idx_agents_orchestrator ON agents(orchestrator);
			CREATE INDEX IF NOT EXISTS idx_routes_channel ON routes(channel);
		`)
		if err != nil {
			return err
		}
	}
	return nil
}

// migrateAgentsTable migrates from old id-based schema to composite (name, orchestrator).
func (r *AgentRegistry) migrateAgentsTable() error {
	// Read old agents (old schema has: id, name, orchestrator, description, enabled).
	rows, err := r.db.Query(`SELECT id, name, orchestrator, COALESCE(description,''), enabled FROM agents`)
	if err != nil {
		return err
	}
	type oldAgent struct {
		id, name, orch, desc string
		enabled              int
	}
	var agents []oldAgent
	for rows.Next() {
		var a oldAgent
		rows.Scan(&a.id, &a.name, &a.orch, &a.desc, &a.enabled)
		agents = append(agents, a)
	}
	rows.Close()

	// Read old routes.
	type oldRoute struct {
		channel, agentID string
	}
	var routes []oldRoute
	rrows, err := r.db.Query(`SELECT channel, agent_id FROM routes`)
	if err == nil {
		for rrows.Next() {
			var ro oldRoute
			rrows.Scan(&ro.channel, &ro.agentID)
			routes = append(routes, ro)
		}
		rrows.Close()
	}

	// Build agent lookup: old compound ID → agent data
	agentLookup := make(map[string]oldAgent)
	for _, a := range agents {
		agentLookup[a.id] = a
	}

	// Drop old tables.
	r.db.Exec(`DROP TABLE IF EXISTS routes`)
	r.db.Exec(`DROP TABLE IF EXISTS agents`)

	// Create new tables.
	_, err = r.db.Exec(`
		CREATE TABLE agents (
			name TEXT NOT NULL,
			orchestrator TEXT NOT NULL,
			description TEXT DEFAULT '',
			enabled INTEGER DEFAULT 1,
			created_at TEXT DEFAULT (datetime('now')),
			PRIMARY KEY (name, orchestrator),
			FOREIGN KEY (orchestrator) REFERENCES backends(name)
		);

		CREATE TABLE routes (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			channel TEXT NOT NULL,
			agent_name TEXT NOT NULL,
			orchestrator TEXT NOT NULL,
			UNIQUE(channel, agent_name, orchestrator),
			FOREIGN KEY (agent_name, orchestrator) REFERENCES agents(name, orchestrator)
		);

		CREATE INDEX IF NOT EXISTS idx_agents_orchestrator ON agents(orchestrator);
		CREATE INDEX IF NOT EXISTS idx_routes_channel ON routes(channel);
	`)
	if err != nil {
		return err
	}

	// Insert agents (dedup by short name + orchestrator).
	seen := make(map[string]bool)
	for _, a := range agents {
		// Extract short name from old compound ID (e.g. "claxon@inber" → "claxon").
		shortName := a.id
		if idx := len(a.id) - len(a.orch) - 1; idx > 0 && a.id[idx] == '@' {
			shortName = a.id[:idx]
		}
		key := shortName + "|" + a.orch
		if seen[key] {
			continue
		}
		seen[key] = true
		r.db.Exec(`INSERT OR IGNORE INTO agents (name, orchestrator, description, enabled) VALUES (?, ?, ?, ?)`,
			shortName, a.orch, a.desc, a.enabled)
	}

	// Insert routes, resolving old agent_id to (short name, orchestrator).
	for _, ro := range routes {
		if a, ok := agentLookup[ro.agentID]; ok {
			shortName := a.id
			if idx := len(a.id) - len(a.orch) - 1; idx > 0 && a.id[idx] == '@' {
				shortName = a.id[:idx]
			}
			r.db.Exec(`INSERT OR IGNORE INTO routes (channel, agent_name, orchestrator) VALUES (?, ?, ?)`,
				ro.channel, shortName, a.orch)
		}
	}

	log.Printf("[registry] migrated %d agents, %d routes", len(agents), len(routes))
	return nil
}

func (r *AgentRegistry) Close() error {
	return r.db.Close()
}

// --- Backends ---

func (r *AgentRegistry) ListBackends() ([]BackendEntry, error) {
	rows, err := r.db.Query(
		"SELECT name, type, config, priority, enabled FROM backends WHERE enabled = 1 ORDER BY priority")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var backends []BackendEntry
	for rows.Next() {
		var b BackendEntry
		var configJSON string
		var enabled int
		if err := rows.Scan(&b.Name, &b.Type, &configJSON, &b.Priority, &enabled); err != nil {
			continue
		}
		b.Enabled = enabled == 1
		if err := json.Unmarshal([]byte(configJSON), &b.Config); err != nil {
			continue
		}
		b.Config.Type = b.Type
		backends = append(backends, b)
	}
	return backends, nil
}

func (r *AgentRegistry) GetBackend(name string) (*BackendEntry, error) {
	var b BackendEntry
	var configJSON string
	var enabled int
	err := r.db.QueryRow(
		"SELECT name, type, config, priority, enabled FROM backends WHERE name = ?", name).
		Scan(&b.Name, &b.Type, &configJSON, &b.Priority, &enabled)
	if err != nil {
		return nil, err
	}
	b.Enabled = enabled == 1
	if err := json.Unmarshal([]byte(configJSON), &b.Config); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	b.Config.Type = b.Type
	return &b, nil
}

func (r *AgentRegistry) SetBackend(b BackendEntry) error {
	configJSON, err := json.Marshal(b.Config)
	if err != nil {
		return err
	}
	enabled := 0
	if b.Enabled {
		enabled = 1
	}
	_, err = r.db.Exec(`INSERT INTO backends (name, type, config, priority, enabled)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			type=excluded.type,
			config=excluded.config,
			priority=excluded.priority,
			enabled=excluded.enabled`,
		b.Name, b.Type, string(configJSON), b.Priority, enabled)
	return err
}

// --- Agents ---

func (r *AgentRegistry) ListAgents() ([]AgentEntry, error) {
	rows, err := r.db.Query(
		"SELECT name, orchestrator, COALESCE(description,''), enabled FROM agents ORDER BY orchestrator, name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var agents []AgentEntry
	for rows.Next() {
		var a AgentEntry
		var enabled int
		if err := rows.Scan(&a.Name, &a.Orchestrator, &a.Description, &enabled); err != nil {
			continue
		}
		a.Enabled = enabled == 1
		agents = append(agents, a)
	}
	return agents, nil
}

func (r *AgentRegistry) GetAgent(name, orchestrator string) (*AgentEntry, error) {
	var a AgentEntry
	var enabled int
	err := r.db.QueryRow(
		"SELECT name, orchestrator, COALESCE(description,''), enabled FROM agents WHERE name = ? AND orchestrator = ?",
		name, orchestrator).
		Scan(&a.Name, &a.Orchestrator, &a.Description, &enabled)
	if err != nil {
		return nil, err
	}
	a.Enabled = enabled == 1
	return &a, nil
}

func (r *AgentRegistry) SetAgent(a AgentEntry) error {
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

func (r *AgentRegistry) DeleteAgent(name, orchestrator string) error {
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

// ResolveAgent finds an agent by name and orchestrator. Both must match.
func (r *AgentRegistry) ResolveAgent(name, orchestrator string) (*AgentEntry, bool) {
	a, err := r.GetAgent(name, orchestrator)
	if err != nil {
		return nil, false
	}
	if !a.Enabled {
		return nil, false
	}
	return a, true
}

// FindAgent finds an agent by name only. Returns the match and true only if exactly one
// enabled agent has that name. If ambiguous (multiple orchestrators), returns false.
func (r *AgentRegistry) FindAgent(name string) (*AgentEntry, bool) {
	rows, err := r.db.Query(
		"SELECT name, orchestrator, COALESCE(description,''), enabled FROM agents WHERE name = ? AND enabled = 1", name)
	if err != nil {
		return nil, false
	}
	defer rows.Close()

	var matches []AgentEntry
	for rows.Next() {
		var a AgentEntry
		var enabled int
		if err := rows.Scan(&a.Name, &a.Orchestrator, &a.Description, &enabled); err == nil {
			a.Enabled = enabled == 1
			matches = append(matches, a)
		}
	}
	if len(matches) == 1 {
		return &matches[0], true
	}
	return nil, false
}

// ResolveRoute returns the agent target for a channel.
func (r *AgentRegistry) ResolveRoute(channel string) (AgentTarget, bool) {
	var t AgentTarget
	err := r.db.QueryRow(
		"SELECT agent_name, orchestrator FROM routes WHERE channel = ? LIMIT 1", channel).
		Scan(&t.Name, &t.Orchestrator)
	if err != nil {
		return t, false
	}
	return t, true
}

// --- Routes ---

func (r *AgentRegistry) ListRoutes() ([]RouteEntry, error) {
	rows, err := r.db.Query("SELECT channel, agent_name, orchestrator FROM routes")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var routes []RouteEntry
	for rows.Next() {
		var ro RouteEntry
		if err := rows.Scan(&ro.Channel, &ro.AgentName, &ro.Orchestrator); err != nil {
			continue
		}
		routes = append(routes, ro)
	}
	return routes, nil
}

func (r *AgentRegistry) SetRoute(channel, agentName, orchestrator string) error {
	_, err := r.db.Exec(`INSERT OR IGNORE INTO routes (channel, agent_name, orchestrator) VALUES (?, ?, ?)`,
		channel, agentName, orchestrator)
	return err
}

func (r *AgentRegistry) DeleteRoute(channel, agentName, orchestrator string) error {
	_, err := r.db.Exec("DELETE FROM routes WHERE channel = ? AND agent_name = ? AND orchestrator = ?",
		channel, agentName, orchestrator)
	return err
}

// --- Sync ---

func (reg *AgentRegistry) SyncInberAgents(agentsJSONPath string) (int, int, int, error) {
	data, err := os.ReadFile(expandHome(agentsJSONPath))
	if err != nil {
		return 0, 0, 0, fmt.Errorf("read agents.json: %w", err)
	}

	var cfg struct {
		Agents map[string]struct {
			Name string `json:"name"`
			Role string `json:"role"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return 0, 0, 0, fmt.Errorf("parse agents.json: %w", err)
	}

	existing := make(map[string]bool)
	rows, err := reg.db.Query("SELECT name FROM agents WHERE orchestrator = 'inber'")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var name string
			if rows.Scan(&name) == nil {
				existing[name] = true
			}
		}
	}

	added, updated := 0, 0
	for name, a := range cfg.Agents {
		displayName := a.Name
		if displayName == "" {
			displayName = name
		}
		description := a.Role
		if displayName != name {
			description = displayName + " — " + description
		}

		entry := AgentEntry{
			Name:         name,
			Orchestrator: "inber",
			Description:  description,
			Enabled:      true,
		}

		if existing[name] {
			if err := reg.SetAgent(entry); err == nil {
				updated++
			}
			delete(existing, name)
		} else {
			if err := reg.SetAgent(entry); err == nil {
				added++
			}
		}
	}

	removed := 0
	for name := range existing {
		if err := reg.DeleteAgent(name, "inber"); err == nil {
			removed++
		}
	}

	return added, updated, removed, nil
}

func (reg *AgentRegistry) SyncOpenclawAgents(openclawDir string) (int, int, int, error) {
	openclawDir = expandHome(openclawDir)
	entries, err := os.ReadDir(openclawDir)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("read openclaw agents dir: %w", err)
	}

	existing := make(map[string]bool)
	rows, err := reg.db.Query("SELECT name FROM agents WHERE orchestrator = 'openclaw'")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var name string
			if rows.Scan(&name) == nil {
				existing[name] = true
			}
		}
	}

	added, updated := 0, 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()

		agentDir := filepath.Join(openclawDir, name, "agent")
		if stat, err := os.Stat(agentDir); err != nil || !stat.IsDir() {
			continue
		}

		agentEntry := AgentEntry{
			Name:         name,
			Orchestrator: "openclaw",
			Enabled:      true,
		}

		if existing[name] {
			if err := reg.SetAgent(agentEntry); err == nil {
				updated++
			}
			delete(existing, name)
		} else {
			if err := reg.SetAgent(agentEntry); err == nil {
				added++
			}
		}
	}

	removed := 0
	for name := range existing {
		if err := reg.DeleteAgent(name, "openclaw"); err == nil {
			removed++
		}
	}

	return added, updated, removed, nil
}

func (reg *AgentRegistry) SyncAllAgents() (added, updated, removed int, err error) {
	a, u, rm, syncErr := reg.SyncInberAgents("~/life/repos/inber/agents.json")
	if syncErr != nil {
		log.Printf("[registry] warning: sync inber agents: %v", syncErr)
	} else {
		added += a
		updated += u
		removed += rm
	}

	a, u, rm, syncErr = reg.SyncOpenclawAgents("~/.openclaw/agents")
	if syncErr != nil {
		log.Printf("[registry] warning: sync openclaw agents: %v", syncErr)
	} else {
		added += a
		updated += u
		removed += rm
	}

	return added, updated, removed, nil
}
