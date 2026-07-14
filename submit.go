package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// SubmitCmd: `leetcode-cli submit <slug> -f solution.go`
func SubmitCmd(args []string) {
	EnsureEnv()
	solFile := "solution.go"
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-f" || a == "--f":
			if i+1 < len(args) {
				solFile = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "-f="):
			solFile = a[3:]
		case strings.HasPrefix(a, "--f="):
			solFile = a[5:]
		default:
			positional = append(positional, a)
		}
	}
	if len(positional) < 1 {
		emitErr("BAD_ARGS", "usage: leetcode-cli submit <slug> -f solution.go")
		return
	}
	slug := strings.ToLower(strings.TrimSpace(positional[0]))
	submit(slug, solFile)
}

type SubmitResult struct {
	Status          string         `json:"status"`         // ACCEPTED | WRONG_ANSWER | TIME_LIMIT_EXCEEDED | RUNTIME_ERROR | MEMORY_LIMIT_EXCEEDED | UNKNOWN
	RuntimeMs       int            `json:"runtime_ms,omitempty"`
	MemoryMB        float64        `json:"memory_mb,omitempty"`
	Passed          int            `json:"passed,omitempty"`
	Total           int            `json:"total,omitempty"`
	RuntimePercentile float64       `json:"runtime_percentile,omitempty"`
	FailedCase      *FailedCase    `json:"failed_case,omitempty"`
}

type FailedCase struct {
	Input     string `json:"input,omitempty"`
	Expected  string `json:"expected,omitempty"`
	Actual    string `json:"actual,omitempty"`
	Parseable bool   `json:"parseable"`
}

func submit(slug, solFile string) {
	c, err := NewClient()
	if err != nil {
		emitErr("SESSION_EXPIRED", err.Error())
		return
	}

	// Fetch question to get questionId and metaData
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

	// Determine the problem's language. Go-only path uses stripMainBoilerplate;
// SQL/other langs submit the raw file contents.
lang := "golang"
if q.GoStarterCode() == "" && q.SQLStarterCode() != "" {
	lang = "mysql"
}
if q.GoStarterCode() == "" && q.SQLStarterCode() == "" {
		emitErr("UNSOLVABLE", "no Go or SQL code snippet for this question")
		return
	}

	code, err := os.ReadFile(solFile)
	if err != nil {
		emitErr("NO_SOLUTION_FILE", err.Error())
		return
	}

	// For Go: strip `package main` and `func main` (LeetCode wants only the
	// function body in typed_code). For SQL: send the query verbatim.
	// Also strip // line comments from Go (keeps /* */ block comments — they
	// hold ListNode/TreeNode definitions).
	body := string(code)
	if lang == "golang" {
		body = stripMainBoilerplate(body)
		body = stripLineComments(body)
		// Sanity check: reject non-ASCII characters. LeetCode Go solutions are
		// pure ASCII; non-ASCII bytes here mean the model hallucinated garbage
		// (e.g. CJK characters replacing punctuation/identifiers).
		if loc := firstNonASCII(body); loc >= 0 {
			emitErr("INVALID_CODE", fmt.Sprintf("non-ASCII byte at offset %d (model hallucination?); first 80 bytes around it: %q", loc, around(body, loc, 80)))
			return
		}
	}

	subID, err := c.Submit(slug, q.QuestionId, lang, body)
	if err != nil {
		switch {
		case isErr(err, "SESSION_EXPIRED"):
			emitErr("SESSION_EXPIRED", err.Error())
		case isErr(err, "RATE_LIMITED"):
			retry := extractRetryAfter(err)
			emitErr("RATE_LIMITED", fmt.Sprintf("retry_after_seconds=%d", retry))
		case isErr(err, "SUBMIT_INCONCLUSIVE"):
			emitErr("SUBMIT_INCONCLUSIVE", "POST might have been accepted but result not collected; check leetcode.com submissions")
		default:
			emitErr("SUBMIT_FAILED", err.Error())
		}
		return
	}

	res, err := c.PollResult(subID, 30*time.Second)
	if err != nil {
		switch {
		case isErr(err, "SESSION_EXPIRED"):
			emitErr("SESSION_EXPIRED", err.Error())
		case isErr(err, "RATE_LIMITED"):
			emitErr("RATE_LIMITED", "retry_after_seconds=60")
		case isErr(err, "SUBMIT_INCONCLUSIVE"):
			emitErr("SUBMIT_INCONCLUSIVE", "poll timed out; check leetcode.com submissions")
		default:
			emitErr("POLL_FAILED", err.Error())
		}
		return
	}

	emitOK(buildSubmitResult(res, q))
}

// stripMainBoilerplate removes `package` lines, `func main()`, import blocks
// referencing third-party packages (halfrost/LeetCode-Go/structures), and
// type-alias declarations like `type ListNode = structures.ListNode` —
// LeetCode already provides these types.
func stripMainBoilerplate(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	skipMain := false
	braceDepth := 0
	inImportBlock := false
	for _, ln := range lines {
		trim := strings.TrimSpace(ln)
		if strings.HasPrefix(trim, "package ") {
			continue
		}
		// Detect import blocks (multi-line)
		if strings.HasPrefix(trim, "import ") {
			if strings.Contains(trim, "(") {
				inImportBlock = true
				continue
			}
			// single-line import — keep only stdlib
			imp := strings.TrimSuffix(strings.TrimPrefix(trim, "import "), "")
			imp = strings.Trim(imp, "\"")
			if !strings.Contains(imp, ".") || isStdlibImport(imp) {
				out = append(out, ln)
			}
			continue
		}
		if inImportBlock {
			if trim == ")" {
				inImportBlock = false
				continue
			}
			imp := strings.Trim(strings.TrimSpace(trim), "\"")
			if strings.Contains(imp, ".") && !isStdlibImport(imp) {
				continue
			}
			out = append(out, ln)
			continue
		}
		// Strip `type Foo = bar.Foo` alias lines (halfrost aliases ListNode etc.)
		if strings.HasPrefix(trim, "type ") && strings.Contains(trim, "=") &&
			(strings.Contains(trim, "structures.") || strings.Contains(trim, ".")) {
			// keep if it's a real type declaration with `struct{...}` body (has "struct{" pattern),
			// but strip if it's just a `type X = pkg.X` alias (no "struct{" in the line)
			if !strings.Contains(trim, "struct{") && !strings.Contains(trim, "struct {") {
				continue
			}
		}
		if strings.HasPrefix(trim, "func main()") || strings.HasPrefix(trim, "func main(") {
			skipMain = true
			braceDepth = 0
			for _, r := range ln {
				if r == '{' {
					braceDepth++
				} else if r == '}' {
					braceDepth--
				}
			}
			if braceDepth == 0 {
				skipMain = false
			}
			continue
		}
		if skipMain {
			for _, r := range ln {
				if r == '{' {
					braceDepth++
				} else if r == '}' {
					braceDepth--
				}
			}
			if braceDepth <= 0 {
				skipMain = false
			}
			continue
		}
		out = append(out, ln)
	}
	return strings.Join(out, "\n")
}

func isStdlibImport(imp string) bool {
	// Go stdlib imports have no dots in their first path segment
	// (e.g. "fmt", "strings", "strconv", "sort")
	// Third-party imports have dots ("github.com/...", "golang.org/x/...")
	if strings.Contains(imp, "github.com/") || strings.Contains(imp, "golang.org/") {
		return false
	}
	if strings.Contains(imp, ".") {
		return false
	}
	return true
}

func extractRetryAfter(err error) int {
	s := err.Error()
	if i := strings.Index(s, "retry_after="); i >= 0 {
		rest := s[i+len("retry_after="):]
		end := strings.IndexAny(rest, " ,)")
		if end < 0 {
			end = len(rest)
		}
		n := atoi(rest[:end])
		if n > 0 {
			return n
		}
	}
	return 60
}

// stripLineComments removes // ... comments from Go source while:
//   - preserving string literals ("...", '...', `...`) verbatim
//   - preserving /* ... */ block comments (they hold ListNode/TreeNode definitions)
//   - preserving the newline at end of stripped line so line numbers stay aligned
//     with the original file (helps debugging diff).
func stripLineComments(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inStr := byte(0) // 0 = outside string, otherwise holds the quote char
	inBlock := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inBlock {
			b.WriteByte(c)
			if c == '*' && i+1 < len(s) && s[i+1] == '/' {
				b.WriteByte(s[i+1])
				i++
				inBlock = false
			}
			continue
		}
		if inStr != 0 {
			b.WriteByte(c)
			if c == '\\' && i+1 < len(s) {
				b.WriteByte(s[i+1])
				i++
				continue
			}
			if c == inStr {
				inStr = 0
			}
			continue
		}
		// not in string or block comment
		if c == '"' || c == '\'' || c == '`' {
			inStr = c
			b.WriteByte(c)
			continue
		}
		if c == '/' && i+1 < len(s) && s[i+1] == '*' {
			inBlock = true
			b.WriteByte(c)
			b.WriteByte(s[i+1])
			i++
			continue
		}
		if c == '/' && i+1 < len(s) && s[i+1] == '/' {
			// skip to end of line (leave the newline itself)
			for i < len(s) && s[i] != '\n' {
				i++
			}
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// status codes (verified against LeetCode via leetgo/models.go):
//   10 Accepted, 11 WrongAnswer, 12 MemoryLimitExceeded,
//   13 OutputLimitExceeded, 14 TimeLimitExceeded, 15 RuntimeError,
//   20 CompileError
func buildSubmitResult(r *SubmissionResult, q *Question) SubmitResult {
	out := SubmitResult{
		RuntimeMs: r.ElapsedTime,
		Passed:   r.TotalCorrect,
		Total:    r.TotalTestcases,
	}
	if r.Memory > 0 {
		out.MemoryMB = float64(r.Memory) / (1024 * 1024)
	}
	if r.RuntimePercentile > 0 {
		out.RuntimePercentile = r.RuntimePercentile
	}
	switch r.StatusCode {
	case 10:
		out.Status = "ACCEPTED"
	case 11:
		out.Status = "WRONG_ANSWER"
		out.FailedCase = failedCase(r, q, true)
	case 12:
		out.Status = "MEMORY_LIMIT_EXCEEDED"
		out.FailedCase = failedCase(r, q, false)
	case 13:
		out.Status = "OUTPUT_LIMIT_EXCEEDED"
		out.FailedCase = failedCase(r, q, false)
	case 14:
		out.Status = "TIME_LIMIT_EXCEEDED"
		out.FailedCase = failedCase(r, q, true)
	case 15:
		out.Status = "RUNTIME_ERROR"
		out.FailedCase = failedCase(r, q, false)
	case 20:
		out.Status = "COMPILE_ERROR"
		out.FailedCase = &FailedCase{
			Input:     "—",
			Expected:  "—",
			Actual:    strings.TrimSpace(r.FullCompileError),
			Parseable: true,
		}
	default:
		out.Status = "UNKNOWN"
	}
	return out
}

// failedCase builds a FailedCase for the user-visible JSON, using CodeOutput
// (the actual solution's return value) — NOT StdOutput (stdout debug).
// Parseable iff every param + return type is in our supportedTypes list AND
// the caller marked the case parseable (true for WA/TLE where Input is real).
func failedCase(r *SubmissionResult, q *Question, parseable bool) *FailedCase {
	fc := &FailedCase{
		Input:     strings.TrimSpace(r.LastTestcase),
		Expected:  strings.TrimSpace(r.ExpectedOutput),
		Actual:    strings.TrimSpace(r.CodeOutput),
		Parseable: parseable,
	}
	var meta metaData
	if q.MetaData != "" {
		_ = json.Unmarshal([]byte(q.MetaData), &meta)
	}
	for _, p := range meta.Params {
		if !supportedTypes[p.Type] && !supportedTypes[aliasType(p.Type)] {
			fc.Parseable = false
			return fc
		}
	}
	if !supportedTypes[meta.Return.Type] && !supportedTypes[aliasType(meta.Return.Type)] {
		fc.Parseable = false
	}
	return fc
}

// firstNonASCII returns the byte offset of the first non-ASCII byte in s, or -1
// if all bytes are <= 0x7F. Used to catch model hallucinations where CJK
// characters leak into Go identifiers/punctuation.
func firstNonASCII(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] > 0x7F {
			return i
		}
	}
	return -1
}

// around returns up to `n` bytes of s centered around offset `at`, as a
// Go-quoted string for safe display in JSON error hints.
func around(s string, at, n int) string {
	start := at - n/2
	if start < 0 {
		start = 0
	}
	end := at + n/2
	if end > len(s) {
		end = len(s)
	}
	return s[start:end]
}