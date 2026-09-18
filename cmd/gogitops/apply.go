// ── daemon recipe auto-apply: convergence loop ─────────────────────────────
//
// The gap this closes: the daemon ticks pull recipes every git interval but
// never APPLIES one — recipe execution was CLI-only (recipe run / run-all).
// With `recipes_interval` set in nodes/<host>.yaml agent config, the daemon
// converges on its own: new/changed recipes apply within one cycle, failed
// recipes retry every cycle, and a full sweep re-verifies everything daily
// (idempotent recipes show applied state as skipped — that's the drift heal).
//
// Semantics:
//   - recipes_interval node config ("off"/"0" or unset = disabled; e.g. "10m")
//   - per-recipe content hash tracked in ~/.cache/gogitops/apply-state.json
//   - unchanged ok/not-applicable recipes are skipped silently (no churn)
//   - node config (nodes/<host>.yaml) hash change → full sweep (labels changed)
//   - failures alert via the fleet webhook on TRANSITION into failed only
//   - test modules excluded (discoverRecipes), recipes exec in a child of the
//     agent binary (recipe run exits non-zero on step failure — process
//     isolation keeps os.Exit in recipeRun from ever killing the daemon)

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bennbanks/gogitops/internal/agentlog"
	"github.com/bennbanks/gogitops/internal/alert"
	"github.com/bennbanks/gogitops/internal/config"
)

const applyFullSweepEvery = 24 * time.Hour

// applyRecipeState is the tracked outcome of one recipe on this node.
type applyRecipeState struct {
	Hash   string `json:"hash"`
	Status string `json:"status"` // ok | failed | na
	Detail string `json:"detail,omitempty"` // first failure line when failed
	At     string `json:"at"`
}

// applyState is the daemon's local convergence state (outside the repo —
// the daemon self-heals its checkout, so state must not live in it).
type applyState struct {
	NodeConfigHash string                     `json:"node_config_hash"`
	LastFullSweep  string                     `json:"last_full_sweep"`
	Recipes        map[string]applyRecipeState `json:"recipes"`
}

func applyStatePath() string {
	cacheDir := os.Getenv("XDG_CACHE_HOME")
	if cacheDir == "" {
		home, _ := os.UserHomeDir()
		cacheDir = filepath.Join(home, ".cache")
	}
	return filepath.Join(cacheDir, "gogitops", "apply-state.json")
}

func loadApplyState() applyState {
	st := applyState{Recipes: map[string]applyRecipeState{}}
	if data, err := os.ReadFile(applyStatePath()); err == nil {
		_ = json.Unmarshal(data, &st)
		if st.Recipes == nil {
			st.Recipes = map[string]applyRecipeState{}
		}
	}
	return st
}

func saveApplyState(st applyState) {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	os.MkdirAll(filepath.Dir(applyStatePath()), 0755)
	tmp := applyStatePath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err == nil {
		_ = os.Rename(tmp, applyStatePath())
	}
}

// hashFileBytes returns the sha256 of a file's contents ("" on error).
func hashFileBytes(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// recipeFileKey derives the recipe name from a recipe file path:
// recipes/<name>.yaml → <name>; recipes/<dir>/<name>.yaml → <name>.
func recipeFileKey(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), ".yaml")
	return base
}

// nodeLabelsFor loads node labels without the LoadNode auto-registration
// side effect (same stat-guard recipeRun uses).
func nodeLabelsFor(repoDir, hostname string) []string {
	if _, err := os.Stat(filepath.Join(repoDir, "nodes", hostname+".yaml")); err != nil {
		return nil
	}
	if n, err := config.LoadNode(repoDir, hostname); err == nil {
		return n.Labels
	}
	return nil
}

// lastMeaningfulLine returns the last non-empty line of output, stripped of
// ANSI escapes — used as the failure detail in logs and alerts.
func lastMeaningfulLine(out []byte) string {
	lines := strings.Split(strings.ReplaceAll(string(out), "\r", "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(ansiStrip(lines[i]))
		if l != "" {
			return l
		}
	}
	return ""
}

func ansiStrip(s string) string {
	var b strings.Builder
	inEscape := false
	for _, r := range s {
		if inEscape {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscape = false
			}
			continue
		}
		if r == 0x1b {
			inEscape = true
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// runRecipeApplyLoop mirrors runFleetTestLoop: settle, first cycle, then tick.
func runRecipeApplyLoop(interval time.Duration, repoDir, hostname, webhook string) {
	time.Sleep(10 * time.Second)
	recipeApplyCycle(repoDir, hostname, webhook)
	ticker := time.NewTicker(interval)
	for range ticker.C {
		recipeApplyCycle(repoDir, hostname, webhook)
	}
}

// recipeApplyCycle is one convergence pass over the recipe set.
func recipeApplyCycle(repoDir, hostname, webhook string) {
	logger := agentlog.Default()
	resolved := resolveRepoDir(repoDir)
	st := loadApplyState()
	now := time.Now().Format(time.RFC3339)

	// Node config hash: a change (labels, intervals) forces a full sweep so
	// recipes that became applicable/unapplicable get re-evaluated.
	nodeCfgHash := ""
	if h, err := hashFileBytes(filepath.Join(resolved, "nodes", hostname+".yaml")); err == nil {
		nodeCfgHash = h
	}

	// Full sweep when: never run, ≥24h since the last one, or node config changed.
	fullSweep := true
	if t, err := time.Parse(time.RFC3339, st.LastFullSweep); err == nil {
		fullSweep = time.Since(t) >= applyFullSweepEvery || st.NodeConfigHash != nodeCfgHash
	}

	nodeLabels := nodeLabelsFor(resolved, hostname)
	files := discoverRecipes(resolved)
	var newFails []string

	for _, f := range files {
		key := recipeFileKey(f)
		h, err := hashFileBytes(f)
		if err != nil {
			continue
		}
		prev, known := st.Recipes[key]

		// Skip unchanged recipes that are ok or not-applicable (unless a
		// full sweep is due). Failed recipes always retry — drift heals.
		if !fullSweep && known && prev.Hash == h && prev.Status != "failed" {
			continue
		}

		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		r := parseRecipe(string(data))
		if r.Name == "" {
			r.Name = key
		}

		// Convergence is opt-in per recipe (auto_apply: true). Manual
		// `recipe run` / `run-all` is unaffected. Unmarked recipes are
		// parsed every cycle (cheap) but never exec'd by the daemon.
		if !r.AutoApply {
			continue
		}

		// Recipe-level applicability gate — same semantics as recipeRun.
		if len(r.Labels) > 0 && !labelsMatch(nodeLabels, r.Labels, nil) {
			if !known || prev.Status != "na" || prev.Hash != h {
				st.Recipes[key] = applyRecipeState{Hash: h, Status: "na", At: now}
			}
			continue
		}

		// Exec in a child of the agent binary: recipe run exits non-zero on
		// step failure and its os.Exit paths must never take the daemon down.
		self, selfErr := os.Executable()
		if selfErr != nil {
			logger.Errorf("recipe", "auto-apply: cannot resolve agent binary: %v", selfErr)
			return
		}
		out, runErr := exec.Command(self, "recipe", "run", f, "--repo", resolved, "--hostname", hostname).CombinedOutput()

		if runErr == nil {
			wasFailed := known && prev.Status == "failed"
			st.Recipes[key] = applyRecipeState{Hash: h, Status: "ok", At: now}
			if wasFailed {
				logger.Infof("recipe", "auto-apply: %s recovered (previously failed)", r.Name)
			} else {
				logger.Actionf("recipe", "auto-apply: %s converged (%d steps)", r.Name, len(r.Steps))
			}
		} else {
			detail := lastMeaningfulLine(out)
			wasFailed := known && prev.Status == "failed"
			st.Recipes[key] = applyRecipeState{Hash: h, Status: "failed", Detail: detail, At: now}
			if wasFailed {
				// still failing — log for /v1/logs, no repeat alert
				logger.Warnf("recipe", "auto-apply retry: %s still failing — %s", r.Name, detail)
			} else {
				logger.Errorf("recipe", "auto-apply FAILED: %s — %s", r.Name, detail)
				newFails = append(newFails, r.Name+": "+detail)
			}
		}
	}

	if fullSweep {
		st.LastFullSweep = now
		st.NodeConfigHash = nodeCfgHash
	}
	saveApplyState(st)

	// Alert only on transitions into failure (failed recipes retry silently
	// each cycle — one alert per new failure, not one per cycle).
	if len(newFails) > 0 && webhook != "" {
		if err := alert.NewSender(webhook).SendAlert("warn", hostname,
			"recipe auto-apply: "+strconv.Itoa(len(newFails))+" new failure(s) — "+strings.Join(newFails, " | ")); err != nil {
			logger.Warnf("alert", "auto-apply failure alert failed: %v", err)
		}
	}
}
