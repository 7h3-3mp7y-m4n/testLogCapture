package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// ansiEscape matches ANSI/VT100 escape sequences that GitHub logs sometimes
// embed (e.g. colour codes, cursor moves).  Strip them before any analysis.
var ansiEscape = regexp.MustCompile(`\x1b(?:[@-Z\\-_]|\[[0-?]*[ -/]*[@-~])`)

type Config struct {
	Settings struct {
		SourceRepo         string `yaml:"source_repo"`
		MaxRunsPerWorkflow int    `yaml:"max_runs_per_workflow"`
		RecentRunsInOutput int    `yaml:"recent_runs_in_output"`
	} `yaml:"settings"`

	Notify      NotifyConfig      `yaml:"notify"`
	LogAnalysis LogAnalysisConfig `yaml:"log_analysis"`

	Workflows []struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
		Critical    bool   `yaml:"critical"`
		Required    bool   `yaml:"required"`
	} `yaml:"workflows"`
}

type LogAnalysisConfig struct {
	MaxSignalsPerJob int                     `yaml:"max_signals_per_job"`
	NoisePatterns    []string                `yaml:"noise_patterns"`
	Categories       []FailureCategoryConfig `yaml:"categories"`
}

type FailureCategoryConfig struct {
	Name     string   `yaml:"name"`
	Priority int      `yaml:"priority"`
	Patterns []string `yaml:"patterns"`
}

type SignalLine struct {
	Line     string
	Category string
	Priority int
}

type LogSummary struct {
	Signals     []SignalLine
	ByCategory  map[string][]string
	TopCategory string
	Empty       bool
}

type Workflow struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}

type WorkflowsListResponse struct {
	Workflows []Workflow `json:"workflows"`
}

type WorkflowsResponse struct {
	WorkflowRuns []Run `json:"workflow_runs"`
}

type Step struct {
	Name        string    `json:"name"`
	Conclusion  string    `json:"conclusion"`
	Number      int       `json:"number"`
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"`
}

type FailedJob struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Conclusion string `json:"conclusion"`
	HTMLURL    string `json:"html_url"`
	LogSnippet string `json:"log_snippet"`
	RawLog     string `json:"raw_log"`
}

type Run struct {
	ID           int          `json:"id"`
	Name         string       `json:"name"`
	Status       string       `json:"status"`
	Conclusion   string       `json:"conclusion"`
	RunNumber    int          `json:"run_number"`
	CreatedAt    time.Time    `json:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
	RunStartedAt time.Time    `json:"run_started_at"`
	HTMLURL      string       `json:"html_url"`
	Jobs         []JobSummary `json:"jobs,omitempty"`
	FailedJobs   []FailedJob  `json:"failed_jobs,omitempty"`
	RunAttempt   int          `json:"run_attempt"`
}

type Job struct {
	ID          int       `json:"id"`
	Name        string    `json:"name"`
	Status      string    `json:"status"`
	Conclusion  string    `json:"conclusion"`
	HTMLURL     string    `json:"html_url"`
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"`
	Steps       []Step    `json:"steps"`
}

type JobSummary struct {
	Name        string  `json:"name"`
	Conclusion  string  `json:"conclusion"`
	DurationSec float64 `json:"duration_sec"`
	HTMLURL     string  `json:"html_url"`
}

type JobsResponse struct {
	Jobs []Job `json:"jobs"`
}

type WorkflowSummary struct {
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	Critical        bool     `json:"critical"`
	Required        bool     `json:"required"`
	TotalRuns       int      `json:"total_runs"`
	FailedRuns      int      `json:"failed_runs"`
	FailureRate     float64  `json:"failure_rate"`
	AvgDurationSecs float64  `json:"avg_duration_secs"`
	WeatherHistory  []string `json:"weather_history"`
	LastRun         *Run     `json:"last_run"`
	RecentRuns      []Run    `json:"recent_runs"`
}

type DashboardData struct {
	GeneratedAt   time.Time         `json:"generated_at"`
	Repo          string            `json:"repo"`
	OverallHealth float64           `json:"overall_health"`
	Workflows     []WorkflowSummary `json:"workflows"`
}

type Client struct {
	token       string
	repo        string
	httpClient  *http.Client
	logClient   *http.Client
	logAnalysis LogAnalysisConfig
}

func NewClient(token, repo string, la LogAnalysisConfig) *Client {
	return &Client{
		token:       token,
		repo:        repo,
		httpClient:  &http.Client{Timeout: 20 * time.Second},
		logClient:   &http.Client{Timeout: 2 * time.Minute},
		logAnalysis: la,
	}
}

// setAuthHeaders applies the standard GitHub API auth/accept headers to req.
// Centralises the two places this was previously duplicated inline.
func (c *Client) setAuthHeaders(req *http.Request) {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
}

func (c *Client) get(url string, v interface{}) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	c.setAuthHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("GitHub API returned HTTP %d for %s", resp.StatusCode, url)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func parseGHTimestamp(line string) (time.Time, error) {
	if len(line) < 28 {
		return time.Time{}, fmt.Errorf("line too short")
	}
	return time.Parse("2006-01-02T15:04:05.9999999Z", line[:28])
}

// stripGHTimestamp removes the leading GitHub log timestamp (first 29 chars)
// and then strips any ANSI escape sequences before trimming whitespace.
func stripGHTimestamp(line string) string {
	if len(line) > 29 && line[10] == 'T' {
		line = line[29:]
	}
	line = ansiEscape.ReplaceAllString(line, "")
	return strings.TrimSpace(line)
}

func analyseLog(scanner *bufio.Scanner, cfg LogAnalysisConfig) LogSummary {
	maxSignals := cfg.MaxSignalsPerJob
	if maxSignals <= 0 {
		maxSignals = 20
	}
	var signals []SignalLine
	seen := make(map[string]bool)
	for scanner.Scan() {
		if len(signals) >= maxSignals {
			break
		}
		clean := stripGHTimestamp(scanner.Text())
		if clean == "" {
			continue
		}
		lower := strings.ToLower(clean)
		if matchesAny(lower, cfg.NoisePatterns) {
			continue
		}
		for _, cat := range cfg.Categories {
			if matchesAny(lower, cat.Patterns) && !seen[clean] {
				seen[clean] = true
				signals = append(signals, SignalLine{
					Line:     clean,
					Category: cat.Name,
					Priority: cat.Priority,
				})
				break
			}
		}
	}

	if len(signals) == 0 {
		return LogSummary{Empty: true}
	}

	sort.SliceStable(signals, func(i, j int) bool {
		return signals[i].Priority < signals[j].Priority
	})
	topCategory := signals[0].Category

	byCategory := make(map[string][]string)
	for _, s := range signals {
		byCategory[s.Category] = append(byCategory[s.Category], s.Line)
	}

	return LogSummary{
		Signals:     signals,
		ByCategory:  byCategory,
		TopCategory: topCategory,
	}
}

func renderSummary(s LogSummary) string {
	if s.Empty {
		return "(no actionable failure signal found in log)"
	}

	total := len(s.Signals)
	var sb strings.Builder

	fmt.Fprintf(&sb, "[%s] — %d signal(s) found\n", s.TopCategory, total)
	sb.WriteString(strings.Repeat("─", 48) + "\n")

	seen := make(map[string]bool)
	var orderedCats []string
	for _, sig := range s.Signals {
		if !seen[sig.Category] {
			seen[sig.Category] = true
			orderedCats = append(orderedCats, sig.Category)
		}
	}

	for _, cat := range orderedCats {
		fmt.Fprintf(&sb, "-> %s\n", cat)
		for _, line := range s.ByCategory[cat] {
			fmt.Fprintf(&sb, "  %s\n", line)
		}
	}

	return strings.TrimRight(sb.String(), "\n")
}

func matchesAny(lower string, patterns []string) bool {
	for _, p := range patterns {
		if strings.Contains(lower, strings.ToLower(p)) {
			return true
		}
	}
	return false
}

func (c *Client) fetchAndAnalyseLog(logURL string, failedSteps []Step) (snippet, raw string, err error) {
	var resp *http.Response
	for attempt := 0; attempt < 2; attempt++ {
		req, _ := http.NewRequest("GET", logURL, nil)
		c.setAuthHeaders(req)
		resp, err = c.logClient.Do(req)
		if err == nil {
			break
		}
		if attempt == 0 {
			log.Printf("  log fetch attempt 1 failed (%v), retrying...", err)
			time.Sleep(3 * time.Second)
		}
	}
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return "", "", fmt.Errorf("log expired (404)")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("log fetch returned HTTP %d", resp.StatusCode)
	}

	var windowStart, windowEnd time.Time
	hasWindow := false
	for _, step := range failedSteps {
		if step.StartedAt.IsZero() {
			continue
		}
		s := step.StartedAt.Add(-time.Second)
		e := step.CompletedAt.Add(5 * time.Second)
		if !hasWindow {
			windowStart, windowEnd = s, e
			hasWindow = true
		} else {
			if s.Before(windowStart) {
				windowStart = s
			}
			if e.After(windowEnd) {
				windowEnd = e
			}
		}
	}

	if hasWindow {
		log.Printf("  window: %s → %s (%d failed step(s))",
			windowStart.Format(time.RFC3339),
			windowEnd.Format(time.RFC3339),
			len(failedSteps))
	} else {
		log.Printf("  no step timestamps — capturing full job log")
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)

	var sliced []string
	for scanner.Scan() {
		line := scanner.Text()
		if hasWindow {
			ts, parseErr := parseGHTimestamp(line)
			if parseErr != nil || ts.Before(windowStart) {
				continue
			}
			if ts.After(windowEnd) {
				break
			}
		}
		sliced = append(sliced, line)
	}
	if scanErr := scanner.Err(); scanErr != nil {
		return "", "", scanErr
	}

	log.Printf("  sliced: %d lines", len(sliced))

	if hasWindow && len(sliced) > 0 {
		preciseSignals := []string{
			"##[error]",
			"--- fail:",
			"fail!",
			"[fail]",
			"panic:",
			"✖",
			"timed out after",
		}
		var preciseEnd time.Time
		for _, line := range sliced {
			lower := strings.ToLower(stripGHTimestamp(line))
			for _, sig := range preciseSignals {
				if strings.Contains(lower, sig) {
					if ts, parseErr := parseGHTimestamp(line); parseErr == nil {
						preciseEnd = ts
					}
					break
				}
			}
		}
		if !preciseEnd.IsZero() {
			log.Printf("  precise end: %s", preciseEnd.Format("2006-01-02T15:04:05.9999999Z"))
			var precise []string
			for _, line := range sliced {
				ts, parseErr := parseGHTimestamp(line)
				if parseErr != nil {
					precise = append(precise, line)
					continue
				}
				if ts.After(preciseEnd) {
					break
				}
				precise = append(precise, line)
			}
			sliced = precise
		}
	}

	var captured []string
	for _, line := range sliced {
		clean := stripGHTimestamp(line)
		if clean == "" {
			continue
		}
		if matchesAny(strings.ToLower(clean), c.logAnalysis.NoisePatterns) {
			continue
		}
		captured = append(captured, clean)
	}
	log.Printf("  captured: %d lines after noise filter", len(captured))

	lineReader := strings.NewReader(strings.Join(captured, "\n"))
	summary := analyseLog(bufio.NewScanner(lineReader), c.logAnalysis)
	snippet = renderSummary(summary)
	raw = strings.Join(captured, "\n")
	if len(raw) > 50000 {
		raw = raw[:50000] + "\n\n[truncated at 50KB — see full log at: " + logURL + "]"
	}
	return snippet, raw, nil
}

func (c *Client) fetchAnnotationFallback(jobID int, htmlURL string) (snippet, raw string) {
	url := fmt.Sprintf(
		"https://api.github.com/repos/%s/check-runs/%d/annotations",
		c.repo, jobID)

	var annotations []struct {
		Level   string `json:"annotation_level"`
		Message string `json:"message"`
		Title   string `json:"title"`
	}

	if err := c.get(url, &annotations); err != nil {
		log.Printf("  annotation fallback failed for job %d: %v", jobID, err)
		snippet = "(log expired — no annotations available)"
		raw = fmt.Sprintf("(log expired — see: %s)", htmlURL)
		return
	}

	var failures []string
	for _, a := range annotations {
		if strings.EqualFold(a.Level, "failure") {
			msg := a.Message
			if a.Title != "" {
				msg = a.Title + ": " + msg
			}
			failures = append(failures, msg)
		}
	}

	if len(failures) == 0 {
		snippet = "(log expired — no failure annotations found)"
		raw = fmt.Sprintf("(log expired — see: %s)", htmlURL)
		return
	}

	snippet = strings.Join(failures, "\n")
	raw = fmt.Sprintf("(log expired — annotations only)\n\n%s\n\nSee: %s",
		snippet, htmlURL)
	return
}

func (c *Client) fetchJobsAndEnrich(run *Run) {
	url := fmt.Sprintf(
		"https://api.github.com/repos/%s/actions/runs/%d/jobs",
		c.repo, run.ID)
	var resp JobsResponse
	if err := c.get(url, &resp); err != nil {
		log.Printf("warn: could not fetch jobs for run %d: %v", run.ID, err)
		return
	}

	for _, job := range resp.Jobs {
		dur := job.CompletedAt.Sub(job.StartedAt).Seconds()
		if dur < 0 {
			dur = 0
		}
		run.Jobs = append(run.Jobs, JobSummary{
			Name:        job.Name,
			Conclusion:  job.Conclusion,
			DurationSec: dur,
			HTMLURL:     job.HTMLURL,
		})

		if job.Conclusion != "failure" {
			continue
		}

		var failedSteps []Step
		for _, step := range job.Steps {
			if step.Conclusion == "failure" {
				failedSteps = append(failedSteps, step)
				log.Printf("  failed step #%d %q | %s → %s",
					step.Number, step.Name,
					step.StartedAt.Format(time.RFC3339),
					step.CompletedAt.Format(time.RFC3339))
			}
		}

		if len(failedSteps) == 0 {
			log.Printf("  job %d (%s): no step-level data — will capture full job log",
				job.ID, job.Name)
		}

		logURL := fmt.Sprintf(
			"https://api.github.com/repos/%s/actions/jobs/%d/logs",
			c.repo, job.ID)
		log.Printf("fetching logs for failed job %d (%s)...", job.ID, job.Name)

		snippet, raw, fetchErr := c.fetchAndAnalyseLog(logURL, failedSteps)
		if fetchErr != nil {
			log.Printf("warn: log fetch failed for job %d: %v", job.ID, fetchErr)
			snippet, raw = c.fetchAnnotationFallback(job.ID, job.HTMLURL)
		}

		run.FailedJobs = append(run.FailedJobs, FailedJob{
			ID:         job.ID,
			Name:       job.Name,
			Conclusion: job.Conclusion,
			HTMLURL:    job.HTMLURL,
			LogSnippet: snippet,
			RawLog:     raw,
		})
	}
}

func normalize(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "-", "")
	s = strings.ReplaceAll(s, "_", "")
	s = strings.ReplaceAll(s, " ", "")
	return s
}

func stemPath(path string) string {
	parts := strings.Split(path, "/")
	file := parts[len(parts)-1]
	file = strings.TrimSuffix(file, ".yml")
	file = strings.TrimSuffix(file, ".yaml")
	return normalize(file)
}

func findWorkflow(workflows []Workflow, keyword string) *Workflow {
	key := normalize(keyword)
	for i, wf := range workflows {
		if stemPath(wf.Path) == key {
			return &workflows[i]
		}
	}
	for i, wf := range workflows {
		stem := stemPath(wf.Path)
		if strings.Contains(stem, key) || strings.Contains(key, stem) {
			return &workflows[i]
		}
	}
	for i, wf := range workflows {
		name := normalize(wf.Name)
		if strings.Contains(name, key) || strings.Contains(key, name) {
			return &workflows[i]
		}
	}
	return nil
}

func buildWeatherHistory(runs []Run) []string {
	const slots = 7
	history := make([]string, slots)
	for i := range history {
		history[i] = "unknown"
	}
	take := runs
	if len(take) > slots {
		take = runs[:slots]
	}

	offset := slots - len(take)
	for i, r := range take {
		idx := len(take) - 1 - i
		c := r.Conclusion
		if c == "" {
			c = "unknown"
		}
		switch c {
		case "success", "failure", "skipped", "action_required":
		default:
			c = "unknown"
		}
		history[offset+idx] = c
	}
	return history
}

func buildSummary(runs []Run, name, desc string, critical, required bool) WorkflowSummary {
	var failed int
	var totalDuration float64
	for _, r := range runs {
		if r.Conclusion == "failure" {
			failed++
		}
		start := r.RunStartedAt
		if start.IsZero() {
			start = r.CreatedAt
		}
		totalDuration += r.UpdatedAt.Sub(start).Seconds()
	}
	total := len(runs)
	var failureRate, avg float64
	if total > 0 {
		failureRate = float64(failed) / float64(total) * 100
		avg = totalDuration / float64(total)
	}
	var lastRun *Run
	if len(runs) > 0 {
		r := runs[0]
		lastRun = &r
	}
	return WorkflowSummary{
		Name:            name,
		Description:     desc,
		Critical:        critical,
		Required:        required,
		TotalRuns:       total,
		FailedRuns:      failed,
		FailureRate:     failureRate,
		AvgDurationSecs: avg,
		WeatherHistory:  buildWeatherHistory(runs),
		LastRun:         lastRun,
		RecentRuns:      runs,
	}
}

func main() {
	cfgBytes, err := os.ReadFile("config.yaml")
	if err != nil {
		log.Fatalf("cannot read config.yaml: %v", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(cfgBytes, &cfg); err != nil {
		log.Fatalf("cannot parse config.yaml: %v", err)
	}

	recentLimit := cfg.Settings.RecentRunsInOutput
	if recentLimit <= 0 {
		recentLimit = cfg.Settings.MaxRunsPerWorkflow
	}

	workflowsBytes, err := os.ReadFile("workflows_raw.json")
	if err != nil {
		log.Fatalf("cannot read workflows_raw.json: %v", err)
	}
	var workflowsResp WorkflowsListResponse
	if err := json.Unmarshal(workflowsBytes, &workflowsResp); err != nil {
		log.Fatalf("cannot parse workflows_raw.json: %v", err)
	}

	runsBytes, err := os.ReadFile("runs_raw.json")
	if err != nil {
		log.Fatalf("cannot read runs_raw.json: %v", err)
	}
	var allRuns map[string]WorkflowsResponse
	if err := json.Unmarshal(runsBytes, &allRuns); err != nil {
		log.Fatalf("cannot parse runs_raw.json: %v", err)
	}

	client := NewClient(os.Getenv("GH_PAT"), cfg.Settings.SourceRepo, cfg.LogAnalysis)

	var notifier *Notifier
	if cfg.Notify.Enabled {
		if os.Getenv("GITHUB_TOKEN") == "" {
			log.Println("warn: notify.enabled=true but GITHUB_TOKEN not set — skipping notifications")
		} else {
			notifier = NewNotifier(os.Getenv("GITHUB_TOKEN"), cfg.Settings.SourceRepo, cfg.Notify)
			log.Printf("Notifier enabled -> target: %s, label: %s",
				notifier.targetRepo, notifier.label)
		}
	}

	var summaries []WorkflowSummary
	var totalHealth float64

	for _, w := range cfg.Workflows {
		wf := findWorkflow(workflowsResp.Workflows, w.Name)
		if wf == nil {
			log.Println("Not found:", w.Name)
			continue
		}
		log.Printf("Matched: %s -> %s", w.Name, wf.Name)

		runsData, ok := allRuns[fmt.Sprintf("%d", wf.ID)]
		if !ok {
			log.Printf("warn: no runs found for workflow %s", w.Name)
			continue
		}
		runs := runsData.WorkflowRuns

		sort.Slice(runs, func(i, j int) bool {
			return runs[i].CreatedAt.After(runs[j].CreatedAt)
		})

		recent := runs
		if len(recent) > recentLimit {
			recent = runs[:recentLimit]
		}

		log.Printf("fetching jobs and logs for %s...", w.Name)
		for i := range recent {
			client.fetchJobsAndEnrich(&recent[i])
		}

		summary := buildSummary(runs, w.Name, w.Description, w.Critical, w.Required)
		summary.RecentRuns = recent

		if len(recent) > 0 && summary.LastRun != nil && recent[0].ID == summary.LastRun.ID {
			r := recent[0]
			summary.LastRun = &r
		}

		summaries = append(summaries, summary)
		totalHealth += (100 - summary.FailureRate)
		if notifier != nil {
			notifier.Process(summary)
		}
	}

	health := 0.0
	if len(summaries) > 0 {
		health = totalHealth / float64(len(summaries))
	}

	data := DashboardData{
		GeneratedAt:   time.Now().UTC(),
		Repo:          cfg.Settings.SourceRepo,
		OverallHealth: health,
		Workflows:     summaries,
	}

	out, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		log.Fatalf("cannot marshal stats: %v", err)
	}
	if err := os.WriteFile("stats.json", out, 0644); err != nil {
		log.Fatalf("cannot write stats.json: %v", err)
	}
}
