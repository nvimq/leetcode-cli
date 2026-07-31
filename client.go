package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"time"
)

// ---- errors ----

var (
	ErrSessionExpired     = errors.New("SESSION_EXPIRED")
	ErrRateLimited        = errors.New("RATE_LIMITED")
	ErrNotSolvable        = errors.New("UNSOLVABLE")
	ErrSubmitInconclusive = errors.New("SUBMIT_INCONCLUSIVE")
	ErrNotFound           = errors.New("NOT_FOUND")
	ErrNetwork            = errors.New("NETWORK_ERROR")
)

const (
	langGo     = "golang"
	langGoSlug = "golang"
)

// GraphQL query for questionData (verified against leetcode.com US site).
// LeetCode US exposes less fields than CN; the JSON variant only exists on CN.
const questionDataQuery = `query questionData($titleSlug: String!) {
  question(titleSlug: $titleSlug) {
    questionId
    questionFrontendId
    categoryTitle
    title
    titleSlug
    content
    translatedTitle
    translatedContent
    difficulty
    isPaidOnly
    sampleTestCase
    exampleTestcases
    exampleTestcaseList
    metaData
    codeSnippets { lang langSlug code }
    topicTags { name slug }
  }
}`

// GraphQL query for daily question (US)
const dailyQueryUS = `query questionOfToday {
  activeDailyCodingChallengeQuestion { question { titleSlug } }
}`

// ---- Client ----

type Client struct {
	baseURL    string
	http       *http.Client
	cookies    []*http.Cookie
	csrf       string
	userAgent  string
	maxRetries int
}

func NewClient() (*Client, error) {
	base := strings.TrimRight(os.Getenv("LEETCODE_BASE_URL"), "/")
	if base == "" {
		base = "https://leetcode.com"
	}
	if !strings.HasPrefix(base, "http") {
		base = "https://" + base
	}

	session := os.Getenv("LEETCODE_SESSION")
	csrf := os.Getenv("LEETCODE_CSRFTOKEN")
	if session == "" {
		return nil, fmt.Errorf("LEETCODE_SESSION not set")
	}

	jar, _ := cookiejar.New(nil)
	baseURL, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	cookies := []*http.Cookie{
		{Name: "LEETCODE_SESSION", Value: session, Domain: hostOf(base)},
	}
	if csrf != "" {
		cookies = append(cookies, &http.Cookie{Name: "csrftoken", Value: csrf, Domain: hostOf(base)})
	}
	cf := os.Getenv("LEETCODE_CFCLEARANCE")
	if cf != "" {
		cookies = append(cookies, &http.Cookie{Name: "cf_clearance", Value: cf, Domain: hostOf(base)})
	}
	jar.SetCookies(baseURL, cookies)

	c := &Client{
		baseURL:    base,
		http:       &http.Client{Jar: jar, Timeout: 30 * time.Second, CheckRedirect: checkRedirect},
		cookies:    cookies,
		csrf:       csrf,
		userAgent:  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36",
		maxRetries: 3,
	}
	return c, nil
}

// checkRedirect blocks redirects to login pages — those indicate SESSION_EXPIRED.
// All other redirects are followed normally.
func checkRedirect(req *http.Request, via []*http.Request) error {
	loc := req.URL.Path
	if strings.Contains(loc, "/accounts/login") ||
		strings.Contains(loc, "/login") ||
		strings.Contains(loc, "/account/login") {
		return http.ErrUseLastResponse
	}
	if len(via) >= 10 {
		return fmt.Errorf("stopped after 10 redirects")
	}
	return nil
}

// ---- request plumbing ----

// do executes a single HTTP request (no retry). Caller retries on transient errors.
func (c *Client) do(req *http.Request) (*http.Response, []byte, error) {
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("x-requested-with", "XMLHttpRequest")
	req.Header.Set("Referer", c.baseURL+"/")
	req.Header.Set("Origin", c.baseURL)
	if c.csrf != "" {
		req.Header.Set("x-csrftoken", c.csrf)
	}

	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		appendLog(req.URL.String(), 0, time.Since(start), err.Error())
		return nil, nil, fmt.Errorf("%w: %v", ErrNetwork, err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	appendLog(req.URL.String(), resp.StatusCode, time.Since(start), "")

	if ferr := c.detectFailure(resp, body); ferr != nil {
		return resp, body, ferr
	}
	return resp, body, nil
}

// detectFailure inspects HTTP response for session/cloudflare/rate-limit conditions.
func (c *Client) detectFailure(resp *http.Response, body []byte) error {
	if resp.StatusCode == 429 {
		retryAfter := 60
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if n := atoi(ra); n > 0 {
				retryAfter = n
			}
		}
		return fmt.Errorf("%w: retry_after=%d", ErrRateLimited, retryAfter)
	}
	if resp.StatusCode == 403 {
		if isCloudflareChallenge(resp, body) {
			writeStopMarker("cf_clearance expired (403 Cloudflare challenge)")
			return fmt.Errorf("%w: cf_clearance протухла, обнови LEETCODE_CFCLEARANCE в .env", ErrSessionExpired)
		}
		writeStopMarker("403 forbidden — cookies may have expired")
		return fmt.Errorf("%w: 403 forbidden — возможно куки протухли", ErrSessionExpired)
	}
	if resp.StatusCode == 302 || resp.StatusCode == 301 {
		loc := resp.Header.Get("Location")
		if strings.Contains(loc, "/accounts/login") || strings.Contains(loc, "login") {
			writeStopMarker("redirected to " + loc + " — LEETCODE_SESSION expired")
			return fmt.Errorf("%w: редирект на %s — LEETCODE_SESSION протухла", ErrSessionExpired, loc)
		}
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippet(body))
	}
	return nil
}

func isCloudflareChallenge(resp *http.Response, body []byte) bool {
	if h := resp.Header.Get("cf-mitigated"); strings.Contains(strings.ToLower(h), "challenge") {
		return true
	}
	if strings.Contains(string(body), "cf-chl-bypass") ||
		strings.Contains(string(body), "__cf_chl_") ||
		strings.Contains(string(body), "Just a moment") {
		return true
	}
	return false
}

func snippet(b []byte) string {
	s := string(b)
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}

// ---- GraphQL ----

type graphqlReq struct {
	Query         string                 `json:"query"`
	OperationName string                 `json:"operationName,omitempty"`
	Variables     map[string]interface{} `json:"variables,omitempty"`
}

func (c *Client) graphql(opName, query string, vars map[string]interface{}, result interface{}) error {
	body, _ := json.Marshal(graphqlReq{
		Query:         query,
		OperationName: opName,
		Variables:     vars,
	})
	var lastErr error
	for attempt := 0; attempt < c.maxRetries; attempt++ {
		req, err := http.NewRequest("POST", c.baseURL+"/graphql", bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, respBody, err := c.do(req)
		if err != nil {
			// non-retryable custom errors propagate immediately
			if errors.Is(err, ErrSessionExpired) || errors.Is(err, ErrRateLimited) || errors.Is(err, ErrNotSolvable) {
				return err
			}
			lastErr = err
			if attempt == c.maxRetries-1 {
				return err
			}
			backoff := time.Duration(1<<attempt) * time.Second
			time.Sleep(backoff)
			continue
		}
		_ = resp
		var errs struct {
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		_ = json.Unmarshal(respBody, &errs)
		if len(errs.Errors) > 0 {
			msg := errs.Errors[0].Message
			if strings.Contains(strings.ToLower(msg), "unauthenticated") ||
				strings.Contains(strings.ToLower(msg), "not authorized") {
				writeStopMarker("GraphQL: " + msg)
				return fmt.Errorf("%w: %s", ErrSessionExpired, msg)
			}
			return fmt.Errorf("graphql error: %s", msg)
		}
		return json.Unmarshal(respBody, result)
	}
	return fmt.Errorf("%w: %v", ErrNetwork, lastErr)
}

// ---- Question fetch ----

type Question struct {
	QuestionId          string `json:"questionId"`
	QuestionFrontendId  string `json:"questionFrontendId"`
	CategoryTitle       string `json:"categoryTitle"`
	Title               string `json:"title"`
	TitleSlug           string `json:"titleSlug"`
	Content             string `json:"content"`
	TranslatedContent   string `json:"translatedContent"`
	Difficulty          string `json:"difficulty"`
	IsPaidOnly          bool   `json:"isPaidOnly"`
	SampleTestCase      string `json:"sampleTestCase"`
	ExampleTestcases    string `json:"exampleTestcases"`
	ExampleTestcaseList []string `json:"exampleTestcaseList"`
	MetaData            string `json:"metaData"`
	CodeSnippets        []struct {
		Lang     string `json:"lang"`
		LangSlug string `json:"langSlug"`
		Code     string `json:"code"`
	} `json:"codeSnippets"`
}

// SQLStarterCode returns the MySQL code snippet (or "" if absent).
// Database problems on leetcode.com use langSlug "mysql" by default.
func (q *Question) SQLStarterCode() string {
	for _, sn := range q.CodeSnippets {
		if sn.LangSlug == "mysql" {
			return sn.Code
		}
	}
	return ""
}

// Category detects problem category: "algorithms" / "database" / ...
func (q *Question) Category() string {
	if q.CategoryTitle != "" {
		return strings.ToLower(q.CategoryTitle)
	}
	if q.SQLStarterCode() != "" {
		return "database"
	}
	return "algorithms"
}

type questionDataResp struct {
	Data struct {
		Question Question `json:"question"`
	} `json:"data"`
}

func (c *Client) GetQuestion(slug string) (*Question, error) {
	var r questionDataResp
	err := c.graphql("questionData", questionDataQuery, map[string]interface{}{
		"titleSlug": slug,
	}, &r)
	if err != nil {
		return nil, err
	}
	if r.Data.Question.TitleSlug == "" {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, slug)
	}
	return &r.Data.Question, nil
}

// GoStarterCode returns codeSnippets[langSlug=golang].code or "".
func (q *Question) GoStarterCode() string {
	for _, sn := range q.CodeSnippets {
		if sn.LangSlug == langGoSlug {
			return sn.Code
		}
	}
	return ""
}

// ---- Run code (local test not used, but submit needs questionId) ----

// ---- Submit ----

// LeetCode returns submission_id as either string or int depending on region/version.
// We accept both via json.Number.
type submitResp struct {
	SubmissionID json.Number `json:"submission_id"`
}

func (c *Client) Submit(slug, questionId, lang, code string) (string, error) {
	enforceSubmitCooldown()
	defer markSubmitTs()

	payload := map[string]interface{}{
		"lang":         lang,
		"questionSlug": slug,
		"question_id":  questionId,
		"typed_code":   code,
	}
	body, _ := json.Marshal(payload)

	path := fmt.Sprintf("/problems/%s/submit/", slug)
	// POST is NOT retried: non-idempotent.
	req, err := http.NewRequest("POST", c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	// Use a decoder that accepts both number and string for submission_id.
	resp, respBody, err := c.do(req)
	if err != nil {
		if errors.Is(err, ErrSessionExpired) || errors.Is(err, ErrRateLimited) {
			return "", err
		}
		return "", fmt.Errorf("%w: %v", ErrSubmitInconclusive, err)
	}
	_ = resp
	dec := json.NewDecoder(bytes.NewReader(respBody))
	dec.UseNumber()
	var sr submitResp
	if err := dec.Decode(&sr); err != nil || sr.SubmissionID.String() == "" || sr.SubmissionID.String() == "0" {
		return "", fmt.Errorf("%w: invalid submit response: %s", ErrSubmitInconclusive, snippet(respBody))
	}
	return sr.SubmissionID.String(), nil
}

// ---- Poll submission result ----
//
// Mirrors SubmitCheckResult from leetgo/models.go (verified against live API).
type SubmissionResult struct {
	CodeOutput           string  `json:"code_output"`        // user's wrong answer (return value)
	CompareResult        string  `json:"compare_result"`     // "111" — per-case pass bits
	ElapsedTime          int     `json:"elapsed_time"`
	ExpectedOutput       string  `json:"expected_output"`
	Finished             bool    `json:"finished"`
	Lang                 string  `json:"lang"`
	LastTestcase         string  `json:"last_testcase"`
	Memory               int     `json:"memory"`             // bytes
	MemoryPercentile     float64 `json:"memory_percentile"`
	RunSuccess           bool    `json:"run_success"`
	RuntimePercentile    float64 `json:"runtime_percentile"`
	State                string  `json:"state"`              // SUCCESS / STARTED / PENDING
	StatusCode           int     `json:"status_code"`
	StatusMemory         string  `json:"status_memory"`
	StatusMsg            string  `json:"status_msg"`
	StatusRuntime        string  `json:"status_runtime"`     // "4 ms" pretty string
	StdOutput            string  `json:"std_output"`
	TaskFinishTime       int     `json:"task_finish_time"`
	TotalCorrect         int     `json:"total_correct"`
	TotalTestcases       int     `json:"total_testcases"`
	CompileError         string  `json:"compile_error"`
	FullCompileError     string  `json:"full_compile_error"`
	FullRuntimeError     string  `json:"full_runtime_error"`
}

func (c *Client) PollResult(submissionId string, timeout time.Duration) (*SubmissionResult, error) {
	deadline := time.Now().Add(timeout)
	path := fmt.Sprintf("/submissions/detail/%s/check/", submissionId)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest("GET", c.baseURL+path, nil)
		resp, body, err := c.do(req)
		if err != nil {
			if errors.Is(err, ErrSessionExpired) || errors.Is(err, ErrRateLimited) {
				return nil, err
			}
			// transient network — backoff and retry within budget
			time.Sleep(2 * time.Second)
			continue
		}
		_ = resp
		var r SubmissionResult
		if err := json.Unmarshal(body, &r); err != nil {
			return nil, fmt.Errorf("parse poll response: %w", err)
		}
		if r.State == "SUCCESS" {
			return &r, nil
		}
		// jitter 1.0–2.0s to avoid lockstep polling pattern
		time.Sleep(time.Duration(1000+randSource.Int63n(1000)) * time.Millisecond)
	}
	return nil, fmt.Errorf("%w: poll timed out after %s", ErrSubmitInconclusive, timeout)
}

// ---- url helpers ----

func hostOf(base string) string {
	u, err := url.Parse(base)
	if err != nil {
		return ""
	}
	return u.Hostname()
}