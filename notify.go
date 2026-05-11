package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

type NotifyConfig struct {
	Enabled             bool   `yaml:"enabled"`
	TargetRepo          string `yaml:"target_repo"`
	Label               string `yaml:"label"`
	ConsecutiveFailures int    `yaml:"consecutive_failures"`
}

type Issue struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	State     string    `json:"state"`
	HTMLURL   string    `json:"html_url"`
	UpdatedAt time.Time `json:"updated_at"`
}

type IssuesResponse []Issue

type Notifier struct {
	token      string
	sourceRepo string
	targetRepo string
	label      string
	threshold  int
	http       *http.Client
}

func shouldComment(issue *Issue) bool {
	return time.Since(issue.UpdatedAt) >= 24*time.Hour
}

func NewNotifier(token, sourceRepo string, cfg NotifyConfig) *Notifier {
	targetRepo := cfg.TargetRepo
	if targetRepo == "" {
		targetRepo = sourceRepo
	}
	label := cfg.Label
	if label == "" {
		label = "ci-failure"
	}
	threshold := cfg.ConsecutiveFailures
	if threshold <= 0 {
		threshold = 1
	}
	return &Notifier{
		token:      token,
		sourceRepo: sourceRepo,
		targetRepo: targetRepo,
		label:      label,
		threshold:  threshold,
		http:       &http.Client{Timeout: 15 * time.Second},
	}
}

func consecutiveFailures(history []string) int {
	count := 0
	for i := len(history) - 1; i >= 0; i-- {
		switch history[i] {
		case "failure":
			count++
		case "success":
			return count
		default:
			return count
		}
	}
	return count
}

func (n *Notifier) Process(summary WorkflowSummary) {
	if !summary.Critical {
		return
	}
	if summary.LastRun == nil {
		return
	}
	if summary.LastRun.Conclusion != "failure" {
		log.Printf("notify: %q passing — no action", summary.Name)
		return
	}
	consecutive := consecutiveFailures(summary.WeatherHistory)
	log.Printf("notify: %q failing — %d consecutive failure(s)", summary.Name, consecutive)
	existingIssue := n.findOpenIssue(summary.Name)

	if existingIssue != nil {
		if shouldComment(existingIssue) {
			log.Printf("notify: adding daily update to issue #%d for %q", existingIssue.Number, summary.Name)
			n.addComment(existingIssue.Number, summary, consecutive)
		} else {
			log.Printf("notify: issue #%d for %q updated recently — skipping comment", existingIssue.Number, summary.Name)
		}
		return
	}

	if consecutive >= n.threshold {
		log.Printf("notify: opening issue for %q (%d consecutive failures)", summary.Name, consecutive)
		n.createIssue(summary, consecutive)
		return
	}

	log.Printf("notify: %q failing but threshold not reached (%d/%d)",
		summary.Name, consecutive, n.threshold)
}

func (n *Notifier) apiURL(path string) string {
	return "https://api.github.com/repos/" + n.targetRepo + path
}

func (n *Notifier) do(method, url string, body interface{}) (*http.Response, error) {
	var buf *bytes.Buffer
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		buf = bytes.NewBuffer(b)
	} else {
		buf = bytes.NewBuffer(nil)
	}

	req, err := http.NewRequest(method, url, buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+n.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	return n.http.Do(req)
}

func (n *Notifier) findOpenIssue(workflowName string) *Issue {
	needle := issueTitle(workflowName)
	page := 1
	for {
		url := n.apiURL(fmt.Sprintf(
			"/issues?state=open&labels=%s&per_page=100&page=%d",
			urlEncode(n.label), page,
		))
		resp, err := n.do("GET", url, nil)
		if err != nil {
			log.Printf("notify: warn: could not list issues: %v", err)
			return nil
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			log.Printf("notify: warn: issues list returned HTTP %d", resp.StatusCode)
			return nil
		}

		var issues IssuesResponse
		if err := json.NewDecoder(resp.Body).Decode(&issues); err != nil {
			resp.Body.Close()
			log.Printf("notify: warn: could not decode issues: %v", err)
			return nil
		}
		resp.Body.Close()

		for i := range issues {
			title := issues[i].Title
			if strings.EqualFold(title, needle) || strings.Contains(title, workflowName) {
				log.Printf("notify: found existing issue #%d for %q", issues[i].Number, workflowName)
				return &issues[i]
			}
		}
		if len(issues) < 100 {
			break
		}
		page++
	}
	return nil
}

func urlEncode(s string) string {
	var sb strings.Builder
	for _, b := range []byte(s) {
		switch {
		case b >= 'A' && b <= 'Z',
			b >= 'a' && b <= 'z',
			b >= '0' && b <= '9',
			b == '-', b == '_', b == '.', b == '~':
			sb.WriteByte(b)
		default:
			fmt.Fprintf(&sb, "%%%02X", b)
		}
	}
	return sb.String()
}

func (n *Notifier) ensureLabel() {
	url := n.apiURL("/labels")
	resp, err := n.do("POST", url, map[string]string{
		"name":        n.label,
		"color":       "d73a49",
		"description": "Automated CI failure notification",
	})
	if err != nil || resp == nil {
		return
	}
	resp.Body.Close()
}

func (n *Notifier) createIssue(summary WorkflowSummary, consecutive int) {
	n.ensureLabel()

	title := issueTitle(summary.Name)
	body := buildIssueBody(summary, consecutive, n.sourceRepo)

	resp, err := n.do("POST", n.apiURL("/issues"), map[string]interface{}{
		"title":  title,
		"body":   body,
		"labels": []string{n.label},
	})
	if err != nil {
		log.Printf("notify: warn: could not create issue: %v", err)
		return
	}
	defer resp.Body.Close()

	var created Issue
	json.NewDecoder(resp.Body).Decode(&created)
	if created.Number > 0 {
		log.Printf("notify: issue #%d created → %s", created.Number, created.HTMLURL)
	}
}

func (n *Notifier) addComment(issueNumber int, summary WorkflowSummary, consecutive int) {
	body := buildCommentBody(summary, consecutive, n.sourceRepo)
	url := n.apiURL(fmt.Sprintf("/issues/%d/comments", issueNumber))

	resp, err := n.do("POST", url, map[string]string{"body": body})
	if err != nil {
		log.Printf("notify: warn: could not add comment: %v", err)
		return
	}
	resp.Body.Close()
}

func issueTitle(workflowName string) string {
	return fmt.Sprintf("CI Failure: %s", workflowName)
}

func weatherEmoji(c string) string {
	switch c {
	case "success":
		return "✅"
	case "failure":
		return "❌"
	case "skipped":
		return "⏭️"
	case "action_required":
		return "⚠️"
	default:
		return "⬜"
	}
}

// buildSparkline renders the weather history as an emoji row.
func buildSparkline(history []string) string {
	if len(history) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, c := range history {
		sb.WriteString(weatherEmoji(c))
		sb.WriteString(" ")
	}
	return strings.TrimSpace(sb.String())
}

func buildFailedJobsSection(jobs []FailedJob) string {
	if len(jobs) == 0 {
		return "> _No individual job failure data captured — check the run link above._\n\n"
	}
	var sb strings.Builder
	for _, job := range jobs {
		sb.WriteString(fmt.Sprintf("#### ✗ [%s](%s)\n\n", job.Name, job.HTMLURL))
		sb.WriteString(buildSnippetSection(job))
	}
	return sb.String()
}

func buildSnippetSection(job FailedJob) string {
	var sb strings.Builder

	switch job.LogSnippet {
	case "", "(no actionable failure signal found in log)", "(log fetch failed)":
		if job.LogSnippet != "" {
			sb.WriteString(fmt.Sprintf("> _%s_\n\n", job.LogSnippet))
		}
	default:
		sb.WriteString("**Signal summary:**\n\n")
		sb.WriteString("```\n")
		sb.WriteString(strings.TrimSpace(job.LogSnippet))
		sb.WriteString("\n```\n\n")
	}

	return sb.String()
}

func buildIssueBody(summary WorkflowSummary, consecutive int, sourceRepo string) string {
	lr := summary.LastRun
	var sb strings.Builder

	// Header
	sb.WriteString(fmt.Sprintf("## ❌ Critical workflow failing: `%s`\n\n", summary.Name))
	if summary.Description != "" {
		sb.WriteString(fmt.Sprintf("> %s\n\n", summary.Description))
	}
	sb.WriteString(fmt.Sprintf("**%d consecutive failure(s)** detected by the CI dashboard.\n\n", consecutive))

	// Run metadata
	sb.WriteString("### Latest Run\n\n")
	sb.WriteString("| Field | Value |\n|---|---|\n")
	sb.WriteString(fmt.Sprintf("| Run | [#%d](%s) |\n", lr.RunNumber, lr.HTMLURL))
	sb.WriteString(fmt.Sprintf("| Started | `%s` |\n", lr.CreatedAt.Format(time.RFC1123)))
	sb.WriteString(fmt.Sprintf("| Conclusion | `%s` |\n", lr.Conclusion))
	sb.WriteString(fmt.Sprintf("| Attempt | `%d` |\n", lr.RunAttempt))
	sb.WriteString(fmt.Sprintf("| Repo | [%s](https://github.com/%s) |\n\n", sourceRepo, sourceRepo))

	// Sparkline
	if spark := buildSparkline(summary.WeatherHistory); spark != "" {
		sb.WriteString("### Recent History (oldest → newest)\n\n")
		sb.WriteString(spark + "\n\n")
	}

	// Failed jobs
	sb.WriteString("### Failed Jobs\n\n")
	sb.WriteString(buildFailedJobsSection(lr.FailedJobs))

	// Footer
	sb.WriteString("---\n")
	sb.WriteString("_This issue was opened automatically by the CI dashboard. ")
	sb.WriteString("Please close it manually once the issue is resolved._\n")

	return sb.String()
}

func buildCommentBody(summary WorkflowSummary, consecutive int, sourceRepo string) string {
	lr := summary.LastRun
	var sb strings.Builder

	// Header
	sb.WriteString(fmt.Sprintf("### ❌ Still failing — run [#%d](%s)\n\n", lr.RunNumber, lr.HTMLURL))
	sb.WriteString(fmt.Sprintf("**%d consecutive failure(s)** as of `%s`.\n\n",
		consecutive, lr.CreatedAt.Format(time.RFC1123)))

	// Sparkline
	if spark := buildSparkline(summary.WeatherHistory); spark != "" {
		sb.WriteString("**Recent history (oldest → newest):** " + spark + "\n\n")
	}

	// Failed jobs
	sb.WriteString(buildFailedJobsSection(lr.FailedJobs))

	return sb.String()
}
