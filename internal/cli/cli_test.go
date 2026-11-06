package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/amlaban65/ciblame/internal/gh"
)

func runCLI(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errb bytes.Buffer
	code = Main(context.Background(), args, &out, &errb)
	return out.String(), errb.String(), code
}

func TestDemoTable(t *testing.T) {
	out, errOut, code := runCLI(t, "--demo", "--color", "never")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, w := range []string{"acme/storefront", "GitHub Copilot", "Claude Code", "OpenAI Codex", "Devin", "Dependabot", "humans (5 people)", "▸ AI agents used"} {
		if !strings.Contains(out, w) {
			t.Errorf("demo output missing %q", w)
		}
	}
}

func TestDemoJSONIsValid(t *testing.T) {
	out, _, code := runCLI(t, "--demo", "--format", "json", "--records", "--by", "kind")
	if code != 0 {
		t.Fatal("non-zero exit")
	}
	var v struct {
		Total struct {
			Minutes int `json:"billable_minutes"`
		} `json:"total"`
		Records []json.RawMessage `json:"records"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatal(err)
	}
	if v.Total.Minutes == 0 || len(v.Records) == 0 {
		t.Fatalf("empty report: %+v", v.Total)
	}
}

func TestUsageErrors(t *testing.T) {
	cases := [][]string{
		{},                                 // no repos
		{"--demo", "acme/web"},             // demo with repo
		{"--demo", "--format", "yaml"},     // bad format
		{"--demo", "--by", "color"},        // bad grouping
		{"--demo", "--since", "yesterday"}, // bad time
		{"--demo", "--since", "2026-10-05", "--until", "2026-10-01"},
		{"--demo", "--rate", "oops"},
		{"not a repo"},
		{"--no-such-flag"},
	}
	for _, args := range cases {
		if _, _, code := runCLI(t, args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
}

func TestAgentsAndVersionAndWhy(t *testing.T) {
	out, _, code := runCLI(t, "agents")
	if code != 0 || !strings.Contains(out, "claude-code") || !strings.Contains(out, "CONTRIBUTING") {
		t.Fatalf("agents: %d %s", code, out)
	}
	out, _, code = runCLI(t, "agents", "--json")
	if code != 0 || !json.Valid([]byte(out)) {
		t.Fatalf("agents --json invalid")
	}
	out, _, code = runCLI(t, "version")
	if code != 0 || !strings.HasPrefix(out, "ciblame ") {
		t.Fatalf("version: %q", out)
	}
	out, errOut, code := runCLI(t, "why", "--demo")
	if code != 0 || !strings.Contains(out, "Attributed to") || !strings.Contains(out, "Evidence") {
		t.Fatalf("why --demo: %d %s %s", code, out, errOut)
	}
	if _, _, code := runCLI(t, "why", "https://example.com/not-a-github-url"); code != 2 {
		t.Fatalf("why with a non-GitHub URL should be a usage error, got %d", code)
	}
}

// fakeGitHub serves just enough of the REST API for an end-to-end report.
func fakeGitHub(t *testing.T) *httptest.Server {
	t.Helper()
	now := time.Now().UTC()
	start := now.Add(-2 * time.Hour)
	end := now.Add(-time.Hour)
	runs := []gh.Run{
		{ID: 101, Name: "CI", Path: ".github/workflows/ci.yml", Event: "pull_request", Status: "completed", Conclusion: "failure",
			HeadBranch: "copilot/fix", RunAttempt: 1, CreatedAt: start, Actor: &gh.User{Login: "Copilot", Type: "Bot"},
			PullRequests: []gh.RunPullRequest{{Number: 7}}, HTMLURL: "https://github.com/acme/web/actions/runs/101"},
		{ID: 102, Name: "CI", Path: ".github/workflows/ci.yml", Event: "push", Status: "completed", Conclusion: "success",
			HeadBranch: "main", RunAttempt: 1, CreatedAt: start, Actor: &gh.User{Login: "alice", Type: "User"},
			HeadCommit: &gh.HeadCommit{Message: "Fix\n\nCo-Authored-By: Claude <noreply@anthropic.com>"}},
	}
	pr := gh.PullRequest{Number: 7, State: "closed", User: &gh.User{Login: "Copilot", Type: "Bot"}, UpdatedAt: now, CreatedAt: start}
	pr.Head.Ref = "copilot/fix"
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("/orgs/acme/repos", func(w http.ResponseWriter, r *http.Request) {
		write(w, []gh.Repo{{FullName: "acme/web", PushedAt: now}, {FullName: "acme/old", PushedAt: now.AddDate(-1, 0, 0)}, {FullName: "acme/archived", Archived: true, PushedAt: now}})
	})
	mux.HandleFunc("/repos/acme/web", func(w http.ResponseWriter, r *http.Request) {
		write(w, gh.Repo{FullName: "acme/web", Private: true})
	})
	mux.HandleFunc("/repos/acme/web/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Query().Get("created"), "..") {
			t.Errorf("missing created range: %s", r.URL.RawQuery)
		}
		if r.Header.Get("Authorization") != "Bearer fake-token" {
			t.Errorf("token not sent")
		}
		write(w, map[string]any{"total_count": len(runs), "workflow_runs": runs})
	})
	mux.HandleFunc("/repos/acme/web/actions/runs/101", func(w http.ResponseWriter, r *http.Request) { write(w, runs[0]) })
	mux.HandleFunc("/repos/acme/web/actions/runs/101/jobs", func(w http.ResponseWriter, r *http.Request) {
		write(w, map[string]any{"total_count": 1, "jobs": []gh.Job{{ID: 1, RunAttempt: 1, Name: "test", StartedAt: &start, CompletedAt: &end, Labels: []string{"ubuntu-latest"}}}})
	})
	mux.HandleFunc("/repos/acme/web/actions/runs/102/jobs", func(w http.ResponseWriter, r *http.Request) {
		e := start.Add(10 * time.Minute)
		write(w, map[string]any{"total_count": 1, "jobs": []gh.Job{{ID: 2, RunAttempt: 1, Name: "build", StartedAt: &start, CompletedAt: &e, Labels: []string{"macos-14"}}}})
	})
	mux.HandleFunc("/repos/acme/web/pulls", func(w http.ResponseWriter, r *http.Request) { write(w, []gh.PullRequest{pr}) })
	mux.HandleFunc("/repos/acme/web/pulls/7", func(w http.ResponseWriter, r *http.Request) { write(w, pr) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestEndToEndAgainstFakeAPI(t *testing.T) {
	srv := fakeGitHub(t)
	t.Setenv("GITHUB_TOKEN", "fake-token")
	out, errOut, code := runCLI(t, "--api-url", srv.URL, "--no-cache", "--org", "acme", "--since", "1d", "--format", "json", "--quiet")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	var rep struct {
		Repos []struct {
			FullName string `json:"full_name"`
		} `json:"repos"`
		Total struct {
			Minutes int     `json:"billable_minutes"`
			Cost    float64 `json:"estimated_cost_usd"`
		} `json:"total"`
		Rows []struct {
			Key           string `json:"key"`
			Minutes       int    `json:"billable_minutes"`
			WastedMinutes int    `json:"minutes_on_unmerged_pull_requests"`
		} `json:"rows"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(rep.Repos) != 1 || rep.Repos[0].FullName != "acme/web" {
		t.Fatalf("--org should skip archived and inactive repos: %+v", rep.Repos)
	}
	if rep.Total.Minutes != 70 {
		t.Fatalf("total minutes %d, want 60 + 10", rep.Total.Minutes)
	}
	got := map[string]int{}
	for _, r := range rep.Rows {
		got[r.Key] = r.Minutes
		if r.Key == "copilot" && r.WastedMinutes != 60 {
			t.Errorf("copilot PR was closed unmerged; wasted = %d", r.WastedMinutes)
		}
	}
	if got["copilot"] != 60 || got["claude-code"] != 10 {
		t.Fatalf("rows %v", got)
	}
	wantCost := 60*0.006 + 10*0.062
	if d := rep.Total.Cost - wantCost; d > 0.001 || d < -0.001 {
		t.Fatalf("cost %v, want %v", rep.Total.Cost, wantCost)
	}

	out, errOut, code = runCLI(t, "why", "--api-url", srv.URL, "https://github.com/acme/web/actions/runs/101")
	if code != 0 || !strings.Contains(out, "GitHub Copilot") || !strings.Contains(out, "#7 by Copilot (closed)") {
		t.Fatalf("why: %d\n%s\n%s", code, out, errOut)
	}

	out, errOut, code = runCLI(t, "why", "--api-url", srv.URL, "https://github.com/acme/web/pull/7")
	if code != 0 {
		t.Fatalf("why pull: %d\n%s\n%s", code, out, errOut)
	}
	for _, w := range []string{"Pull request #7", "Attributed to", "GitHub Copilot", "Runs", "total ("} {
		if !strings.Contains(out, w) {
			t.Fatalf("why pull missing %q:\n%s", w, out)
		}
	}
}

func TestParseWhen(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	cases := map[string]time.Time{
		"7d":                   now.AddDate(0, 0, -7),
		"12h":                  now.Add(-12 * time.Hour),
		"2w":                   now.AddDate(0, 0, -14),
		"2026-10-01":           time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		"2026-10-01T08:30:00Z": time.Date(2026, 10, 1, 8, 30, 0, 0, time.UTC),
	}
	for in, want := range cases {
		got, err := parseWhen(in, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("parseWhen(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := parseWhen("soon", now); err == nil {
		t.Error("expected error")
	}
}

func TestNormalizeRepo(t *testing.T) {
	ok := map[string]string{
		"acme/web":                          "acme/web",
		"https://github.com/acme/web":       "acme/web",
		"https://github.com/acme/web.git":   "acme/web",
		"github.com/acme/web/actions":       "acme/web",
		"https://github.com/acme/web/pulls": "acme/web",
	}
	for in, want := range ok {
		if got, err := normalizeRepo(in); err != nil || got != want {
			t.Errorf("normalizeRepo(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"acme", "acme/../x", "https://gitlab.com/acme/web", "-acme/web", "acme/web/extra"} {
		if _, err := normalizeRepo(bad); err == nil {
			t.Errorf("normalizeRepo(%q) should fail", bad)
		}
	}
}

func TestParseArgsInterleaved(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	since := fs.String("since", "", "")
	fast := fs.Bool("fast", false, "")
	pos, err := parseArgs(fs, []string{"acme/a", "--since", "7d", "acme/b", "--fast", "--", "--weird"})
	if err != nil {
		t.Fatal(err)
	}
	if *since != "7d" || !*fast || strings.Join(pos, " ") != "acme/a acme/b --weird" {
		t.Fatalf("since=%q fast=%v pos=%v", *since, *fast, pos)
	}
}
