package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
)

// SolutionsCmd: `leetcode-cli solutions <slug>`
//
// Fetches a ready-made Go solution for the given problem from a community
// repository (halfrost/LeetCode-Go — 798+ problems solved in Go) or, for
// SQL problems, from LeetCode's editorial solutions via GraphQL. Returns JSON
// with the solution code. The agent doesn't write any code — it just calls
// solve which fetches this and submits.
func SolutionsCmd(args []string) {
	EnsureEnv()
	if len(args) < 1 {
		emitErr("BAD_ARGS", "usage: leetcode-cli solutions <slug>")
		return
	}
	slug := strings.ToLower(strings.TrimSpace(args[0]))

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

	langTag := "golang"
	if q.GoStarterCode() == "" && (q.SQLStarterCode() != "" || q.Category() == "database") {
		langTag = "mysql"
	}

	if langTag == "golang" {
		code, err := fetchGitHubSolution(q)
		if err != nil {
			emitErr("NO_SOLUTIONS", err.Error())
			return
		}
		emitOK(map[string]interface{}{
			"slug":    slug,
			"lang":    "golang",
			"code":    code,
			"source":  "github.com/halfrost/LeetCode-Go",
		})
		return
	}

	// SQL fallback — LeetCode editorial content is often null for free users.
	emitErr("NO_SOLUTIONS", "SQL solutions not available via community repo; agent should solve manually")
}

// fetchGitHubSolution downloads the Go solution from halfrost/LeetCode-Go
// based on the question's frontend ID and title. The repo organises folders as
// "NNNN.Title-With-Dashes" and files as "N. Title.go".
func fetchGitHubSolution(q *Question) (string, error) {
	id := strings.TrimPrefix(q.QuestionFrontendId, "")
	if id == "" {
		return "", fmt.Errorf("empty frontend ID")
	}
	padded := padLeft(id, 4, "0")
	// Build folder name like "0036.Valid-Sudoku"
	titleDashed := strings.ReplaceAll(q.Title, " ", "-")
	folder := padded + "." + titleDashed
	// Build file name like "36. Valid Sudoku.go"
	fileName := fmt.Sprintf("%s. %s.go", id, q.Title)
	rawURL := "https://raw.githubusercontent.com/halfrost/LeetCode-Go/master/leetcode/" +
		folder + "/" + fileName
	encodedURL := "https://raw.githubusercontent.com/halfrost/LeetCode-Go/master/leetcode/" +
		folder + "/" + url.PathEscape(fileName)

	// Try URL-encoded first (handles spaces), fall back to %20
	resp, err := http.Get(encodedURL)
	if err != nil || resp.StatusCode != 200 {
		rawWithSpaces := "https://raw.githubusercontent.com/halfrost/LeetCode-Go/master/leetcode/" +
			folder + "/" + strings.ReplaceAll(fileName, " ", "%20")
		if resp != nil {
			resp.Body.Close()
		}
		resp, err = http.Get(rawWithSpaces)
		if err != nil || resp.StatusCode != 200 {
			if resp != nil {
				resp.Body.Close()
			}
			return "", fmt.Errorf("solution not found in LeetCode-Go repo (tried: %s)", rawURL)
		}
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read solution body: %w", err)
	}
	return string(b), nil
}

func padLeft(s string, width int, pad string) string {
	for len(s) < width {
		s = pad + s
	}
	return s
}

// ---- kept for backwards compat with solve.go ----

type solutionItem struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Content     string `json:"content"`
	UpvoteCount int    `json:"upvoteCount"`
	Author      string `json:"author"`
}

type solutionsResp struct {
	Data struct {
		SolutionList struct {
			Solutions []struct {
				ID          string `json:"id"`
				Title       string `json:"title"`
				Content     string `json:"content"`
				UpvoteCount int    `json:"upvoteCount"`
				Author      struct {
					Username string `json:"username"`
				} `json:"author"`
			} `json:"solutions"`
		} `json:"solutionList"`
	} `json:"data"`
}

const solutionsQuery = `query solutionList($questionSlug: String!, $first: Int, $skip: Int, $languageTags: [String], $topicTags: [String]) {
  solutionList(questionSlug: $questionSlug, first: $first, skip: $skip, languageTags: $languageTags, topicTags: $topicTags, keyword: "") {
    solutionNum
    solutions {
      id
      title
      content
      upvoteCount
      author { username }
    }
  }
}`

func (c *Client) GetSolutions(slug, langTag string, limit int) ([]solutionItem, error) {
	vars := map[string]interface{}{
		"questionSlug": slug,
		"first":        limit,
		"skip":          0,
		"languageTags": []string{langTag},
		"topicTags":    []string{},
	}
	var r solutionsResp
	if err := c.graphql("solutionList", solutionsQuery, vars, &r); err != nil {
		return nil, err
	}
	var out []solutionItem
	for _, s := range r.Data.SolutionList.Solutions {
		out = append(out, solutionItem{
			ID:          s.ID,
			Title:       s.Title,
			Content:     s.Content,
			UpvoteCount: s.UpvoteCount,
			Author:      s.Author.Username,
		})
	}
	return out, nil
}

// ---- HTML code extraction ----

var (
	codeBlockRe2 = regexp.MustCompile(`(?s)<code[^>]*>(.*?)</code>`)
	langClassRe  = regexp.MustCompile(`(?i)language-(go|golang|mysql|sql)`)
)
var anyTagReSolutions = regexp.MustCompile(`<[^>]+>`)

// extractCodeFromHTML pulls Go/SQL code out of a LeetCode community solution's HTML.
func extractCodeFromHTMLSol(html, langTag string) string {
	targetLang := langTag
	if langTag == "golang" {
		targetLang = "go"
	}
	preMatches := preBlockRe.FindAllStringSubmatch(html, -1)
	if len(preMatches) == 0 {
		codeMatches := codeBlockRe2.FindAllStringSubmatch(html, -1)
		if len(codeMatches) == 0 {
			return ""
		}
		for _, m := range codeMatches {
			langMatch := langClassRe.FindString(m[0])
			if strings.Contains(strings.ToLower(langMatch), targetLang) {
				return cleanCodeSolutions(m[1])
			}
		}
		return cleanCodeSolutions(codeMatches[0][1])
	}
	for _, m := range preMatches {
		fullBlock := m[0]
		if langClassRe.MatchString(fullBlock) {
			langMatch := langClassRe.FindString(fullBlock)
			if strings.Contains(strings.ToLower(langMatch), targetLang) {
				codeMatch := codeBlockRe2.FindStringSubmatch(m[1])
				if codeMatch != nil {
					return cleanCodeSolutions(codeMatch[1])
				}
				return cleanCodeSolutions(m[1])
			}
		}
	}
	first := preMatches[0][1]
	codeMatch := codeBlockRe2.FindStringSubmatch(first)
	if codeMatch != nil {
		return cleanCodeSolutions(codeMatch[1])
	}
	return cleanCodeSolutions(first)
}

func cleanCodeSolutions(s string) string {
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&quot;", "\"")
	s = strings.ReplaceAll(s, "&#39;", "'")
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	s = anyTagReSolutions.ReplaceAllString(s, "")
	s = strings.TrimSpace(s)
	lines := strings.Split(s, "\n")
	var out []string
	for _, ln := range lines {
		out = append(out, strings.TrimRight(ln, " \t\r"))
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// extractCodeFromHTML pulls Go/SQL code out of a LeetCode community solution's HTML.
func extractCodeFromHTML(html, langTag string) string {
	return extractCodeFromHTMLSol(html, langTag)
}

// silence unused
var _ = fmt.Sprintf
var _ = os.Args