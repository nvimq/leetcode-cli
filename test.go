package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// TestCmd: `leetcode-cli test <slug> -f solution.go`
//
// Loads question, generates a Go main package in a tmp dir with helpers + the
// user's solution + a generated test that calls the user's func with each
// example input, runs `go test -timeout 15s`, parses output, emits JSON.
func TestCmd(args []string) {
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
		emitErr("BAD_ARGS", "usage: leetcode-cli test <slug> -f solution.go")
		return
	}
	slug := strings.ToLower(strings.TrimSpace(positional[0]))
	runTest(slug, solFile)
}

type TestResult struct {
	Passed     int           `json:"passed"`
	Failed     int           `json:"failed"`
	CompileErr string        `json:"compile_err,omitempty"`
	Cases      []TestCaseOut `json:"cases"`
}

type TestCaseOut struct {
	Input    string `json:"input"`
	Expected string `json:"expected,omitempty"`
	Actual   string `json:"actual,omitempty"`
	Pass     bool   `json:"pass"`
}

func runTest(slug, solFile string) {
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

	var meta metaData
	if q.MetaData == "" {
		emitErr("NO_META", "missing metaData (SQL or interactive: skip local test, submit directly)")
		return
	}
	if err := json.Unmarshal([]byte(q.MetaData), &meta); err != nil {
		emitErr("BAD_META", err.Error())
		return
	}
	// Local test harness is Go-only. SQL/other langs → agent should skip to submit.
	if q.GoStarterCode() == "" {
		emitErr("UNSUPPORTED_TYPE", "no Go starter code — local test unavailable, submit directly")
		return
	}
	if !typesSupportedFor(&meta) {
		emitErr("UNSUPPORTED_TYPE", "atypes in metaData not covered by local test harness")
		return
	}

	solBytes, err := os.ReadFile(solFile)
	if err != nil {
		emitErr("NO_SOLUTION_FILE", err.Error())
		return
	}
	sol := ensurePackageMain(string(solBytes))

	examples := extractExamples(q.Content)
	// Prefer LeetCode's structured ExampleTestcaseList for local test parsing —
	// it's the same input format LeetCode uses internally ([2,7,11,15]\n9), not
	// the human-readable "Input: nums = ..." string.
	if len(q.ExampleTestcaseList) > 0 {
		examples = nil
		for i, tc := range q.ExampleTestcaseList {
			if tc == "" {
				continue
			}
			examples = append(examples, Example{Input: tc, Index: i + 1, IsRaw: true})
		}
	} else if len(examples) == 0 && q.SampleTestCase != "" {
		examples = append(examples, Example{Input: q.SampleTestCase, IsRaw: true})
	}
	if len(examples) == 0 {
		emitErr("NO_EXAMPLES", "no examples found to test against")
		return
	}

	dir, err := os.MkdirTemp("", "lc-test-")
	if err != nil {
		emitErr("IO", err.Error())
		return
	}
	defer func() {
		if os.Getenv("LC_DEBUG") == "" {
			os.RemoveAll(dir)
		}
	}()

	// go.mod (no external deps needed)
	_ = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module lctest\n\ngo 1.26\n"), 0o644)
	// helpers.go (embedded types + ParseTyped/SerializeTyped)
	if err := os.WriteFile(filepath.Join(dir, "helpers.go"), []byte(helpersSrc), 0o644); err != nil {
		emitErr("IO", err.Error())
		return
	}
	// solution.go
	if err := os.WriteFile(filepath.Join(dir, "solution.go"), []byte(sol), 0o644); err != nil {
		emitErr("IO", err.Error())
		return
	}
	// main_test.go generated
	testFile, err := genTestFile(meta, examples)
	if err != nil {
		emitErr("GEN_TEST", err.Error())
		return
	}
	if os.Getenv("LC_DEBUG") != "" {
		dbg("tmp dir: %s", dir)
		dbg("main_test.go:\n%s", testFile)
	}
	if err := os.WriteFile(filepath.Join(dir, "main_test.go"), []byte(testFile), 0o644); err != nil {
		emitErr("IO", err.Error())
		return
	}

	// compile only first — fast error surface
	compile := exec.Command("go", "build", "./")
	compile.Dir = dir
	var cstdout, cstderr bytes.Buffer
	compile.Stdout = &cstdout
	compile.Stderr = &cstderr
	if err := compile.Run(); err != nil {
		emitErr("COMPILE_FAILED", strings.TrimSpace(cstderr.String()))
		return
	}

	// run with hard 15s timeout and -v to enable per-case PASS/FAIL markers
	cmd := exec.Command("go", "test", "-v", "-timeout", "15s", "./")
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	done := make(chan error, 1)
	go func() { done <- cmd.Run() }()
	select {
	case err := <-done:
		emitTestResult(err, stdout.String(), stderr.String(), examples)
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		emitErr("TEST_TIMEOUT", "go test exceeded 20s wallclock")
	}
}

func emitTestResult(runErr error, stdout, stderr string, examples []Example) {
	out := stdout + "\n" + stderr
	res := &TestResult{}

	if strings.Contains(out, "panic: test timed out") || strings.Contains(out, "test timed out") {
		emitErr("TEST_TIMEOUT", "go test internal timeout (15s) hit")
		return
	}
	if strings.Contains(out, "panic:") {
		// extract panic
		res.CompileErr = "runtime panic: " + extractPanic(out)
		emitOK(res)
		return
	}

	// Parse PASS/FAIL tokens
	lines := strings.Split(stdout, "\n")
	var pass, fail int
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(ln, "--- PASS:") {
			pass++
		} else if strings.HasPrefix(ln, "--- FAIL:") {
			fail++
		}
	}

	for i, ex := range examples {
		// crude per-case pass map: if any FAIL seen overall mark first examples as failing
		c := TestCaseOut{
			Input:    ex.Input,
			Expected: ex.Output,
		}
		c.Pass = (fail == 0)
		if !c.Pass && i == 0 {
			// best-effort extracting actual from FAIL log
			c.Actual = strings.TrimSpace(strings.SplitN(out, "FAIL:", 2)[1])
		}
		res.Cases = append(res.Cases, c)
	}
	res.Passed = pass
	res.Failed = fail
	if runErr != nil && fail == 0 {
		// unknown error — surface minimally
		res.CompileErr = strings.TrimSpace(out)
	}
	emitOK(res)
}

func extractPanic(s string) string {
	i := strings.Index(s, "panic:")
	if i < 0 {
		return ""
	}
	rest := s[i:]
	if nl := strings.Index(rest, "\n\n"); nl >= 0 {
		rest = rest[:nl]
	}
	return rest
}

// ensurePackageMain prefixes `package main\n\n` if the solution doesn't already have a package decl.
func ensurePackageMain(sol string) string {
	if strings.HasPrefix(strings.TrimSpace(sol), "package ") {
		return sol
	}
	return "package main\n\n" + sol
}

func typesSupportedFor(m *metaData) bool {
	for _, p := range m.Params {
		if !supportedTypes[p.Type] && !supportedTypes[aliasType(p.Type)] {
			return false
		}
	}
	if !supportedTypes[m.Return.Type] && !supportedTypes[aliasType(m.Return.Type)] {
		return false
	}
	return true
}

// genTestFile emits main_test.go that runs the solution function once per example.
func genTestFile(meta metaData, examples []Example) (string, error) {
	if meta.Name == "" {
		return "", fmt.Errorf("meta.Name empty")
	}
	var b strings.Builder
	b.WriteString("package main\n\nimport \"testing\"\n\n")
	b.WriteString("func TestSolution(t *testing.T) {\n")
	for i, ex := range examples {
		args, err := splitExampleInput(ex.Input, meta)
		if err != nil {
			return "", err
		}
		var callArgs []string
		for j, p := range meta.Params {
			local := fmt.Sprintf("arg_%d_%d", i, j)
			raw := ""
			if j < len(args) {
				raw = args[j]
			}
			// escape backslash and quotes for embedding
			esc := strings.ReplaceAll(raw, `\`, `\\`)
			esc = strings.ReplaceAll(esc, `"`, `\"`)
			fmt.Fprintf(&b, "\t%s := ParseTyped(%q, %q).(%s)\n", local, p.Type, esc, goTypeFor(p.Type))
			callArgs = append(callArgs, local)
		}
		if meta.Return.Type == "void" || meta.Return.Type == "" {
			fmt.Fprintf(&b, "\t%s(%s)\n", meta.Name, strings.Join(callArgs, ", "))
			fmt.Fprintf(&b, "\tt.Logf(\"case %d ok\")\n", i)
			continue
		}
		fmt.Fprintf(&b, "\tgot_%d := %s(%s)\n", i, meta.Name, strings.Join(callArgs, ", "))
		fmt.Fprintf(&b, "\tt.Logf(\"case %d output = %s\", SerializeTyped(got_%d))\n", i, "%v", i)
	}
	b.WriteString("}\n")
	return b.String(), nil
}

// splitExampleInput splits the raw example input into per-arg strings.
// Rules:
//   - If input contains '\n' it's typically one arg per line.
//   - Otherwise, it's a single arg (single-value problems).
func splitExampleInput(input string, meta metaData) ([]string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("empty input")
	}
	n := len(meta.Params)
	if n == 0 {
		return nil, nil
	}
	if strings.Contains(input, "\n") {
		parts := strings.Split(input, "\n")
		// trim trailing empties / spaces
		var out []string
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" || len(out) < n {
				out = append(out, p)
			}
		}
		return out, nil
	}
	return []string{input}, nil
}

func goTypeFor(t string) string {
	switch t {
	case "integer":
		return "int"
	case "integer[]":
		return "[]int"
	case "integer[][]":
		return "[][]int"
	case "long":
		return "int"
	case "double", "float":
		return "float64"
	case "string":
		return "string"
	case "string[]":
		return "[]string"
	case "character":
		return "byte"
	case "character[]":
		return "[]byte"
	case "character[][]":
		return "[][]byte"
	case "boolean":
		return "bool"
	case "ListNode":
		return "*ListNode"
	case "ListNode[]":
		return "[]*ListNode"
	case "TreeNode":
		return "*TreeNode"
	}
	return "interface{}"
}