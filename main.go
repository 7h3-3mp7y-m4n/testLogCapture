package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"
)

type Config struct {
	Settings struct {
		SourceRepo         string `yaml:"source_repo"`
		MaxRunsPerWorkflow int    `yaml:"max_runs_per_workflow"`
		RecentRunsInOutput int    `yaml:"recent_runs_in_output"`
		MaxLogConcurrency  int    `yaml:"max_log_concurrency"`
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
	http        *http.Client
	logHTTP     *http.Client
	logAnalysis LogAnalysisConfig
	logSem      chan struct{} // semaphore: caps concurrent log fetches
}

func NewClient(token, repo string, la LogAnalysisConfig, maxLogConcurrency int) *Client {
	if maxLogConcurrency <= 0 {
		maxLogConcurrency = 5
	}
	return &Client{
		token:       token,
		repo:        repo,
		http:        &http.Client{Timeout: 20 * time.Second},
		logHTTP:     &http.Client{Timeout: 2 * time.Minute},
		logAnalysis: la,
		logSem:      make(chan struct{}, maxLogConcurrency),
	}
}

func (c *Client) get(url string, v interface{}) error {
	req, _ := http.NewRequest("GET", url, nil)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
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
	byCategory := make(map[string][]string)
	topPriority := signals[0].Priority
	topCategory := signals[0].Category
	for _, s := range signals {
		byCategory[s.Category] = append(byCategory[s.Category], s.Line)
		if s.Priority < topPriority {
			topPriority = s.Priority
			topCategory = s.Category
		}
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

func (c *Client) fetchAndAnalyseLog(logURL string) (string, string, error) {
	// Acquire semaphore slot before making the long-lived request.
	c.logSem <- struct{}{}
	defer func() { <-c.logSem }()

	var resp *http.Response
	var err error

	for attempt := 0; attempt < 2; attempt++ {
		req, _ := http.NewRequest("GET", logURL, nil)
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		resp, err = c.logHTTP.Do(req)
		if err == nil {
			break
		}
		if attempt == 0 {
			log.Printf("log fetch attempt 1 failed (%v), retrying...", err)
			time.Sleep(3 * time.Second)
		}
	}
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	const maxRawBytes = 64 * 1024
	var rawBuf strings.Builder
	truncated := false
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)
	var allLines []string
	for scanner.Scan() {
		allLines = append(allLines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return "", "", err
	}
	lineReader := strings.NewReader(strings.Join(allLines, "\n"))
	summary := analyseLog(bufio.NewScanner(lineReader), c.logAnalysis)
	for _, line := range allLines {
		clean := stripGHTimestamp(line)
		if rawBuf.Len()+len(clean)+1 > maxRawBytes {
			truncated = true
			break
		}
		rawBuf.WriteString(clean)
		rawBuf.WriteByte('\n')
	}
	raw := strings.TrimRight(rawBuf.String(), "\n")
	if truncated {
		raw += "\n\n[log truncated at 64KB]"
	}

	return renderSummary(summary), raw, nil
}

// fetchJobSummaries populates run.Jobs. Safe to call concurrently for different runs.
func (c *Client) fetchJobSummaries(run *Run) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/actions/runs/%d/jobs", c.repo, run.ID)
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
	}
}

// enrichWithLogs fetches logs for every failed job in a run concurrently,
// bounded by the client's semaphore.
func (c *Client) enrichWithLogs(run *Run) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/actions/runs/%d/jobs", c.repo, run.ID)
	var resp JobsResponse
	if err := c.get(url, &resp); err != nil {
		log.Printf("warn: could not fetch jobs for run %d: %v", run.ID, err)
		return
	}

	type result struct {
		job     Job
		snippet string
		raw     string
		err     error
	}

	var failedJobs []Job
	for _, job := range resp.Jobs {
		if job.Conclusion == "failure" {
			failedJobs = append(failedJobs, job)
		}
	}
	if len(failedJobs) == 0 {
		return
	}

	results := make([]result, len(failedJobs))
	var wg sync.WaitGroup
	for i, job := range failedJobs {
		wg.Add(1)
		go func(i int, job Job) {
			defer wg.Done()
			logURL := fmt.Sprintf("https://api.github.com/repos/%s/actions/jobs/%d/logs", c.repo, job.ID)
			log.Printf("fetching logs for failed job %d (%s)...", job.ID, job.Name)
			snippet, raw, err := c.fetchAndAnalyseLog(logURL)
			results[i] = result{job: job, snippet: snippet, raw: raw, err: err}
		}(i, job)
	}
	wg.Wait()

	for _, r := range results {
		snippet := r.snippet
		raw := r.raw
		if r.err != nil {
			log.Printf("warn: could not fetch logs for job %d: %v", r.job.ID, r.err)
			snippet = "(log fetch failed)"
			raw = ""
		}
		run.FailedJobs = append(run.FailedJobs, FailedJob{
			ID:         r.job.ID,
			Name:       r.job.Name,
			Conclusion: r.job.Conclusion,
			HTMLURL:    r.job.HTMLURL,
			LogSnippet: snippet,
			RawLog:     raw,
		})
	}
}

func (c *Client) listWorkflows() ([]Workflow, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/actions/workflows", c.repo)
	var resp WorkflowsListResponse
	err := c.get(url, &resp)
	return resp.Workflows, err
}

func (c *Client) fetchRuns(workflowID int, limit int) ([]Run, error) {
	url := fmt.Sprintf(
		"https://api.github.com/repos/%s/actions/workflows/%d/runs?per_page=%d",
		c.repo, workflowID, limit,
	)
	var resp WorkflowsResponse
	err := c.get(url, &resp)
	return resp.WorkflowRuns, err
}

func stripGHTimestamp(line string) string {
	if len(line) > 29 && line[10] == 'T' {
		line = line[29:]
	}
	return strings.TrimSpace(line)
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
		history[slots-len(take)+idx] = c
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

type workflowResult struct {
	summary WorkflowSummary
	health  float64
	index   int // preserves config order in output
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

	client := NewClient(
		os.Getenv("GITHUB_TOKEN"),
		cfg.Settings.SourceRepo,
		cfg.LogAnalysis,
		cfg.Settings.MaxLogConcurrency,
	)

	workflows, err := client.listWorkflows()
	if err != nil {
		log.Fatal(err)
	}

	var notifier *Notifier
	if cfg.Notify.Enabled {
		if os.Getenv("GITHUB_TOKEN") == "" {
			log.Println("warn: notify.enabled=true but GITHUB_TOKEN not set — skipping notifications")
		} else {
			notifier = NewNotifier(os.Getenv("GITHUB_TOKEN"), cfg.Settings.SourceRepo, cfg.Notify)
			log.Printf("Notifier enabled -> target: %s, threshold: %d consecutive failures",
				notifier.targetRepo, notifier.threshold)
		}
	}
	results := make([]workflowResult, len(cfg.Workflows))
	var wg sync.WaitGroup

	for i, w := range cfg.Workflows {
		wf := findWorkflow(workflows, w.Name)
		if wf == nil {
			log.Println("Not found:", w.Name)
			continue
		}
		log.Printf("Matched: %s -> %s", w.Name, wf.Name)

		wg.Add(1)
		go func(i int, w struct {
			Name        string `yaml:"name"`
			Description string `yaml:"description"`
			Critical    bool   `yaml:"critical"`
			Required    bool   `yaml:"required"`
		}, wf *Workflow) {
			defer wg.Done()

			runs, _ := client.fetchRuns(wf.ID, cfg.Settings.MaxRunsPerWorkflow)
			sort.Slice(runs, func(a, b int) bool {
				return runs[a].CreatedAt.After(runs[b].CreatedAt)
			})

			recent := runs
			if len(recent) > recentLimit {
				recent = runs[:recentLimit]
			}

			// Fetch job summaries and logs concurrently across runs.
			log.Printf("fetching jobs/logs for %s…", w.Name)
			var runWg sync.WaitGroup
			for j := range recent {
				runWg.Add(1)
				go func(j int) {
					defer runWg.Done()
					client.fetchJobSummaries(&recent[j])
					if recent[j].Conclusion == "failure" {
						client.enrichWithLogs(&recent[j])
					}
				}(j)
			}
			runWg.Wait()

			summary := buildSummary(runs, w.Name, w.Description, w.Critical, w.Required)
			summary.RecentRuns = recent

			results[i] = workflowResult{
				summary: summary,
				health:  100 - summary.FailureRate,
				index:   i,
			}
		}(i, w, wf)
	}

	wg.Wait()

	// Collect in config order; run notifier sequentially (low-volume writes).
	var summaries []WorkflowSummary
	var totalHealth float64
	for _, r := range results {
		if r.summary.Name == "" {
			continue // workflow was not found
		}
		summaries = append(summaries, r.summary)
		totalHealth += r.health
		if notifier != nil {
			notifier.Process(r.summary)
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
