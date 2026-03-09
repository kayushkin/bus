package main

import (
	"context"
	"fmt"
	"log"
	"os"
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

// AcquireByProject tries to get a slot for an agent by forge project ID.
func (fm *ForgeManager) AcquireByProject(agentName, sessionID, projectID string) (slotPath string, slotKey string) {
	if fm.forge == nil {
		return "", ""
	}

	fm.mu.Lock()
	defer fm.mu.Unlock()

	slot, err := fm.forge.Acquire(projectID, forge.AcquireOpts{
		AgentID:      agentName,
		SessionID:    sessionID,
		Orchestrator: "bus-agent",
	})
	if err != nil {
		log.Printf("[forge] no slots for %q (project %s): %v", agentName, projectID, err)
		return "", ""
	}

	// Clean slot (reset to origin/main) then pull latest
	fm.forge.CleanSlot(projectID, slot.ID)
	fm.forge.SlotPull(projectID, slot.ID)

	key := projectID + ":" + sessionID
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

// DeployDev deploys a slot to its dev preview (build + serve).
// Auto-commits any dirty files before deploying so the commit hash is accurate.
func (fm *ForgeManager) DeployDev(project string, slotID int, triggeredBy string) (int64, error) {
	if fm.forge == nil {
		return 0, fmt.Errorf("forge not available")
	}

	slots, err := fm.forge.SlotStatus(project)
	if err != nil {
		return 0, err
	}
	var slot *forge.Slot
	for _, s := range slots {
		if s.ID == slotID {
			slot = &s
			break
		}
	}
	if slot == nil {
		return 0, fmt.Errorf("slot %d not found for project %s", slotID, project)
	}

	// Auto-commit dirty files so deploy commit hash is accurate
	autoCommitDirty(slot.Path, triggeredBy)

	hash, msg := gitHeadInfo(slot.Path)
	branch := gitCurrentBranch(slot.Path)
	target := fmt.Sprintf("dev-%d", slotID)

	deployID, err := fm.forge.RecordDeploy(project, target, hash, msg, branch, triggeredBy)
	if err != nil {
		return 0, err
	}

	// Run deploy-dev.sh in background
	go func() {
		cmd := exec.Command(expandHome("~/bin/deploy-dev.sh"), fmt.Sprint(slotID), slot.Path)
		cmd.Env = append(os.Environ(), fmt.Sprintf("SLOT_PATH=%s", slot.Path))
		out, err := cmd.CombinedOutput()
		if err != nil {
			log.Printf("[forge] deploy dev-%d failed: %v\n%s", slotID, err, out)
			fm.forge.FinishDeploy(deployID, false, fmt.Sprintf("%v: %s", err, lastLines(string(out), 5)))
		} else {
			log.Printf("[forge] deploy dev-%d success", slotID)
			fm.forge.FinishDeploy(deployID, true, "")
		}
	}()

	return deployID, nil
}

// DeployProd deploys to production.
func (fm *ForgeManager) DeployProd(project, repoDir, triggeredBy string) (int64, error) {
	if fm.forge == nil {
		return 0, fmt.Errorf("forge not available")
	}

	hash, msg := gitHeadInfo(repoDir)
	branch := gitCurrentBranch(repoDir)

	deployID, err := fm.forge.RecordDeploy(project, "prod", hash, msg, branch, triggeredBy)
	if err != nil {
		return 0, err
	}

	go func() {
		cmd := exec.Command(expandHome("~/bin/deploy-prod.sh"), repoDir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			log.Printf("[forge] deploy prod failed: %v\n%s", err, out)
			fm.forge.FinishDeploy(deployID, false, fmt.Sprintf("%v: %s", err, lastLines(string(out), 5)))
		} else {
			log.Printf("[forge] deploy prod success")
			fm.forge.FinishDeploy(deployID, true, "")
		}
	}()

	return deployID, nil
}

// DeployCommit checks out a specific commit in a slot and deploys it.
func (fm *ForgeManager) DeployCommit(project string, slotID int, commitHash, triggeredBy string) (int64, error) {
	if fm.forge == nil {
		return 0, fmt.Errorf("forge not available")
	}

	slots, err := fm.forge.SlotStatus(project)
	if err != nil {
		return 0, err
	}
	var slot *forge.Slot
	for _, s := range slots {
		if s.ID == slotID {
			slot = &s
			break
		}
	}
	if slot == nil {
		return 0, fmt.Errorf("slot %d not found", slotID)
	}

	// Checkout the specific commit
	cmd := exec.Command("git", "-C", slot.Path, "checkout", commitHash)
	if out, err := cmd.CombinedOutput(); err != nil {
		return 0, fmt.Errorf("git checkout %s: %v\n%s", commitHash, err, out)
	}

	return fm.DeployDev(project, slotID, triggeredBy)
}

// Deploys returns deploy history.
func (fm *ForgeManager) Deploys(project, target string, limit int) ([]forge.Deploy, error) {
	if fm.forge == nil {
		return nil, fmt.Errorf("forge not available")
	}
	if project == "" {
		return fm.forge.AllDeploys(limit)
	}
	return fm.forge.ListDeploys(project, target, limit)
}

// gitCurrentBranch returns the current branch name.
func gitCurrentBranch(path string) string {
	cmd := exec.Command("git", "-C", path, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// lastLines returns the last N lines of a string.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) <= n {
		return strings.TrimSpace(s)
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

// autoCommitDirty commits any uncommitted changes in a worktree.
// Skips .inber/ session artifacts.
func autoCommitDirty(path, triggeredBy string) {
	// Check if dirty
	cmd := exec.Command("git", "-C", path, "status", "--porcelain")
	out, err := cmd.Output()
	if err != nil || len(strings.TrimSpace(string(out))) == 0 {
		return
	}

	// Add everything except .inber/
	cmd = exec.Command("git", "-C", path, "add", "-A")
	cmd.Run()
	cmd = exec.Command("git", "-C", path, "reset", "HEAD", "--", ".inber/")
	cmd.Run()

	// Check if there's anything staged after excluding .inber
	cmd = exec.Command("git", "-C", path, "diff", "--cached", "--quiet")
	if cmd.Run() == nil {
		return // nothing staged
	}

	msg := fmt.Sprintf("deploy: auto-commit by %s", triggeredBy)
	cmd = exec.Command("git", "-C", path, "commit", "-m", msg)
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("[forge] auto-commit failed: %v\n%s", err, out)
	} else {
		log.Printf("[forge] auto-committed dirty files in %s", path)
	}
}

// expandHome is in backend.go
