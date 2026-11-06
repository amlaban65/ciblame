package gh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestClient(t *testing.T, h http.Handler) (*Client, *[]time.Duration) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := NewClient("test-token")
	c.BaseURL = srv.URL
	var slept []time.Duration
	var mu sync.Mutex
	c.sleep = func(_ context.Context, d time.Duration) error {
		mu.Lock()
		slept = append(slept, d)
		mu.Unlock()
		return nil
	}
	return c, &slept
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func TestAuthHeaderAndPagination(t *testing.T) {
	var srvURL string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q", got)
		}
		if r.URL.Query().Get("filter") != "all" {
			t.Errorf("jobs should be listed with filter=all")
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page == 0 {
			page = 1
		}
		if page < 3 {
			w.Header().Set("Link", fmt.Sprintf(`<%s%s?filter=all&per_page=100&page=%d>; rel="next", <%s/x?page=3>; rel="last"`,
				srvURL, r.URL.Path, page+1, srvURL))
		}
		writeJSON(w, jobsPage{TotalCount: 3, Jobs: []Job{{ID: int64(page)}}})
	})
	c, _ := newTestClient(t, h)
	srvURL = c.BaseURL
	jobs, err := c.ListJobs(context.Background(), "acme", "web", 42)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 3 || jobs[0].ID != 1 || jobs[2].ID != 3 {
		t.Fatalf("got %+v", jobs)
	}
}

func TestListRunsBisectsLargeRanges(t *testing.T) {
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(48 * time.Hour)
	var mu sync.Mutex
	var ranges []string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		created := r.URL.Query().Get("created")
		mu.Lock()
		ranges = append(ranges, created)
		mu.Unlock()
		a, b, ok := strings.Cut(created, "..")
		if !ok {
			t.Fatalf("bad created filter %q", created)
		}
		start, err1 := time.Parse(createdLayout, a)
		end, err2 := time.Parse(createdLayout, b)
		if err1 != nil || err2 != nil {
			t.Fatalf("bad created filter %q", created)
		}
		// Pretend there are 1,500 runs per day.
		total := int(end.Sub(start).Hours() / 24 * 1500)
		writeJSON(w, runsPage{TotalCount: total, WorkflowRuns: []Run{{ID: start.Unix()}}})
	})
	c, _ := newTestClient(t, h)
	var got []Run
	err := c.ListRuns(context.Background(), "acme", "web", from, to, func(rs []Run) error {
		got = append(got, rs...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// 48h of 1,500/day must be split until each piece is ≤ 1,000 runs (≤ 16h).
	if len(got) < 3 {
		t.Fatalf("expected the range to be split, got %d leaf ranges (%v)", len(got), ranges)
	}
	if !strings.HasPrefix(ranges[0], "2026-10-01T00:00:00Z..2026-10-03T00:00:00Z") {
		t.Fatalf("first request should cover the full range, got %q", ranges[0])
	}
}

func TestRateLimitWaitThenSucceed(t *testing.T) {
	calls := 0
	reset := time.Now().Add(3 * time.Second).Unix()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset, 10))
			w.WriteHeader(http.StatusForbidden)
			writeJSON(w, map[string]string{"message": "API rate limit exceeded"})
			return
		}
		writeJSON(w, Repo{FullName: "acme/web"})
	})
	c, slept := newTestClient(t, h)
	r, err := c.GetRepo(context.Background(), "acme", "web")
	if err != nil {
		t.Fatal(err)
	}
	if r.FullName != "acme/web" || calls != 2 || len(*slept) != 1 {
		t.Fatalf("calls=%d slept=%v repo=%+v", calls, *slept, r)
	}
}

func TestSecondaryRateLimitRetryAfter(t *testing.T) {
	calls := 0
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		writeJSON(w, Repo{})
	})
	c, slept := newTestClient(t, h)
	if _, err := c.GetRepo(context.Background(), "acme", "web"); err != nil {
		t.Fatal(err)
	}
	if len(*slept) != 1 || (*slept)[0] != 7*time.Second {
		t.Fatalf("slept %v", *slept)
	}
}

func TestRateLimitTooLongReturnsError(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(2*time.Hour).Unix(), 10))
		w.WriteHeader(http.StatusForbidden)
	})
	c, _ := newTestClient(t, h)
	_, err := c.GetRepo(context.Background(), "acme", "web")
	var rl *RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("want RateLimitError, got %v", err)
	}
}

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		status int
		body   string
		check  func(error) bool
	}{
		{401, `{"message":"Bad credentials"}`, func(e error) bool { return errors.Is(e, ErrUnauthorized) }},
		{404, `{"message":"Not Found"}`, func(e error) bool { return errors.Is(e, ErrNotFound) }},
		{403, `{"message":"Resource not accessible by integration"}`, func(e error) bool {
			var ae *APIError
			return errors.As(e, &ae) && ae.StatusCode == 403 && strings.Contains(ae.Message, "not accessible")
		}},
		{422, `{"message":"Validation Failed"}`, func(e error) bool {
			var ae *APIError
			return errors.As(e, &ae) && ae.StatusCode == 422
		}},
	}
	for _, tc := range cases {
		calls := 0
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		})
		c, _ := newTestClient(t, h)
		_, err := c.GetRepo(context.Background(), "acme", "web")
		if !tc.check(err) {
			t.Errorf("status %d: unexpected error %v", tc.status, err)
		}
		if calls != 1 {
			t.Errorf("status %d: should not retry, got %d calls", tc.status, calls)
		}
	}
}

func TestServerErrorsAreRetried(t *testing.T) {
	calls := 0
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		writeJSON(w, Repo{FullName: "acme/web"})
	})
	c, slept := newTestClient(t, h)
	if _, err := c.GetRepo(context.Background(), "acme", "web"); err != nil {
		t.Fatal(err)
	}
	if calls != 3 || len(*slept) != 2 {
		t.Fatalf("calls=%d slept=%v", calls, *slept)
	}
}

func TestListPullsStopsAtSince(t *testing.T) {
	since := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	var srvURL string
	pages := 0
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages++
		q := r.URL.Query()
		if q.Get("state") != "all" || q.Get("sort") != "updated" || q.Get("direction") != "desc" {
			t.Errorf("unexpected query %v", q)
		}
		w.Header().Set("Link", fmt.Sprintf(`<%s/repos/acme/web/pulls?page=2>; rel="next"`, srvURL))
		writeJSON(w, []PullRequest{
			{Number: 3, UpdatedAt: since.Add(48 * time.Hour)},
			{Number: 2, UpdatedAt: since.Add(time.Hour)},
			{Number: 1, UpdatedAt: since.Add(-time.Hour)},
		})
	})
	c, _ := newTestClient(t, h)
	srvURL = c.BaseURL
	var got []int
	err := c.ListPulls(context.Background(), "acme", "web", since, func(ps []PullRequest) error {
		for _, p := range ps {
			got = append(got, p.Number)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if pages != 1 || fmt.Sprint(got) != "[3 2]" {
		t.Fatalf("pages=%d got=%v", pages, got)
	}
}

func TestListOwnerReposFallsBackToUser(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/orgs/"):
			w.WriteHeader(http.StatusNotFound)
		case r.URL.Path == "/users/alice/repos":
			writeJSON(w, []Repo{{FullName: "alice/dotfiles"}})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	c, _ := newTestClient(t, h)
	repos, err := c.ListOwnerRepos(context.Background(), "alice")
	if err != nil || len(repos) != 1 || repos[0].FullName != "alice/dotfiles" {
		t.Fatalf("repos=%v err=%v", repos, err)
	}
}

func TestParseRunURL(t *testing.T) {
	o, n, id, err := ParseRunURL("https://github.com/acme/web/actions/runs/123456789/attempts/2")
	if err != nil || o != "acme" || n != "web" || id != 123456789 {
		t.Fatalf("got %q %q %d %v", o, n, id, err)
	}
	for _, bad := range []string{"https://github.com/acme/web/pull/3", "https://github.com/acme/web/actions/runs/abc", "nope"} {
		if _, _, _, err := ParseRunURL(bad); err == nil {
			t.Errorf("ParseRunURL(%q) should fail", bad)
		}
	}
}

func TestNextLink(t *testing.T) {
	h := `<https://api.github.com/x?page=1>; rel="prev", <https://api.github.com/x?page=3>; rel="next", <https://api.github.com/x?page=9>; rel="last"`
	if got := nextLink(h); got != "https://api.github.com/x?page=3" {
		t.Fatalf("got %q", got)
	}
	if nextLink(`<https://x>; rel="last"`) != "" || nextLink("") != "" {
		t.Fatal("expected no next link")
	}
}

func TestParsePullURL(t *testing.T) {
	o, n, num, err := ParsePullURL("https://github.com/acme/web/pull/4149/files")
	if err != nil || o != "acme" || n != "web" || num != 4149 {
		t.Fatalf("ParsePullURL: %q %q %d %v", o, n, num, err)
	}
	for _, bad := range []string{"https://github.com/acme/web", "https://github.com/acme/web/pull/x", "https://github.com/acme/web/actions/runs/1"} {
		if _, _, _, err := ParsePullURL(bad); err == nil {
			t.Fatalf("ParsePullURL(%q) should fail", bad)
		}
	}
}
