package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/kayushkin/forge"
)

// EnvironmentManager handles environment acquisition and multi-repo deployment.
type EnvironmentManager struct {
	forge *forge.Forge
	mu    sync.Mutex
	held  map[int]string // envID → sessionID
}

// NewEnvironmentManager opens the forge database.
func NewEnvironmentManager() *EnvironmentManager {
	f, err := forge.Open("")
	if err != nil {
		log.Printf("[env] failed to open forge: %v (environment management disabled)", err)
		return &EnvironmentManager{held: make(map[int]string)}
	}
	log.Printf("[env] opened %s", forge.DefaultPath())
	em := &EnvironmentManager{forge: f, held: make(map[int]string)}
	go em.backgroundFetch()
	return em
}

// backgroundFetch periodically fetches origin/main for all base repos.
func (em *EnvironmentManager) backgroundFetch() {
	if em.forge == nil {
		return
	}
	time.Sleep(5 * time.Second)
	for {
		projects, err := em.forge.ListProjects()
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

// Acquire acquires an environment for an agent session.
func (em *EnvironmentManager) Acquire(agentName, sessionID string) (envID int, envPath string, basePort int) {
	if em.forge == nil {
		return 0, "", 0
	}

	em.mu.Lock()
	defer em.mu.Unlock()

	env, err := em.forge.AcquireEnvironment(forge.AcquireOpts{
		AgentID:      agentName,
		SessionID:    sessionID,
		Orchestrator: "bus-agent",
	})
	if err != nil {
		log.Printf("[env] no available environments for %s: %v", agentName, err)
		return 0, "", 0
	}

	// Clean all repos in the environment
	em.forge.CleanEnvironment(env.ID)
	em.held[env.ID] = sessionID

	log.Printf("[env] %s acquired %s (base_port: %d)", agentName, env.Name, env.BasePort)
	return env.ID, forge.DefaultEnvDir + "/" + env.Name, env.BasePort
}

// Release returns an environment to the pool.
func (em *EnvironmentManager) Release(envID int) {
	if em.forge == nil || envID == 0 {
		return
	}

	em.mu.Lock()
	delete(em.held, envID)
	em.mu.Unlock()

	if err := em.forge.ReleaseEnvironment(envID); err != nil {
		log.Printf("[env] failed to release env %d: %v", envID, err)
		return
	}
	log.Printf("[env] released env-%d", envID)
	em.notifyRelease()
}

var envRelease = make(chan struct{}, 10)

func (em *EnvironmentManager) notifyRelease() {
	select {
	case envRelease <- struct{}{}:
	default:
	}
}

// WaitForEnvironment blocks until an environment is available.
func (em *EnvironmentManager) WaitForEnvironment(ctx context.Context) bool {
	select {
	case <-envRelease:
		return true
	case <-ctx.Done():
		return false
	}
}

// EnvironmentInfo is the API response for an environment.
type EnvironmentInfo struct {
	ID        int                   `json:"id"`
	Name      string                `json:"name"`
	BasePort  int                   `json:"base_port"`
	Status    string                `json:"status"`
	Agent     string                `json:"agent,omitempty"`
	SessionID string                `json:"session_id,omitempty"`
	Repos     []EnvironmentRepoInfo `json:"repos"`
}

// EnvironmentRepoInfo is repo status within an environment.
type EnvironmentRepoInfo struct {
	ProjectID    string `json:"project_id"`
	WorktreePath string `json:"worktree_path"`
	Branch       string `json:"branch"`
	Commit       string `json:"commit"`
	CommitMsg    string `json:"commit_message"`
	Dirty        bool   `json:"dirty"`
	DirtyFiles   int    `json:"dirty_files"`
	Ahead        int    `json:"ahead"`
	Behind       int    `json:"behind"`
}

// Status returns all environments with their repo status.
func (em *EnvironmentManager) Status() []EnvironmentInfo {
	if em.forge == nil {
		return nil
	}

	envs, err := em.forge.AllEnvironments()
	if err != nil {
		return nil
	}

	var result []EnvironmentInfo
	for _, env := range envs {
		info := EnvironmentInfo{
			ID:        env.ID,
			Name:      env.Name,
			BasePort:  env.BasePort,
			Status:    env.Status,
			Agent:     env.AgentID,
			SessionID: env.SessionID,
		}

		repos, err := em.forge.GetEnvironmentRepos(env.ID)
		if err == nil {
			for _, r := range repos {
				repoInfo := EnvironmentRepoInfo{
					ProjectID:    r.ProjectID,
					WorktreePath: r.WorktreePath,
					Branch:       r.Branch,
				}

				// Git status
				if hash, msg := gitHeadInfo(r.WorktreePath); hash != "" {
					repoInfo.Commit = hash
					repoInfo.CommitMsg = msg
				}
				if dirty, count := gitDirtyStatus(r.WorktreePath); dirty {
					repoInfo.Dirty = true
					repoInfo.DirtyFiles = count
				}
				repoInfo.Ahead, repoInfo.Behind = gitAheadBehind(r.WorktreePath)

				info.Repos = append(info.Repos, repoInfo)
			}
		}

		result = append(result, info)
	}
	return result
}

// Close releases all held environments and closes the database.
func (em *EnvironmentManager) Close() {
	em.mu.Lock()
	defer em.mu.Unlock()

	// Release all held environments
	for envID := range em.held {
		em.forge.ReleaseEnvironment(envID)
	}

	if em.forge != nil {
		em.forge.Close()
	}
}

// DeployAll builds and deploys all services in an environment.
func (em *EnvironmentManager) DeployAll(envID int, triggeredBy string) error {
	if em.forge == nil {
		return fmt.Errorf("forge not available")
	}

	env, err := em.forge.GetEnvironment(envID)
	if err != nil {
		return fmt.Errorf("environment %d not found", envID)
	}

	// Auto-commit dirty files in all repos
	repos, _ := em.forge.GetEnvironmentRepos(envID)
	for _, r := range repos {
		autoCommitDirty(r.WorktreePath, triggeredBy)
	}

	// Run deploy in background
	go em.runDeploy(env, triggeredBy)
	return nil
}

// runDeploy executes the full-stack deploy for an environment.
func (em *EnvironmentManager) runDeploy(env *forge.Environment, triggeredBy string) {
	log.Printf("[env] starting full-stack deploy for %s", env.Name)

	// 1. Build all Go services
	repos, _ := em.forge.GetEnvironmentRepos(env.ID)
	for _, r := range repos {
		// Skip libraries
		if strings.HasSuffix(r.ProjectID, "store") || r.ProjectID == "forge" {
			continue
		}

		// Check if it has go.mod
		if _, err := os.Stat(r.WorktreePath + "/go.mod"); err != nil {
			continue
		}

		log.Printf("[env] building %s...", r.ProjectID)
		cmd := exec.Command("go", "build", "-o", expandHome("~/bin/"+r.ProjectID), ".")
		cmd.Dir = r.WorktreePath
		if out, err := cmd.CombinedOutput(); err != nil {
			log.Printf("[env] build %s failed: %v\n%s", r.ProjectID, err, out)
			return
		}
	}

	// 2. Build frontend (kayushkin)
	for _, r := range repos {
		if r.ProjectID == "kayushkin" {
			log.Printf("[env] building kayushkin frontend...")
			cmd := exec.Command("npx", "vite", "build")
			cmd.Dir = r.WorktreePath
			if out, err := cmd.CombinedOutput(); err != nil {
				log.Printf("[env] frontend build failed: %v\n%s", err, out)
				return
			}
		}
	}

	log.Printf("[env] deploy %s complete - services on ports %d-%d", env.Name, env.BasePort, env.BasePort+30)
}
