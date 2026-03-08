package main

import (
	"context"
	"log"
	"os/exec"
	"strings"
	"sync"

	"github.com/kayushkin/forge"
)

// ForgeManager handles slot acquisition and release for agent tasks.
// Bus-agent owns the forge lifecycle — agents just work in whatever directory they're given.
type ForgeManager struct {
	forge *forge.Forge
	mu    sync.Mutex
	// Track which slots are held so we can release on cleanup
	held map[string]*forge.Slot // sessionID → slot
}

// NewForgeManager opens the forge database.
func NewForgeManager() *ForgeManager {
	f, err := forge.Open("")
	if err != nil {
		log.Printf("[forge] failed to open: %v (slot management disabled)", err)
		return &ForgeManager{held: make(map[string]*forge.Slot)}
	}
	log.Printf("[forge] opened %s", forge.DefaultPath())
	return &ForgeManager{forge: f, held: make(map[string]*forge.Slot)}
}

// Acquire tries to get a slot for the given agent working on a project.
// Returns the slot path, or empty string if no slot available or no project match.
func (fm *ForgeManager) Acquire(agentName, sessionID, repoRoot string) (slotPath string, slotKey string) {
	if fm.forge == nil {
		return "", ""
	}

	proj := fm.forge.FindProjectByPath(repoRoot)
	if proj == nil {
		return "", ""
	}

	fm.mu.Lock()
	defer fm.mu.Unlock()

	slot, err := fm.forge.Acquire(proj.ID, forge.AcquireOpts{
		AgentID:      agentName,
		SessionID:    sessionID,
		Orchestrator: "bus-agent",
	})
	if err != nil {
		log.Printf("[forge] no slots for %q (project %s): %v", agentName, proj.ID, err)
		return "", ""
	}

	// Pull latest into slot
	fm.forge.SlotPull(proj.ID, slot.ID)

	key := proj.ID + ":" + sessionID
	fm.held[key] = slot
	log.Printf("[forge] %s acquired slot %d → %s", agentName, slot.ID, slot.Path)
	return slot.Path, key
}

// Release returns a slot to the pool and notifies any waiters.
func (fm *ForgeManager) Release(slotKey string) {
	if fm.forge == nil || slotKey == "" {
		return
	}

	fm.mu.Lock()
	slot, ok := fm.held[slotKey]
	if ok {
		delete(fm.held, slotKey)
	}
	fm.mu.Unlock()

	if !ok {
		return
	}

	if err := fm.forge.Release(slot.Project, slot.ID); err != nil {
		log.Printf("[forge] failed to release slot %d: %v", slot.ID, err)
		return
	}
	log.Printf("[forge] released slot %d for %s", slot.ID, slot.Project)

	// Notify waiters that a slot is available
	fm.notifyRelease()
}

// slotRelease is used to notify blocked goroutines that a slot was freed.
var slotRelease = make(chan struct{}, 10)

func (fm *ForgeManager) notifyRelease() {
	select {
	case slotRelease <- struct{}{}:
	default:
	}
}

// WaitForSlot blocks until a slot release notification or context cancellation.
func (fm *ForgeManager) WaitForSlot(ctx context.Context) bool {
	select {
	case <-slotRelease:
		return true
	case <-ctx.Done():
		return false
	}
}

// SlotInfo is the rich status of a single slot for the API.
type SlotInfo struct {
	ID           int    `json:"id"`
	Project      string `json:"project"`
	Status       string `json:"status"`
	Agent        string `json:"agent,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
	Branch       string `json:"branch,omitempty"`
	Commit       string `json:"commit,omitempty"`
	CommitMsg    string `json:"commit_message,omitempty"`
	Dirty        bool   `json:"dirty"`
	DirtyFiles   int    `json:"dirty_files,omitempty"`
	UncommittedChanges string `json:"uncommitted_changes,omitempty"`
	Path         string `json:"path"`
}

// ProjectStatus is the full forge status for the API.
type ProjectStatus struct {
	ID       string     `json:"id"`
	BaseRepo string     `json:"base_repo"`
	Slots    []SlotInfo `json:"slots"`
}

// Status returns rich status for all projects and slots.
func (fm *ForgeManager) Status() []ProjectStatus {
	if fm.forge == nil {
		return nil
	}

	projects, err := fm.forge.ListProjects()
	if err != nil {
		return nil
	}

	var result []ProjectStatus
	for _, p := range projects {
		ps := ProjectStatus{ID: p.ID, BaseRepo: p.BaseRepo}

		slots, err := fm.forge.SlotStatus(p.ID)
		if err != nil {
			continue
		}

		for _, s := range slots {
			si := SlotInfo{
				ID:      s.ID,
				Project: s.Project,
				Status:  s.Status,
				Agent:   s.AgentID,
				SessionID: s.SessionID,
				Branch:  s.Branch,
				Path:    s.Path,
			}

			// Git info from the worktree
			si.Commit, si.CommitMsg = gitHeadInfo(s.Path)
			dirty, dirtyFiles := gitDirtyStatus(s.Path)
			si.Dirty = dirty
			si.DirtyFiles = dirtyFiles
			if dirty {
				si.UncommittedChanges = gitPorcelain(s.Path)
			}

			ps.Slots = append(ps.Slots, si)
		}

		result = append(result, ps)
	}
	return result
}

// gitHeadInfo returns the HEAD commit hash and message for a repo path.
func gitHeadInfo(path string) (hash, msg string) {
	cmd := exec.Command("git", "-C", path, "log", "-1", "--format=%h %s")
	out, err := cmd.Output()
	if err != nil {
		return "", ""
	}
	line := strings.TrimSpace(string(out))
	if idx := strings.IndexByte(line, ' '); idx > 0 {
		return line[:idx], line[idx+1:]
	}
	return line, ""
}

// gitDirtyStatus checks if a worktree has uncommitted changes.
func gitDirtyStatus(path string) (dirty bool, fileCount int) {
	cmd := exec.Command("git", "-C", path, "status", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return false, 0
	}
	lines := strings.TrimSpace(string(out))
	if lines == "" {
		return false, 0
	}
	return true, strings.Count(lines, "\n") + 1
}

// gitPorcelain returns the raw git status --porcelain output.
func gitPorcelain(path string) string {
	cmd := exec.Command("git", "-C", path, "status", "--porcelain")
	out, _ := cmd.Output()
	s := strings.TrimSpace(string(out))
	if len(s) > 500 {
		s = s[:500] + "…"
	}
	return s
}

// Close releases all held slots and closes the database.
func (fm *ForgeManager) Close() {
	fm.mu.Lock()
	slots := make(map[string]*forge.Slot, len(fm.held))
	for k, v := range fm.held {
		slots[k] = v
	}
	fm.held = make(map[string]*forge.Slot)
	fm.mu.Unlock()

	for _, slot := range slots {
		fm.forge.Release(slot.Project, slot.ID)
	}

	if fm.forge != nil {
		fm.forge.Close()
	}
}
