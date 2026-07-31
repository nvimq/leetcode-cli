package main

import (
	"fmt"
	"os"
)

const usage = `leetcode-cli - LeetCode solver agent helper

usage: leetcode-cli <command> [args]

commands:
  worklist   [flags]
            Generate worklist.md from your unsolved problems.
  fetch <slug>
            Fetch question details as JSON.
  test <slug> -f solution.go
            Run Go solution locally (15s timeout).
  submit <slug> -f solution.go
            Submit solution to LeetCode.
  solutions <slug>
            Fetch the top community solution (Go/SQL) as JSON.
  solve <slug>
            Auto-solve: fetch community solution + submit. Full pipeline in one command.
  progress <slug> <status> [attempts]
            Append to progress.md.

stdout always emits JSON: {"ok":true,"data":{...}} or {"ok":false,"error":"...", "hint":"..."}
stderr is for human debug logs.

environment (.env auto-loaded from cwd or ~/.leetcode-cli/.env):
  LEETCODE_SESSION     (required)
  LEETCODE_CSRFTOKEN   (recommended)
  LEETCODE_CFCLEARANCE  (required for leetcode.com Cloudflare bypass)
  LEETCODE_BASE_URL    (default https://leetcode.com)
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]
	switch cmd {
	case "worklist":
		WorklistCmd(args)
	case "fetch":
		FetchCmd(args)
	case "test":
		TestCmd(args)
	case "submit":
		SubmitCmd(args)
	case "solutions":
		SolutionsCmd(args)
	case "solve":
		SolveCmd(args)
	case "progress":
		ProgressCmd(args)
	case "-h", "--help", "help":
		fmt.Fprint(os.Stderr, usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n%s", cmd, usage)
		os.Exit(2)
	}
}