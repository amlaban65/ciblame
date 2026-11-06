package gh

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

const (
	// GitHub returns at most 1,000 results for filtered workflow-run listings.
	maxFilteredResults = 1000
	// Ranges narrower than this are not split further.
	minSplitRange = time.Hour
	// Format accepted by the "created" filter.
	createdLayout = "2006-01-02T15:04:05Z"
)

// GetRepo returns repository metadata.
func (c *Client) GetRepo(ctx context.Context, owner, name string) (*Repo, error) {
	var r Repo
	if _, err := c.getJSON(ctx, c.endpoint(fmt.Sprintf("/repos/%s/%s", owner, name), nil), &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// GetRun returns a single workflow run.
func (c *Client) GetRun(ctx context.Context, owner, name string, id int64) (*Run, error) {
	var r Run
	if _, err := c.getJSON(ctx, c.endpoint(fmt.Sprintf("/repos/%s/%s/actions/runs/%d", owner, name, id), nil), &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// ListRuns streams every workflow run created in [from, to] to fn, one page at a time.
//
// GitHub caps filtered run listings at 1,000 results, so when a range holds
// more than that, ListRuns bisects it and walks each half. Runs on a split
// boundary can appear twice; callers should de-duplicate by ID.
func (c *Client) ListRuns(ctx context.Context, owner, name string, from, to time.Time, fn func([]Run) error) error {
	from = from.UTC().Truncate(time.Second)
	to = to.UTC().Truncate(time.Second)
	if !to.After(from) {
		return nil
	}
	return c.listRunsRange(ctx, owner, name, from, to, fn)
}

func (c *Client) listRunsRange(ctx context.Context, owner, name string, from, to time.Time, fn func([]Run) error) error {
	q := url.Values{}
	q.Set("created", from.Format(createdLayout)+".."+to.Format(createdLayout))
	q.Set("per_page", "100")
	u := c.endpoint(fmt.Sprintf("/repos/%s/%s/actions/runs", owner, name), q)

	var page runsPage
	next, err := c.getJSON(ctx, u, &page)
	if err != nil {
		return err
	}
	if page.TotalCount > maxFilteredResults {
		if to.Sub(from) > minSplitRange {
			mid := from.Add(to.Sub(from) / 2).Truncate(time.Second)
			if err := c.listRunsRange(ctx, owner, name, from, mid, fn); err != nil {
				return err
			}
			return c.listRunsRange(ctx, owner, name, mid.Add(time.Second), to, fn)
		}
		c.logf("warning: %s/%s had %d runs between %s and %s; GitHub returns at most %d, so some are missing",
			owner, name, page.TotalCount, from.Format(time.RFC3339), to.Format(time.RFC3339), maxFilteredResults)
	}
	if len(page.WorkflowRuns) == 0 {
		return nil
	}
	if err := fn(page.WorkflowRuns); err != nil {
		return err
	}
	for next != "" {
		page = runsPage{}
		next, err = c.getJSON(ctx, next, &page)
		if err != nil {
			return err
		}
		if len(page.WorkflowRuns) == 0 {
			break
		}
		if err := fn(page.WorkflowRuns); err != nil {
			return err
		}
	}
	return nil
}

// ListJobs returns the jobs of every attempt of a workflow run.
func (c *Client) ListJobs(ctx context.Context, owner, name string, runID int64) ([]Job, error) {
	q := url.Values{}
	q.Set("filter", "all")
	q.Set("per_page", "100")
	u := c.endpoint(fmt.Sprintf("/repos/%s/%s/actions/runs/%d/jobs", owner, name, runID), q)
	var jobs []Job
	for u != "" {
		var page jobsPage
		next, err := c.getJSON(ctx, u, &page)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, page.Jobs...)
		if len(page.Jobs) == 0 {
			break
		}
		u = next
	}
	return jobs, nil
}

// ListPulls streams pull requests updated at or after since, most recently
// updated first, stopping once it reaches older pull requests.
func (c *Client) ListPulls(ctx context.Context, owner, name string, since time.Time, fn func([]PullRequest) error) error {
	q := url.Values{}
	q.Set("state", "all")
	q.Set("sort", "updated")
	q.Set("direction", "desc")
	q.Set("per_page", "100")
	u := c.endpoint(fmt.Sprintf("/repos/%s/%s/pulls", owner, name), q)
	for u != "" {
		var page []PullRequest
		next, err := c.getJSON(ctx, u, &page)
		if err != nil {
			return err
		}
		keep := page[:0]
		done := false
		for _, pr := range page {
			if pr.UpdatedAt.Before(since) {
				done = true
				continue
			}
			keep = append(keep, pr)
		}
		if len(keep) > 0 {
			if err := fn(keep); err != nil {
				return err
			}
		}
		if done || len(page) == 0 {
			return nil
		}
		u = next
	}
	return nil
}

// GetPull returns a single pull request.
func (c *Client) GetPull(ctx context.Context, owner, name string, number int) (*PullRequest, error) {
	var pr PullRequest
	if _, err := c.getJSON(ctx, c.endpoint(fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, name, number), nil), &pr); err != nil {
		return nil, err
	}
	return &pr, nil
}

// ListOwnerRepos lists the repositories of an organization, or of a user
// account if no organization by that name exists.
func (c *Client) ListOwnerRepos(ctx context.Context, owner string) ([]Repo, error) {
	q := url.Values{}
	q.Set("per_page", "100")
	q.Set("type", "all")
	repos, err := c.listRepos(ctx, c.endpoint("/orgs/"+url.PathEscape(owner)+"/repos", q))
	if errors.Is(err, ErrNotFound) {
		q.Set("type", "owner")
		return c.listRepos(ctx, c.endpoint("/users/"+url.PathEscape(owner)+"/repos", q))
	}
	return repos, err
}

func (c *Client) listRepos(ctx context.Context, u string) ([]Repo, error) {
	var all []Repo
	for u != "" {
		var page []Repo
		next, err := c.getJSON(ctx, u, &page)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) == 0 {
			break
		}
		u = next
	}
	return all, nil
}

// ParseRunURL extracts owner, repo and run ID from a run URL such as
// https://github.com/acme/web/actions/runs/123 (attempt suffixes are ignored).
func ParseRunURL(raw string) (owner, name string, id int64, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", 0, err
	}
	parts := splitPath(u.Path)
	// owner / repo / actions / runs / ID [/ attempts / N]
	if len(parts) < 5 || parts[2] != "actions" || parts[3] != "runs" {
		return "", "", 0, fmt.Errorf("not a workflow run URL: %s", raw)
	}
	id, err = strconv.ParseInt(parts[4], 10, 64)
	if err != nil {
		return "", "", 0, fmt.Errorf("not a workflow run URL: %s", raw)
	}
	return parts[0], parts[1], id, nil
}

// ParsePullURL extracts owner, repo and pull number from a pull request URL
// such as https://github.com/acme/web/pull/4149 (trailing path is ignored).
func ParsePullURL(raw string) (owner, name string, number int, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", 0, err
	}
	parts := splitPath(u.Path)
	// owner / repo / pull / N [/ ...]
	if len(parts) < 4 || parts[2] != "pull" {
		return "", "", 0, fmt.Errorf("not a pull request URL: %s", raw)
	}
	number, err = strconv.Atoi(parts[3])
	if err != nil || number <= 0 {
		return "", "", 0, fmt.Errorf("not a pull request URL: %s", raw)
	}
	return parts[0], parts[1], number, nil
}

func splitPath(p string) []string {
	var out []string
	start := -1
	for i := 0; i <= len(p); i++ {
		if i == len(p) || p[i] == '/' {
			if start >= 0 && i > start {
				out = append(out, p[start:i])
			}
			start = i + 1
		}
	}
	return out
}
