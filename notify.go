package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type NotifyConfig struct {
	Enabled    bool   `yaml:"enabled"`
	TargetRepo string `yaml:"target_repo"`
	Label      string `yaml:"label"`
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
	http       *http.Client
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
	return &Notifier{
		token:      token,
		sourceRepo: sourceRepo,
		targetRepo: targetRepo,
		label:      label,
		http:       &http.Client{Timeout: 15 * time.Second},
	}
}

func anyFailed(runs []Run) *Run {
	for i := range runs {
		if runs[i].Conclusion == "failure" {
			return &runs[i]
		}
	}
	return nil
}

func (n *Notifier) Process(summary WorkflowSummary) {
	if !summary.Critical {
		return
	}

	repr := anyFailed(summary.RecentRuns)
	if repr == nil {
		log.Printf("notify: %q — no failures in recent runs, skipping", summary.Name)
		return
	}

	log.Printf("notify: %q — failure found (run #%d)", summary.Name, repr.RunNumber)

	existingIssue := n.findOpenIssue(summary.Name)
	if existingIssue == nil {
		log.Printf("notify: opening issue for %q", summary.Name)
		n.createIssue(summary, repr)
		return
	}

	// if time.Since(existingIssue.UpdatedAt) >= 24*time.Hour {
	// 	log.Printf("notify: adding update to issue #%d for %q", existingIssue.Number, summary.Name)
	// 	n.addComment(existingIssue.Number, summary, repr)
	// } else {
	// 	log.Printf("notify: issue #%d for %q updated within 24h — skipping comment",
	// 		existingIssue.Number, summary.Name)
	// }
	n.addComment(existingIssue.Number, summary, repr)
}

func (n *Notifier) apiURL(path string) string {
	return "https://api.github.com/repos/" + n.targetRepo + path
}

func (n *Notifier) do(method, rawURL string, body interface{}) (*http.Response, error) {
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

	req, err := http.NewRequest(method, rawURL, buf)
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
		rawURL := n.apiURL(fmt.Sprintf(
			"/issues?state=open&labels=%s&per_page=100&page=%d",
			url.QueryEscape(n.label), page,
		))
		resp, err := n.do("GET", rawURL, nil)
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
				log.Printf("notify: found existing issue #%d for %q",
					issues[i].Number, workflowName)
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

// func (n *Notifier) ensureLabel() {
// 	rawURL := n.apiURL("/labels")
// 	resp, err := n.do("POST", rawURL, map[string]string{
// 		"name":        n.label,
// 		"color":       "d73a49",
// 		"description": "Automated CI failure notification",
// 	})
// 	if err != nil || resp == nil {
// 		return
// 	}
// 	resp.Body.Close()
// }

func (n *Notifier) createIssue(summary WorkflowSummary, repr *Run) {
	// n.ensureLabel()

	resp, err := n.do("POST", n.apiURL("/issues"), map[string]interface{}{
		"title":  issueTitle(summary.Name),
		"body":   buildIssueBody(summary, repr, n.sourceRepo),
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

func (n *Notifier) addComment(issueNumber int, summary WorkflowSummary, repr *Run) {
	rawURL := n.apiURL(fmt.Sprintf("/issues/%d/comments", issueNumber))
	resp, err := n.do("POST", rawURL, map[string]string{
		"body": buildCommentBody(summary, repr, n.sourceRepo),
	})
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

func buildRawLogBlock(job FailedJob) string {
	raw := job.RawLog
	if raw == "" ||
		strings.HasPrefix(raw, "(log expired") ||
		strings.HasPrefix(raw, "(log fetch") {
		return ""
	}
	const maxBytes = 30000
	truncated := false
	if len(raw) > maxBytes {
		raw = raw[:maxBytes]
		truncated = true
	}

	var sb strings.Builder
	sb.WriteString("<details>\n")
	sb.WriteString(fmt.Sprintf("<summary>Full step log — %s</summary>\n\n", job.Name))
	sb.WriteString("```\n")
	sb.WriteString(raw)
	if truncated {
		sb.WriteString("\n\n[truncated at 30KB — see full log at: ")
		sb.WriteString(job.HTMLURL)
		sb.WriteString("]")
	}
	sb.WriteString("\n```\n")
	sb.WriteString("</details>\n\n")
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
	sb.WriteString(buildRawLogBlock(job))
	return sb.String()
}

func buildFailedJobsSection(jobs []FailedJob) string {
	if len(jobs) == 0 {
		return "> No individual job failure data captured — check the run link above.\n\n"
	}
	var sb strings.Builder
	for _, job := range jobs {
		sb.WriteString(fmt.Sprintf("#### ❌ [%s](%s)\n\n", job.Name, job.HTMLURL))
		sb.WriteString(buildSnippetSection(job))
	}
	return sb.String()
}

func buildIssueBody(summary WorkflowSummary, repr *Run, sourceRepo string) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("## ❌ Critical workflow failing: `%s`\n\n", summary.Name))
	if summary.Description != "" {
		sb.WriteString(fmt.Sprintf("> %s\n\n", summary.Description))
	}

	sb.WriteString("### Failed Run\n\n")
	sb.WriteString("| Field | Value |\n|---|---|\n")
	sb.WriteString(fmt.Sprintf("| Run | [#%d](%s) |\n", repr.RunNumber, repr.HTMLURL))
	sb.WriteString(fmt.Sprintf("| Started | `%s` |\n", repr.CreatedAt.Format(time.RFC1123)))
	sb.WriteString(fmt.Sprintf("| Conclusion | `%s` |\n", repr.Conclusion))
	sb.WriteString(fmt.Sprintf("| Attempt | `%d` |\n", repr.RunAttempt))
	sb.WriteString(fmt.Sprintf("| Repo | [%s](https://github.com/%s) |\n\n",
		sourceRepo, sourceRepo))

	if spark := buildSparkline(summary.WeatherHistory); spark != "" {
		sb.WriteString("### Recent History (oldest → newest)\n\n")
		sb.WriteString(spark + "\n\n")
	}

	sb.WriteString("### Failed Jobs\n\n")
	sb.WriteString(buildFailedJobsSection(repr.FailedJobs))

	sb.WriteString("---\n")
	sb.WriteString("This issue was opened automatically by the CI dashboard.")
	sb.WriteString("Please close it manually once the issue is resolved._\n")

	return sb.String()
}

func buildCommentBody(summary WorkflowSummary, repr *Run, sourceRepo string) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("### ❌ Still failing — run [#%d](%s)\n\n",
		repr.RunNumber, repr.HTMLURL))
	sb.WriteString(fmt.Sprintf("Failed at `%s`.\n\n", repr.CreatedAt.Format(time.RFC1123)))

	if spark := buildSparkline(summary.WeatherHistory); spark != "" {
		sb.WriteString("**Recent history (oldest → newest):** " + spark + "\n\n")
	}

	sb.WriteString(buildFailedJobsSection(repr.FailedJobs))

	return sb.String()
}
