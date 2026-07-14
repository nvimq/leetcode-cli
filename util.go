package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---- JSON output contract: stdout always JSON, stderr human debug ----

type Out struct {
	Ok    bool        `json:"ok"`
	Data  interface{} `json:"data,omitempty"`
	Error string      `json:"error,omitempty"`
	Hint  string      `json:"hint,omitempty"`
}

func emitOK(data interface{}) {
	b, _ := json.Marshal(Out{Ok: true, Data: data})
	fmt.Fprintln(os.Stdout, string(b))
}

func emitErr(code string, hint string) {
	b, _ := json.Marshal(Out{Ok: false, Error: code, Hint: hint})
	fmt.Fprintln(os.Stdout, string(b))
}

// stderr debug log
var dbgMutex sync.Mutex

func dbg(format string, args ...interface{}) {
	dbgMutex.Lock()
	defer dbgMutex.Unlock()
	fmt.Fprintf(os.Stderr, "[dbg] "+format+"\n", args...)
}

// ---- .env loader (minimal, no external deps) ----

func loadEnv(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		k := strings.TrimSpace(parts[0])
		v := strings.TrimSpace(parts[1])
		// strip surrounding quotes
		if (strings.HasPrefix(v, "\"") && strings.HasSuffix(v, "\"")) ||
			(strings.HasPrefix(v, "'") && strings.HasSuffix(v, "'")) {
			v = v[1 : len(v)-1]
		}
		os.Setenv(k, v)
	}
	return nil
}

// EnsureEnv loads .env from cwd, then walks up to repo root, then defaults to ~/.leetcode-cli/.env.
// First one that exists wins (earliest in order).
func EnsureEnv() {
	candidates := []string{".env"}
	if wd, err := os.Getwd(); err == nil {
		dir := wd
		for i := 0; i < 10; i++ {
			p := filepath.Join(dir, ".env")
			candidates = append(candidates, p)
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	home, _ := os.UserHomeDir()
	candidates = append(candidates, filepath.Join(home, ".leetcode-cli", ".env"))
	for _, p := range candidates {
		if loadEnv(p) == nil {
			dbg("loaded env from %s", p)
			return
		}
	}
	dbg("no .env found")
}

// ---- state dir ~/.leetcode-cli ----

func stateDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".leetcode-cli")
}

func ensureStateDir() {
	if err := os.MkdirAll(stateDir(), 0o755); err != nil {
		dbg("mkdir state: %v", err)
	}
}

func statePath() string  { return filepath.Join(stateDir(), "state.json") }
func logPath() string    { return filepath.Join(stateDir(), "log.jsonl") }
func stopPath() string   { return filepath.Join(stateDir(), "STOP") }

// writeStopMarker creates ~/.leetcode-cli/STOP and notifies the userProbably
// via macOS `say` / osascript so they don't have to poll manually every hour.
// Idempotent: if STOP already exists returns silently.
func writeStopMarker(reason string) {
	ensureStateDir()
	_ = os.WriteFile(stopPath(), []byte(time.Now().UTC().Format(time.RFC3339)+" "+reason+"\n"), 0o644)
	notifyUser("leetcode-cli STOP", reason)
}

func clearStopMarker() {
	_ = os.Remove(stopPath())
}

// notifyUser shows a desktop notification (macOS) and speaks the reason.
// Falls back to stderr on non-macOS / when commands are missing.
func notifyUser(title, msg string) {
	dbg("NOTIFY %s: %s", title, msg)
	// try say (voice) — short
	if _, err := exec.LookPath("say"); err == nil {
		_ = exec.Command("say", "-r", "180", title+": "+msg).Start()
	}
	// try osascript (banner) — silent if unavailable
	if _, err := exec.LookPath("osascript"); err == nil {
		script := "display notification " + quoteApple(msg) + " with title " + quoteApple(title)
		_ = exec.Command("osascript", "-e", script).Start()
	}
}

func quoteApple(s string) string {
	return "\"" + strings.ReplaceAll(s, "\"", "\\\"") + "\""
}
func progressPath() string {
	// progress.md is project-local if it exists, else in state dir
	if _, err := os.Stat("progress.md"); err == nil {
		return "progress.md"
	}
	return filepath.Join(stateDir(), "progress.md")
}

// ---- state.json ----

type stateFile struct {
	LastSubmitTS int64 `json:"last_submit_ts"`
}

func loadState() stateFile {
	var s stateFile
	b, err := os.ReadFile(statePath())
	if err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

func saveState(s stateFile) {
	ensureStateDir()
	b, _ := json.Marshal(s)
	_ = os.WriteFile(statePath(), b, 0o644)
}

// EnforceSubmitRateLimit blocks until at least `minInterval` + random jitter
// has passed since the last submit. Jitter avoids a fixed 5s pattern that
// anti-fraud systems flag on mass runs.
//
// Effective cooldown = 5s + uniform(0, 3s) = 5-8s between submits.
const (
	submitMinInterval = 5 * time.Second
	submitJitter      = 3 * time.Second
)

// randSource is package-level so we don't reseed every call.
var randSource = rand.New(rand.NewSource(time.Now().UnixNano()))

func enforceSubmitCooldown() {
	s := loadState()
	if s.LastSubmitTS > 0 {
		base := submitMinInterval + time.Duration(randSource.Int63n(int64(submitJitter)))
		elapsed := time.Since(time.Unix(s.LastSubmitTS, 0))
		if elapsed < base {
			remaining := base - elapsed
			dbg("rate-limit cooldown: waiting %s (base=%s)", remaining, base)
			time.Sleep(remaining)
		}
	}
}

func markSubmitTs() {
	s := loadState()
	s.LastSubmitTS = time.Now().Unix()
	saveState(s)
}

// ---- log.jsonl logging ----

func appendLog(endpoint string, status int, latency time.Duration, extra string) {
	ensureStateDir()
	f, err := os.OpenFile(logPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	entry := map[string]interface{}{
		"ts":        time.Now().UTC().Format(time.RFC3339),
		"endpoint":  endpoint,
		"status":    status,
		"latency_ms": latency.Milliseconds(),
	}
	if extra != "" {
		entry["note"] = extra
	}
	b, _ := json.Marshal(entry)
	fmt.Fprintln(f, string(b))
}

// ---- helpers ----

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }