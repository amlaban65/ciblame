package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/amlaban65/ciblame/internal/collect"
	"github.com/amlaban65/ciblame/internal/demo"
	"github.com/amlaban65/ciblame/internal/gh"
	"github.com/amlaban65/ciblame/internal/pricing"
)

const whyUsage = `Explain how ciblame attributes a workflow run, or the runs on a pull request.

Usage:
  ciblame why <run-url|pull-url>
  ciblame why --demo [run-id]

Examples:
  ciblame why https://github.com/acme/web/actions/runs/123456789
  ciblame why https://github.com/acme/web/pull/4149
`

type runGetter interface {
	collect.Source
	GetRun(ctx context.Context, owner, name string, id int64) (*gh.Run, error)
}

func runWhy(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("ciblame why", stderr, whyUsage)
	useDemo := fs.Bool("demo", false, "explain a run from the built-in sample data")
	sigPath := fs.String("signatures", "", "JSON file with extra agent/bot signatures")
	token := fs.String("token", "", "GitHub token (prefer GITHUB_TOKEN)")
	apiURL := fs.String("api-url", gh.DefaultBaseURL, "GitHub API base URL")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	classifier, err := buildClassifier(*sigPath)
	if err != nil {
		return err
	}

	var (
		src         runGetter
		owner, name string
		id          int64
		prNumber    int
		isPR        bool
	)
	if *useDemo {
		now := time.Now().UTC()
		d := demo.New(now.Add(-30*24*time.Hour), now)
		src = d
		owner, name, _ = strings.Cut(demo.Repo, "/")
		if len(pos) > 0 {
			if id, err = strconv.ParseInt(pos[0], 10, 64); err != nil {
				if _, _, id, err = gh.ParseRunURL(pos[0]); err != nil {
					return usagef("%v", err)
				}
			}
		} else {
			id = pickDemoRun(d)
		}
	} else {
		if len(pos) != 1 {
			fs.Usage()
			return usagef("want exactly one run or pull request URL")
		}
		raw := pos[0]
		if owner, name, id, err = gh.ParseRunURL(raw); err == nil {
			// run URL
		} else if owner, name, prNumber, err = gh.ParsePullURL(raw); err == nil {
			isPR = true
		} else {
			return usagef("want a workflow run URL or a pull request URL")
		}
		tok, _ := resolveToken(*token)
		c := gh.NewClient(tok)
		c.BaseURL = strings.TrimRight(*apiURL, "/")
		c.UserAgent = "ciblame/" + version()
		src = c
	}

	opts := collect.Options{Pricing: pricing.Default(), Classifier: classifier}
	if isPR {
		return explainPull(ctx, src, owner, name, prNumber, opts, stdout)
	}
	return explainRun(ctx, src, owner, name, id, opts, stdout)
}

func explainRun(ctx context.Context, src runGetter, owner, name string, id int64, opts collect.Options, stdout io.Writer) error {
	run, err := src.GetRun(ctx, owner, name, id)
	if err != nil {
		return err
	}
	rec, jobs, err := collect.Explain(ctx, src, owner, name, *run, opts)
	if err != nil {
		return err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Run %d · %s (%s) · %s · %s", rec.RunID, rec.Workflow, rec.WorkflowPath, rec.Event, orDash(rec.Conclusion))
	if rec.Attempts > 1 {
		fmt.Fprintf(&b, " · %d attempts", rec.Attempts)
	}
	fmt.Fprintf(&b, "\n  %s\n\n", rec.URL)
	writeAttribution(&b, rec, run)
	b.WriteString("\nJobs\n")
	sort.SliceStable(jobs, func(i, j int) bool { return jobs[i].RunAttempt < jobs[j].RunAttempt })
	t := pricing.Default()
	for _, j := range jobs {
		m := pricing.BillableMinutes(j.StartedAt, j.CompletedAt)
		cost := "unpriced"
		if rate, ok := t.Rate(j.Labels); ok {
			cost = fmt.Sprintf("$%.3f", rate*float64(m))
		}
		fmt.Fprintf(&b, "  #%d %-28s %-24s %4d min  %s\n", j.RunAttempt, truncate(j.Name, 28), truncate(strings.Join(j.Labels, ","), 24), m, cost)
	}
	fmt.Fprintf(&b, "  %-55s %4d min  $%.3f\n", fmt.Sprintf("total (%d jobs)", rec.Jobs), rec.Minutes, rec.Cost)
	b.WriteString("\nWrong attribution? Run 'ciblame agents' and see CONTRIBUTING.md to add or fix a signature.\n")
	_, err = io.WriteString(stdout, b.String())
	return err
}

func explainPull(ctx context.Context, src runGetter, owner, name string, number int, opts collect.Options, stdout io.Writer) error {
	pr, err := src.GetPull(ctx, owner, name, number)
	if err != nil {
		return err
	}
	branch := pr.Head.Ref
	from := pr.CreatedAt.Add(-time.Hour)
	if from.IsZero() {
		from = time.Now().UTC().Add(-90 * 24 * time.Hour)
	}
	to := time.Now().UTC().Add(time.Hour)

	seen := map[int64]gh.Run{}
	err = src.ListRuns(ctx, owner, name, from, to, func(page []gh.Run) error {
		for _, r := range page {
			if runBelongsToPR(r, *pr) {
				seen[r.ID] = r
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	runs := make([]gh.Run, 0, len(seen))
	for _, r := range seen {
		runs = append(runs, r)
	}
	sort.SliceStable(runs, func(i, j int) bool {
		if runs[i].CreatedAt.Equal(runs[j].CreatedAt) {
			return runs[i].ID < runs[j].ID
		}
		return runs[i].CreatedAt.Before(runs[j].CreatedAt)
	})

	var b strings.Builder
	author := "–"
	if pr.User != nil {
		author = pr.User.Login
	}
	fmt.Fprintf(&b, "Pull request #%d by %s (%s) · branch %s\n", pr.Number, author, orDash(pr.State), orDash(branch))
	if pr.HTMLURL != "" {
		fmt.Fprintf(&b, "  %s\n", pr.HTMLURL)
	}
	b.WriteString("\n")

	if len(runs) == 0 {
		b.WriteString("No workflow runs found for this pull request's head branch in the collected window.\n")
		_, err = io.WriteString(stdout, b.String())
		return err
	}

	type priced struct {
		rec collect.Record
		run gh.Run
	}
	rows := make([]priced, 0, len(runs))
	for _, run := range runs {
		rec, _, err := collect.Explain(ctx, src, owner, name, run, opts)
		if err != nil {
			return err
		}
		// Prefer the GetPull we already have for a consistent PR line.
		rec.PR = collectPRRef(pr)
		rows = append(rows, priced{rec: rec, run: run})
	}

	// Attribution once: use the newest run (agents often show up there).
	latest := rows[len(rows)-1]
	writeAttribution(&b, latest.rec, &latest.run)

	b.WriteString("\nRuns\n")
	var totalMin int
	var totalCost float64
	for _, row := range rows {
		fmt.Fprintf(&b, "  %-28s %2d att  %-10s %4d min  $%.3f\n",
			truncate(row.rec.Workflow, 28),
			row.rec.Attempts,
			orDash(row.rec.Conclusion),
			row.rec.Minutes,
			row.rec.Cost,
		)
		totalMin += row.rec.Minutes
		totalCost += row.rec.Cost
	}
	fmt.Fprintf(&b, "  %-42s %4d min  $%.3f\n", fmt.Sprintf("total (%d runs)", len(rows)), totalMin, totalCost)
	b.WriteString("\nWrong attribution? Run 'ciblame agents' and see CONTRIBUTING.md to add or fix a signature.\n")
	_, err = io.WriteString(stdout, b.String())
	return err
}

func writeAttribution(b *strings.Builder, rec collect.Record, run *gh.Run) {
	a := rec.Attribution
	fmt.Fprintf(b, "Attributed to  %s (%s", a.Name, a.Kind)
	if a.Role != "" {
		fmt.Fprintf(b, ", %s", a.Role)
	}
	fmt.Fprintf(b, ")\nEvidence       %s\n", a.Evidence)
	if a.Operator != "" {
		fmt.Fprintf(b, "Operator       %s (the account the agent worked through)\n", a.Operator)
	}
	if rec.PR != nil {
		fmt.Fprintf(b, "Pull request   #%d by %s (%s)\n", rec.PR.Number, rec.PR.Author, rec.PR.State)
	} else {
		b.WriteString("Pull request   none found\n")
	}
	if run != nil && run.Actor != nil {
		fmt.Fprintf(b, "Run actor      %s (%s)\n", run.Actor.Login, run.Actor.Type)
	}
	fmt.Fprintf(b, "Branch         %s\n", orDash(rec.Branch))
}

func runBelongsToPR(run gh.Run, pr gh.PullRequest) bool {
	for _, p := range run.PullRequests {
		if p.Number == pr.Number {
			return true
		}
	}
	if pr.Head.Ref != "" && strings.EqualFold(run.HeadBranch, pr.Head.Ref) {
		return true
	}
	return false
}

func collectPRRef(pr *gh.PullRequest) *collect.PRRef {
	if pr == nil {
		return nil
	}
	ref := &collect.PRRef{Number: pr.Number, URL: pr.HTMLURL, State: collect.PROpen}
	if pr.User != nil {
		ref.Author = pr.User.Login
	}
	switch {
	case pr.MergedAt != nil:
		ref.State = collect.PRMerged
	case pr.State == "closed":
		ref.State = collect.PRClosed
	}
	return ref
}

// pickDemoRun chooses an agent-assisted run so the demo shows something interesting.
func pickDemoRun(d *demo.Source) int64 {
	runs := d.Runs()
	for _, r := range runs {
		if r.HeadCommit != nil && strings.Contains(r.HeadCommit.Message, "Claude") && r.Event == "pull_request" && r.RunAttempt > 1 {
			return r.ID
		}
	}
	for _, r := range runs {
		if r.Status == "completed" {
			return r.ID
		}
	}
	return 0
}

func orDash(s string) string {
	if s == "" {
		return "–"
	}
	return s
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
