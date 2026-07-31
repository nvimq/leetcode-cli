package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// metaData from LeetCode looks like:
// {"name":"twoSum","params":[{"name":"nums","type":"integer[]"},{"name":"target","type":"integer"}],"return":{"type":"integer[]"},"manual":false}

type metaData struct {
	Name   string `json:"name"`
	Params []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"params"`
	Return struct {
		Type string `json:"type"`
	} `json:"return"`
	Manual bool `json:"manual"`
}

// CmpLine normalises LeetCode api type names to our Go-ised name table.
var typeAlias = map[string]string{
	"integer":          "int",
	"integer[]":        "int[]",
	"integer[][]":      "int[][]",
	"long":              "int",
	"double":            "float",
	"float":            "float",
	"string":           "string",
	"string[]":        "string[]",
	"character":        "char",
	"character[]":      "char[]",
	"boolean":          "boolean",
	"ListNode":         "ListNode",
	"ListNode[]":       "ListNode[]",
	"TreeNode":         "TreeNode",
	"void":             "void",
}

// supportedTypes is the white-list for local test parsing.
var supportedTypes = map[string]bool{
	"int":          true,
	"int[]":        true,
	"int[][]":      true,
	"float":        true,
	"string":       true,
	"string[]":     true,
	"char":         true,
	"char[]":       true,
	"char[][]":     true,
	"boolean":      true,
	"ListNode":     true,
	"ListNode[]":   true,
	"TreeNode":     true,
	"integer":      true,
	"integer[]":    true,
	"integer[][]":  true,
	"long":         true,
	"double":       true,
	"character":    true,
	"character[]":  true,
	"character[][]":true,
}

// FetchCmd executes `leetcode-cli fetch <slug>`.
func FetchCmd(args []string) {
	EnsureEnv()
	fs := flag.NewFlagSet("fetch", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return
	}
	if fs.NArg() < 1 {
		emitErr("BAD_ARGS", "usage: leetcode-cli fetch <slug>")
		return
	}
	slug := strings.ToLower(strings.TrimSpace(fs.Arg(0)))

	c, err := NewClient()
	if err != nil {
		emitErr("SESSION_EXPIRED", err.Error())
		return
	}

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

	// Parse metaData
	var meta metaData
	var metaParseErr error
	if q.MetaData != "" {
		_ = json.Unmarshal([]byte(q.MetaData), &meta)
	} else {
		metaParseErr = fmt.Errorf("empty metaData")
	}

	// Determine Go starter
	goStarter := q.GoStarterCode()
	sqlStarter := q.SQLStarterCode()
	lang := "go"
	starter := goStarter
	category := q.Category()
	if goStarter == "" && (sqlStarter != "" || category == "database") {
		// SQL problems on leetcode.com often have no starter code snippet —
		// the user writes a bare query. The categoryTitle "database" is the signal.
		lang = "mysql"
		starter = sqlStarter // may be "" — that's fine for SQL
	}
	isSolvable := true
	reason := ""
	if q.IsPaidOnly && q.Content == "" && q.TranslatedContent == "" {
		// Premium-gated question with no content — can't see the problem statement.
		isSolvable = false
		reason = "paid_only_locked"
		lang = ""
		starter = ""
	} else if starter == "" && lang == "go" {
		isSolvable = false
		reason = "no_go_or_sql_snippet"
	}

	// Build description (HTML -> markdown)
	descHTML := q.Content
	if descHTML == "" && q.TranslatedContent != "" {
		descHTML = q.TranslatedContent
	}
	description := ""
	if descHTML != "" {
		description = HTMLToMarkdown(descHTML)
	}

	// Constraints: LeetCode inline constraints in description as <ul><li>...</li></ul>.
	// We pull them out of HTML using a simple regex (best-effort).
	constraints := extractConstraints(descHTML)

	// Examples: prefer exampleTestcaseList (clean split) — but that's only testcases, not full example text.
	// For human-readable examples, parse <pre> blocks from description.
	examples := extractExamples(descHTML)
	if len(examples) == 0 && q.ExampleTestcaseList != nil {
		// fallback: build minimal examples from raw testcase data
		for i, tc := range q.ExampleTestcaseList {
			if tc == "" {
				continue
			}
			examples = append(examples, Example{
				Input:    tc,
				Output:   "",
				Explain:  "",
				Index:    i + 1,
				IsRaw:    true,
			})
		}
	}

	// go_signature: derive from starter code via regex (func Name(...)
	goSig := extractFirstFuncSig(goStarter)
	if lang != "go" {
		goSig = ""
	}

	// Are types supported for local test? Only Go has a local test harness at all.
	typesSupported := lang == "go"
	missingType := ""
	if typesSupported && metaParseErr == nil {
		for _, p := range meta.Params {
			if !supportedTypes[p.Type] && !supportedTypes[aliasType(p.Type)] {
				typesSupported = false
				missingType = p.Type
			}
		}
		if !supportedTypes[meta.Return.Type] && !supportedTypes[aliasType(meta.Return.Type)] {
			typesSupported = false
			missingType = meta.Return.Type
		}
	}

	resp := FetchResp{
		Slug:             q.TitleSlug,
		ID:               q.QuestionId,
		FrontendID:       q.QuestionFrontendId,
		Title:            q.Title,
		Difficulty:       q.Difficulty,
		Description:      description,
		Constraints:      constraints,
		Examples:         examples,
		Lang:             lang,
		Category:         category,
		StarterCode:      starter,
		GoSignature:      goSig,
		Meta:             meta,
		IsSolvable:       isSolvable,
		Reason:           reason,
		IsPaidOnly:       q.IsPaidOnly,
		TypesSupported:   typesSupported,
		MissingType:      missingType,
	}

	emitOK(resp)

	// Record STARTED in progress.md
	_ = appendProgress(q.TitleSlug, "STARTED", 0)
	_ = os.Stderr.Sync()
}

// ---- types ----

type Example struct {
	Input   string `json:"input"`
	Output  string `json:"output"`
	Explain string `json:"explanation,omitempty"`
	Index   int    `json:"index,omitempty"`
	IsRaw   bool   `json:"is_raw,omitempty"`
}

type FetchResp struct {
	Slug           string    `json:"slug"`
	ID             string    `json:"id"`
	FrontendID     string    `json:"frontend_id"`
	Title          string    `json:"title"`
	Difficulty     string    `json:"difficulty"`
	Description    string    `json:"description"`
	Constraints    []string  `json:"constraints"`
	Examples       []Example `json:"examples"`
	Lang           string    `json:"lang"`            // "go" | "mysql" | ...
	Category       string    `json:"category"`       // "algorithms" | "database" | ...
	StarterCode    string    `json:"starter_code"`
	GoSignature    string    `json:"go_signature,omitempty"`
	Meta           metaData  `json:"meta"`
	IsSolvable     bool      `json:"is_solvable"`
	Reason         string    `json:"reason,omitempty"`
	IsPaidOnly     bool      `json:"is_paid_only"`
	TypesSupported bool      `json:"types_supported"`
	MissingType    string    `json:"missing_type,omitempty"`
}

// ---- helpers ----

func isErr(err error, code string) bool {
	return strings.Contains(err.Error(), code)
}

func aliasType(t string) string {
	if v, ok := typeAlias[t]; ok {
		return v
	}
	return t
}

var funcSigRe = regexp.MustCompile(`(?m)^(?:func\s+)?(?:\([^)]*\)\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*\(`)

func extractFirstFuncSig(code string) string {
	lines := strings.Split(code, "\n")
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(ln, "func ") {
			// capture up to first ')'
			if i := strings.Index(ln, ")"); i >= 0 {
				return ln[:i+1]
			}
			return ln
		}
	}
	return ""
}

var preBlockRe = regexp.MustCompile(`(?s)<pre[^>]*>(.*?)</pre>`)
var tagRe = regexp.MustCompile(`<[^>]+>`)
var exampleHeaderRe = regexp.MustCompile(`(?i)<strong>Example\s*(\d+)?:?</strong>|<b>Example\s*(\d+)?:?</b>`)

func extractExamples(html string) []Example {
	if html == "" {
		return nil
	}
	// split on Example headers
	_ = exampleHeaderRe.Split(html, -1) // unused; we iterate via headers below
	headers := exampleHeaderRe.FindAllStringSubmatchIndex(html, -1)
	if len(headers) == 0 {
		// no headers — try to pull <pre> blocks
		pres := preBlockRe.FindAllStringSubmatch(html, -1)
		var out []Example
		for i, m := range pres {
			out = append(out, Example{Input: strings.TrimSpace(stripHTMLPres(m[1])), Index: i + 1, IsRaw: true})
		}
		return out
	}
	var out []Example
	for i, h := range headers {
		// chunk content from end of this header to start of next (or end)
		start := h[1]
		end := len(html)
		if i+1 < len(headers) {
			end = headers[i+1][0]
		}
		block := html[start:end]
		// Find <pre> if any; extract Input/Output
		var ex Example
		ex.Index = i + 1
		if pres := preBlockRe.FindStringSubmatch(block); pres != nil {
			inner := stripHTMLPres(pres[1])
			lines := strings.Split(inner, "\n")
			var inB, outB, expB strings.Builder
			section := "input"
			for _, ln := range lines {
				lo := strings.ToLower(strings.TrimSpace(ln))
				switch {
				case strings.HasPrefix(lo, "input:"):
					section = "input"
					rest := strings.TrimSpace(strings.TrimPrefix(lo, "input:"))
					if rest != "" {
						inB.WriteString(rest)
					}
					continue
				case strings.HasPrefix(lo, "output:"):
					section = "output"
					rest := strings.TrimSpace(strings.TrimPrefix(lo, "output:"))
					if rest != "" {
						outB.WriteString(rest)
					}
					continue
				case strings.HasPrefix(lo, "explanation:") || strings.HasPrefix(lo, "explain:"):
					section = "explain"
					rest := strings.TrimSpace(strings.TrimPrefix(lo, "explanation:"))
					rest = strings.TrimSpace(strings.TrimPrefix(rest, "explain:"))
					if rest != "" {
						expB.WriteString(rest)
					}
					continue
				}
				switch section {
				case "input":
					if inB.Len() > 0 {
						inB.WriteString("\n")
					}
					inB.WriteString(strings.TrimSpace(ln))
				case "output":
					if outB.Len() > 0 {
						outB.WriteString("\n")
					}
					outB.WriteString(strings.TrimSpace(ln))
				case "explain":
					if expB.Len() > 0 {
						expB.WriteString("\n")
					}
					expB.WriteString(strings.TrimSpace(ln))
				}
			}
			ex.Input = strings.TrimSpace(inB.String())
			ex.Output = strings.TrimSpace(outB.String())
			ex.Explain = strings.TrimSpace(expB.String())
		} else {
			// no pre block — just grab raw block text
			ex.Input = strings.TrimSpace(stripHTML(block))
		}
		out = append(out, ex)
	}
	return out
}

func stripHTMLPres(s string) string {
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&quot;", "\"")
	return strings.TrimSpace(tagRe.ReplaceAllString(s, ""))
}

func stripHTML(s string) string {
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&quot;", "\"")
	return tagRe.ReplaceAllString(s, "")
}

var constraintsListRe = regexp.MustCompile(`(?s)<li[^>]*>(.*?)</li>`)

// extractConstraints looks specifically under a <ul><li> block that follows
// <strong>Constraints</strong> / Constraints:
func extractConstraints(html string) []string {
	if html == "" {
		return nil
	}
	idx := strings.Index(strings.ToLower(html), "constraint")
	if idx < 0 {
		return nil
	}
	tail := html[idx:]
	// find first <ul>
	ulStart := strings.Index(tail, "<ul")
	if ulStart < 0 {
		// fallback: look for <li>
		ulStart = strings.Index(tail, "<li")
	}
	if ulStart < 0 {
		return nil
	}
	tail = tail[ulStart:]
	matches := constraintsListRe.FindAllStringSubmatch(tail, -1)
	var out []string
	for _, m := range matches {
		s := stripHTMLPres(m[1])
		if s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Avoid unused import.
var _ = fmt.Sprintf