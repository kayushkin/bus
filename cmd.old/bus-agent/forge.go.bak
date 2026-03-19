package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/kayushkin/forge"
)

// ForgeManager handles environment acquisition and release for agent tasks.
type ForgeManager struct {
	forge *forge.Forge
	mu    sync.Mutex
	held  map[string]int // sessionKey → envID
}

func NewForgeManager() *ForgeManager {
	f, err := forge.Open("")
	if err != nil {
		log.Printf("[forge] failed to open: %v (disabled)", err)
		return &ForgeManager{held: make(map[string]int)}
	}
	log.Printf("[forge] opened %s", forge.DefaultPath())
	fm := &ForgeManager{forge: f, held: make(map[string]int)}
	go fm.backgroundFetch()
	return fm
}

func (fm *ForgeManager) backgroundFetch() {
	if fm.forge == nil {
		return
	}
	time.Sleep(5 * time.Second)
	for {
		projects, err := fm.forge.ListProjects()
		if err == nil {
			seen := map[string]bool{}
			for _, p := range projects {
				repo := expandHome(p.BaseRepo)
				if seen[repo] {
					continue
				}
				seen[repo] = true
				cmd := exec.Command("git", "-C", repo, "fetch", "origin", "main", "--quiet")
				cmd.Run()
			}
		}
		time.Sleep(60 * time.Second)
	}
}

// AcquireByProject acquires an environment for an agent.
func (fm *ForgeManager) AcquireByProject(agentName, sessionID, projectID string) (slotPath string, slotKey string, slotID int) {
	if fm.forge == nil {
		return "", "", 0
	}

	fm.mu.Lock()
	defer fm.mu.Unlock()

	env, err := fm.forge.AcquireEnvironment(forge.AcquireOpts{
		AgentID:      agentName,
		SessionID:    sessionID,
		Orchestrator: "bus-agent",
	})
	if err != nil {
		log.Printf("[forge] no env for %q (project %s): %v", agentName, projectID, err)
		return "", "", 0
	}

	repos, _ := fm.forge.GetEnvironmentRepos(env.ID)
	var path string
	for _, r := range repos {
		if r.ProjectID == projectID {
			path = r.WorktreePath
			break
		}
	}
	if path == "" && len(repos) > 0 {
		path = repos[0].WorktreePath
	}
	if path == "" {
		log.Printf("[forge] env %d has no repo for project %s", env.ID, projectID)
		fm.forge.ReleaseEnvironment(env.ID)
		return "", "", 0
	}

	key := fmt.Sprintf("%s:%s", projectID, sessionID)
	fm.held[key] = env.ID
	log.Printf("[forge] %s acquired env %d → %s", agentName, env.ID, path)
	return path, key, env.ID
}

func (fm *ForgeManager) Release(slotKey string) {
	if fm.forge == nil || slotKey == "" {
		return
	}
	fm.mu.Lock()
	envID, ok := fm.held[slotKey]
	if ok {
		delete(fm.held, slotKey)
	}
	fm.mu.Unlock()
	if !ok {
		return
	}
	if err := fm.forge.ReleaseEnvironment(envID); err != nil {
		log.Printf("[forge] release env %d failed: %v", envID, err)
	}
}

var slotRelease = make(chan struct{}, 10)

func (fm *ForgeManager) WaitForSlot(ctx interface{ Done() <-chan struct{} }) bool {
	select {
	case <-slotRelease:
		return true
	case <-ctx.Done():
		return false
	}
}

// SlotInfo is the rich status of a single slot for the API.
type SlotInfo struct {
	ID                 int    `json:"id"`
	Project            string `json:"project"`
	Status             string `json:"status"`
	Agent              string `json:"agent,omitempty"`
	SessionID          string `json:"session_id,omitempty"`
	Branch             string `json:"branch,omitempty"`
	Commit             string `json:"commit,omitempty"`
	CommitMsg          string `json:"commit_message,omitempty"`
	Dirty              bool   `json:"dirty"`
	DirtyFiles         int    `json:"dirty_files,omitempty"`
	UncommittedChanges string `json:"uncommitted_changes,omitempty"`
	Ahead              int    `json:"ahead"`
	Behind             int    `json:"behind"`
	CommitTime         int64  `json:"commit_time,omitempty"`
	Path               string `json:"path"`
}

type ProjectStatus struct {
	ID       string     `json:"id"`
	BaseRepo string     `json:"base_repo"`
	Slots    []SlotInfo `json:"slots"`
}

func (fm *ForgeManager) Status() []ProjectStatus {
	if fm.forge == nil {
		return nil
	}
	projects, err := fm.forge.ListProjects()
	if err != nil {
		return nil
	}
	envs, _ := fm.forge.AllEnvironments()

	var result []ProjectStatus
	for _, p := range projects {
		ps := ProjectStatus{ID: p.ID, BaseRepo: p.BaseRepo}
		for _, env := range envs {
			repos, _ := fm.forge.GetEnvironmentRepos(env.ID)
			for _, r := range repos {
				if r.ProjectID == p.ID {
					si := SlotInfo{
						ID:        env.ID,
						Project:   p.ID,
						Status:    env.Status,
						Agent:     env.AgentID,
						SessionID: env.SessionID,
						Path:      r.WorktreePath,
					}
					si.Commit, si.CommitMsg = gitHeadInfo(r.WorktreePath)
					dirty, dirtyFiles := gitDirtyStatus(r.WorktreePath)
					si.Dirty = dirty
					si.DirtyFiles = dirtyFiles
					si.Ahead, si.Behind = gitAheadBehind(r.WorktreePath)
					si.CommitTime = gitCommitTime(r.WorktreePath)
					ps.Slots = append(ps.Slots, si)
				}
			}
		}
		result = append(result, ps)
	}
	return result
}

func (fm *ForgeManager) ForceRelease(project string, slotID int) {
	if fm.forge == nil {
		return
	}
	fm.mu.Lock()
	for key, id := range fm.held {
		if id == slotID {
			delete(fm.held, key)
			break
		}
	}
	fm.mu.Unlock()
	fm.forge.ReleaseEnvironment(slotID)
}

func (fm *ForgeManager) CleanSlot(project string, slotID int) {
	if fm.forge == nil {
		return
	}
	fm.forge.CleanEnvironment(slotID)
}

func (fm *ForgeManager) CommitAndDeploy(slotKey, summary string) {
	if fm.forge == nil || slotKey == "" {
		return
	}
	fm.mu.Lock()
	envID, ok := fm.held[slotKey]
	fm.mu.Unlock()
	if !ok {
		return
	}
	repos, _ := fm.forge.GetEnvironmentRepos(envID)
	for _, r := range repos {
		dirty, _ := gitDirtyStatus(r.WorktreePath)
		if dirty {
			autoCommitDirty(r.WorktreePath, summary)
		}
	}
}

func (fm *ForgeManager) DeployDev(project string, slotID int, triggeredBy string) (int64, error) {
	if fm.forge == nil {
		return 0, fmt.Errorf("forge not available")
	}
	hash, msg := gitHeadInfo("")
	return fm.forge.RecordDeploy(project, fmt.Sprintf("dev-%d", slotID), hash, msg, "", triggeredBy)
}

func (fm *ForgeManager) DeployProd(project, repoDir, triggeredBy string) (int64, error) {
	if fm.forge == nil {
		return 0, fmt.Errorf("forge not available")
	}
	hash, msg := gitHeadInfo(repoDir)
	branch := gitCurrentBranch(repoDir)
	return fm.forge.RecordDeploy(project, "prod", hash, msg, branch, triggeredBy)
}

func (fm *ForgeManager) DeployCommit(project string, slotID int, commitHash, triggeredBy string) (int64, error) {
	return fm.DeployDev(project, slotID, triggeredBy)
}

func (fm *ForgeManager) Deploys(project, target string, limit int) ([]forge.Deploy, error) {
	if fm.forge == nil {
		return nil, fmt.Errorf("forge not available")
	}
	if project == "" {
		return fm.forge.AllDeploys(limit)
	}
	return fm.forge.ListDeploys(project, target, limit)
}

func (fm *ForgeManager) Close() {
	fm.mu.Lock()
	for key, envID := range fm.held {
		fm.forge.ReleaseEnvironment(envID)
		delete(fm.held, key)
	}
	fm.mu.Unlock()
	if fm.forge != nil {
		fm.forge.Close()
	}
}

// --- git helpers ---

func gitHeadInfo(path string) (hash, msg string) {
	if path == "" {
		return "", ""
	}
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

func gitDirtyStatus(path string) (dirty bool, fileCount int) {
	if path == "" {
		return false, 0
	}
	cmd := exec.Command("git", "-C", path, "status", "--porcelain", "-uno")
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

func gitPorcelain(path string) string {
	cmd := exec.Command("git", "-C", path, "status", "--porcelain")
	out, _ := cmd.Output()
	s := strings.TrimSpace(string(out))
	if len(s) > 500 {
		s = s[:500] + "…"
	}
	return s
}

func gitAheadBehind(path string) (ahead, behind int) {
	if path == "" {
		return 0, 0
	}
	cmd := exec.Command("git", "-C", path, "rev-list", "--left-right", "--count", "HEAD...origin/main")
	out, err := cmd.Output()
	if err != nil {
		return 0, 0
	}
	fmt.Sscanf(strings.TrimSpace(string(out)), "%d\t%d", &ahead, &behind)
	return
}

func gitCommitTime(path string) int64 {
	if path == "" {
		return 0
	}
	cmd := exec.Command("git", "-C", path, "log", "-1", "--format=%ct")
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	var t int64
	fmt.Sscan(strings.TrimSpace(string(out)), &t)
	return t
}

func gitCurrentBranch(path string) string {
	cmd := exec.Command("git", "-C", path, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func autoCommitDirty(path, commitMsg string) {
	cmd := exec.Command("git", "-C", path, "status", "--porcelain")
	out, err := cmd.Output()
	if err != nil || len(strings.TrimSpace(string(out))) == 0 {
		return
	}
	exec.Command("git", "-C", path, "add", "-A").Run()
	exec.Command("git", "-C", path, "reset", "HEAD", "--", ".inber/").Run()
	cmd = exec.Command("git", "-C", path, "diff", "--cached", "--quiet")
	if cmd.Run() == nil {
		return
	}
	exec.Command("git", "-C", path, "commit", "-m", commitMsg).Run()
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) <= n {
		return strings.TrimSpace(s)
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

// API handler wrappers that were in main.go but depend on forge types

func (ba *BusAgent) handleForgeStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	status := ba.forge.Status()
	if status == nil {
		status = []ProjectStatus{}
	}
	json.NewEncoder(w).Encode(status)
}

func (ba *BusAgent) handleForgeDeploy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Project     string `json:"project"`
		Target      string `json:"target"`
		Slot        int    `json:"slot"`
		Commit      string `json:"commit"`
		TriggeredBy string `json:"triggered_by"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
		return
	}
	if req.Project == "" {
		http.Error(w, `{"error":"project required"}`, http.StatusBadRequest)
		return
	}
	if req.TriggeredBy == "" {
		req.TriggeredBy = "dashboard"
	}

	var deployID int64
	var err error
	if req.Target == "prod" {
		projects := ba.forge.Status()
		var deployDir string
		for _, p := range projects {
			if p.ID == req.Project {
				deployDir = p.BaseRepo
			}
		}
		if deployDir == "" {
			http.Error(w, `{"error":"project not found"}`, http.StatusNotFound)
			return
		}
		deployID, err = ba.forge.DeployProd(req.Project, deployDir, req.TriggeredBy)
	} else {
		deployID, err = ba.forge.DeployDev(req.Project, req.Slot, req.TriggeredBy)
	}
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"deploy_id": deployID, "status": "running"})
}

func (ba *BusAgent) handleForgeRelease(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Project string `json:"project"`
		Slot    int    `json:"slot"`
		Clean   bool   `json:"clean"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
		return
	}
	if req.Clean {
		ba.forge.CleanSlot(req.Project, req.Slot)
	}
	ba.forge.ForceRelease(req.Project, req.Slot)
	json.NewEncoder(w).Encode(map[string]string{"status": "released"})
}

func (ba *BusAgent) handleForgeDeploys(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	project := r.URL.Query().Get("project")
	target := r.URL.Query().Get("target")
	deploys, err := ba.forge.Deploys(project, target, 20)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}
	if deploys == nil {
		json.NewEncoder(w).Encode([]struct{}{})
		return
	}
	json.NewEncoder(w).Encode(deploys)
}
