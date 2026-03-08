package main

import (
	"log"
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

// Release returns a slot to the pool.
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
