package main

import (
	"fmt"
	"os"
	"strings"
)

// SolveCmd: `leetcode-cli solve <slug>`
//
// Full automated pipeline:
//  1. fetch the problem (detect Go vs SQL)
//  2. fetch the top community Go solution from halfrost/LeetCode-Go (GitHub)
//  3. clean (strip package/comments/non-ASCII) and write to solution.go
//  4. submit to LeetCode
//  5. emit JSON result + update progress.md
//
// For SQL problems: the agent should solve manually (no community repo yet).
// For Go problems not in the repo: falls back to agent manual solve.
func SolveCmd(args []string) {
	EnsureEnv()
	if len(args) < 1 {
		emitErr("BAD_ARGS", "usage: leetcode-cli solve <slug>")
		return
	}
	slug := strings.ToLower(strings.TrimSpace(args[0]))

	c, err := NewClient()
	if err != nil {
		emitErr("SESSION_EXPIRED", err.Error())
		return
	}

	// Step 1: fetch question
	q, err := c.GetQuestion(slug)
	if err != nil {
		switch {
		case isErr(err, "SESSION_EXPIRED"):
			emitErr("SESSION_EXPIRED", err.Error())
		case isErr(err, "NOT_FOUND"):
			emitErr("NOT_FOUND", slug)
		default:
			emitErr("FETCH_FAILED", err.Error())
		}
		return
	}

	// Determine language
	lang := "golang"
	solFile := "solution.go"
	if q.GoStarterCode() == "" && (q.SQLStarterCode() != "" || q.Category() == "database") {
		lang = "mysql"
		solFile = "solution.sql"
	}

	// Check paid-only
	if q.IsPaidOnly && q.Content == "" && q.TranslatedContent == "" {
		_ = appendProgress(slug, "SKIP", 0)
		emitErr("UNSOLVABLE", "paid_only_locked")
		return
	}

	// SQL: no auto-solve yet
	if lang != "golang" {
		_ = appendProgress(slug, "STUCK", 0)
		emitErr("NO_AUTO_SOLVE_SQL", "SQL problems require manual solve — use fetch + write solution.sql + submit")
		return
	}

	// Step 2: fetch Go solution from GitHub repo
	rawCode, err := fetchGitHubSolution(q)
	if err != nil {
		_ = appendProgress(slug, "NO_SOLUTION", 0)
		emitErr("NO_SOLUTION", err.Error())
		return
	}

	// Step 3: clean the code
	body := stripMainBoilerplate(rawCode)
	body = stripLineComments(body)
	if loc := firstNonASCII(body); loc >= 0 {
		// non-ASCII (likely Chinese chars in code, not comments)
		// try to extract just the function body between `// 解法` markers
		// fallback: strip everything between `package` and `func` and after last `}`
		_ = loc
		// For now just reject
		_ = appendProgress(slug, "STUCK", 0)
		emitErr("INVALID_CODE", fmt.Sprintf("non-ASCII byte at %d in community solution: %s", loc, around(body, loc, 80)))
		return
	}

	// write cleaned code to file for inspection
	_ = os.WriteFile(solFile, []byte(body), 0o644)

	// Step 4: submit
	subID, err := c.Submit(slug, q.QuestionId, lang, body)
	if err != nil {
		switch {
		case isErr(err, "SESSION_EXPIRED"):
			emitErr("SESSION_EXPIRED", err.Error())
			return
		case isErr(err, "RATE_LIMITED"):
			emitErr("RATE_LIMITED", fmt.Sprintf("retry_after_seconds=%d", extractRetryAfter(err)))
			return
		default:
			_ = appendProgress(slug, "SUBMIT_FAILED", 0)
			emitErr("SUBMIT_FAILED", err.Error())
			return
		}
	}

	res, err := c.PollResult(subID, 30*1000*1000*1000)
	if err != nil {
		switch {
		case isErr(err, "SESSION_EXPIRED"):
			emitErr("SESSION_EXPIRED", err.Error())
			return
		case isErr(err, "RATE_LIMITED"):
			emitErr("RATE_LIMITED", err.Error())
			return
		default:
			_ = appendProgress(slug, "POLL_FAILED", 0)
			emitErr("POLL_FAILED", err.Error())
			return
		}
	}

	result := buildSubmitResult(res, q)
	status := result.Status
	_ = appendProgress(slug, status, 1)

	emitOK(SolveResult{
		Status:            result.Status,
		RuntimeMs:         result.RuntimeMs,
		MemoryMB:          result.MemoryMB,
		Passed:            result.Passed,
		Total:             result.Total,
		RuntimePercentile: result.RuntimePercentile,
		FailedCase:        result.FailedCase,
		Source:            "github.com/halfrost/LeetCode-Go",
	})
}

type SolveResult struct {
	Status            string      `json:"status"`
	RuntimeMs         int         `json:"runtime_ms,omitempty"`
	MemoryMB          float64     `json:"memory_mb,omitempty"`
	Passed            int         `json:"passed,omitempty"`
	Total             int         `json:"total,omitempty"`
	RuntimePercentile float64     `json:"runtime_percentile,omitempty"`
	FailedCase        *FailedCase `json:"failed_case,omitempty"`
	Source            string      `json:"source,omitempty"`
}