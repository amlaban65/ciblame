# ciblame: GitHub Actions cost per AI agent, bot and human

**`git blame` for your CI bill.** ciblame is a command-line tool that shows how many GitHub Actions minutes and dollars go to AI coding agents (GitHub Copilot, Claude Code, OpenAI Codex, Devin, Cursor), to bots like Dependabot and Renovate, and to the people on your team.

[![CI](https://github.com/amlaban65/ciblame/actions/workflows/ci.yml/badge.svg)](https://github.com/amlaban65/ciblame/actions/workflows/ci.yml)
[![npm](https://img.shields.io/npm/v/ciblame.svg)](https://www.npmjs.com/package/ciblame)
[![Go Reference](https://pkg.go.dev/badge/github.com/amlaban65/ciblame.svg)](https://pkg.go.dev/github.com/amlaban65/ciblame)
[![Go Report Card](https://goreportcard.com/badge/github.com/amlaban65/ciblame)](https://goreportcard.com/report/github.com/amlaban65/ciblame)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

<img src="docs/demo.svg" alt="ciblame report: AI agents used 73% of CI minutes; Copilot 45%, humans 17%, Claude Code 16%; 5,127 agent minutes went to PRs closed without merging" width="100%">

<sub>Output of <code>ciblame --demo</code>, a month of generated activity on a fictional repo.</sub>

Guides: [how much Actions AI coding agents use](https://amlaban65.github.io/ciblame/guides/ai-coding-agents-github-actions-cost/), [GitHub Actions pricing in 2026](https://amlaban65.github.io/ciblame/guides/github-actions-pricing/), [why the bill is higher than expected](https://amlaban65.github.io/ciblame/guides/github-actions-bill-higher-than-expected/) and [how to reduce Actions minutes](https://amlaban65.github.io/ciblame/guides/reduce-github-actions-minutes/).

## Why

AI coding agents use CI as their test loop. Each attempt at getting the build green is another workflow run, and a single agent PR can rack up dozens of them.

- GitHub reported agent-opened pull requests rising from about **4 million in Sept 2025 to over 17 million in March 2026**, and Actions compute hitting **2.1 billion minutes in a single week** ([source](https://tianpan.co/blog/2026/06/02/the-coding-agent-ci-bill-that-doubled-without-a-postmortem)).
- Since **June 1, 2026**, Copilot code review on private repos [consumes Actions minutes](https://github.blog/changelog/2026-04-27-github-copilot-code-review-will-start-consuming-github-actions-minutes-on-june-1-2026/) on top of AI credits.
- GitHub's billing breaks minutes down by repo and workflow, not by agent. When an agent works through someone's account (Claude Code, Codex, Cursor), its usage shows up under that person's name.

ciblame answers the questions finance will eventually ask:

- **Which agents are using our CI, and how much does each cost?**
- **How much CI went to agent PRs that were never merged?**
- **How much CI does it take to land one agent PR vs. one human PR?**

## Quick start

Try it without installing anything (needs Node.js):

```sh
npx ciblame --demo             # sample report, no token needed
npx ciblame your-org/your-repo # last 30 days of a real repo
```

Or install it with `npm install -g ciblame`, or download a binary for Linux, macOS or Windows (x64 and arm64) from the [releases page](https://github.com/amlaban65/ciblame/releases). On Linux:

```sh
curl -fsSL https://github.com/amlaban65/ciblame/releases/download/v0.1.0/ciblame_0.1.0_linux_amd64.tar.gz | tar xz ciblame
./ciblame --demo                # sample report, no token needed
./ciblame your-org/your-repo    # last 30 days of a real repo
```

With Go 1.23 or newer:

```sh
go run github.com/amlaban65/ciblame/cmd/ciblame@latest --demo   # try it without installing
go install github.com/amlaban65/ciblame/cmd/ciblame@latest
```

To get a report every week without installing anything, use the [GitHub Action](#run-it-weekly-in-github-actions).

ciblame finds a token in `GITHUB_TOKEN`, `GH_TOKEN`, or your [GitHub CLI](https://cli.github.com) login. It only reads: Actions runs and jobs, pull requests, and repository metadata.

```sh
ciblame acme/web acme/api --since 7d                 # several repos, last week
ciblame --org acme --since 2026-10-01 --until 2026-11-01
ciblame acme/web --by workflow                       # which workflows burn the minutes
ciblame acme/web --format markdown >> "$GITHUB_STEP_SUMMARY"
ciblame acme/web --format csv --records > runs.csv   # one row per run, for your own analysis
ciblame why https://github.com/acme/web/actions/runs/123456789
ciblame why https://github.com/acme/web/pull/4149
```

## What you get

| Column | Meaning |
|---|---|
| **MINUTES** | Billable minutes: every job of every attempt, each rounded up to a whole minute, the way GitHub bills them. Re-runs count. |
| **EST $** | Minutes × [list price](#cost-estimates) for the runner each job used. |
| **FAIL** | Share of runs whose final conclusion was a failure. |
| **MERGED** | Merged / total pull requests behind those runs. |
| **MIN/MERGE** | CI minutes per merged PR, *including* minutes spent on PRs that were abandoned. In other words, what it really costs to land one change. |
| **UNMERGED** | Minutes spent on PRs that were closed without merging. |

Below the table, ciblame writes plain-language findings you can paste into a channel: agents' share of CI, the biggest consumer, minutes lost to abandoned PRs, CI per merged PR versus humans, and how much of the agents' time was the agent itself running on Actions (Copilot sessions and reviews) rather than CI on its changes.

Humans are collapsed into a single row by default. This is a report about tools, not a leaderboard of people. Use `--per-human` if you want the breakdown.

## How attribution works

Each run is attributed using the strongest signal available, in this order:

1. **Workflow path** of an agent runtime: `dynamic/agents/copilot-pull-request-reviewer`, any other `dynamic/agents/*`, Copilot coding-agent sessions, agentic workflows (`*.lock.yml`)
2. **Scheduled runs** go to "scheduled workflows", not to whoever last edited the cron line
3. **An agent's own account** as PR author or run actor (`Copilot`, `claude[bot]`, `devin-ai-integration[bot]`, …)
4. **PR body markers** (`Generated with Claude Code`, agentic-workflow markers)
5. **Commit trailers and e-mails** (`Co-authored-by: Copilot <…>`, `noreply@anthropic.com`, `cursoragent@cursor.com`)
6. **Branch prefixes** (`copilot/`, `claude/`, `codex/`, `cursor/`, `devin/`, `dependabot/`)
7. **Known bots**, then any other `[bot]` account
8. **The human** who owns the change (the PR author, not whoever happened to push)

Steps 4–6 catch agents that work *through a person's account*; that person is recorded as the agent's operator (shown by `ciblame why` and in `--records` output). Runs are linked to their pull request even when GitHub leaves the run's PR list empty (fork PRs, merge queues, `refs/pull/N/head` dynamic runs), so CI on an agent's PR counts against the agent.

Not sure about a run? `ciblame why <run-url>` prints the evidence. Prefer a pull request? `ciblame why <pull-url>` attributes the PR once and lists every run on its head branch with minutes and estimated cost:

```text
$ ciblame why --demo
Run 9100000544 · CI (.github/workflows/ci.yml) · pull_request · failure · 2 attempts

Attributed to  Claude Code (agent)
Evidence       PR body contains "Generated with [Claude Code"
Operator       maya-chen (the account the agent worked through)
Pull request   #4149 by maya-chen (merged)
Run actor      maya-chen (User)
Branch         feat/remove-price-rounding
```

### Recognised agents and bots

| Agent / bot | Detected by |
|---|---|
| GitHub Copilot (coding agent, co-authored commits) | login, workflow path, trailer, e-mail, branch |
| Copilot code review | workflow path |
| Code scanning AI (GHAS) | workflow path, login |
| Claude Code | login, e-mail, PR body, branch |
| OpenAI Codex | login, branch |
| Cursor agent | login, e-mail, branch |
| Devin · Jules · Amazon Q Developer | login (+ branch for Devin) |
| Aider | e-mail |
| GitHub Agentic Workflows | workflow path, PR body |
| Dependabot · Renovate · github-actions · pre-commit.ci · release-please · Mergify | login, branch, workflow path |

Run `ciblame agents` for the current list. Signatures live in one JSON file, and **[adding an agent](CONTRIBUTING.md#adding-or-fixing-an-agent-signature) is the easiest way to contribute.** You can also pass your own with `--signatures my-agents.json` (for in-house bots); they take priority over the built-ins.

## Run it weekly in GitHub Actions

```yaml
# .github/workflows/ci-cost.yml
name: CI cost report
on:
  schedule: [{ cron: "17 8 * * 1" }] # Mondays
  workflow_dispatch:
permissions:
  actions: read
  pull-requests: read
  contents: read
jobs:
  ciblame:
    runs-on: ubuntu-latest
    steps:
      - uses: amlaban65/ciblame@main
        with:
          since: 7d
```

The report lands in the run's job summary; the action also exposes `report` (Markdown) and `json` file paths as outputs for posting to Slack or an issue. The default `GITHUB_TOKEN` can only read the current repository, so for `org:` reports pass a fine-grained token with read access to Actions and pull requests. See [`examples/`](examples/).

## Flags

```text
--since 30d          window start: 7d, 12h, 2w (counted back from --until), 2026-10-01, RFC 3339
--until now          window end
--org NAME           every non-archived repo pushed to during the window (--include-inactive for all)
--by actor           group by actor, kind, workflow or repo
--format table       table, markdown, json or csv (--records adds one row per run)
--per-human          list people separately
--fast               skip per-run job lookups; estimate from run duration (fewer API calls)
--rate LABEL=USD     price a runner label, e.g. --rate macos-15-xlarge=0.10 (repeatable)
--self-hosted-rate   per-minute cost to assign to self-hosted runners (default 0)
--signatures FILE    extra agent/bot signatures
--api-url URL        GitHub Enterprise Server: https://HOST/api/v3
--top 15             rows to show before folding the rest into one
--no-cache           don't use the on-disk cache
```

## Cost estimates

Costs use GitHub's list prices for standard 2-core hosted runners as of January 2026: **Linux $0.006/min, Windows $0.010/min, macOS $0.062/min**. Larger Linux and Windows runners are scaled by core count. They are estimates, not your invoice:

- Included plan minutes and discounts aren't subtracted.
- Public repositories don't pay for standard hosted runners; their costs are shown as list-price equivalents.
- Self-hosted runners are free on GitHub's side and priced at `$0` unless you set `--self-hosted-rate`.
- Labels ciblame can't price (custom larger runners, GPU, macOS large/xlarge) are listed under the table with their minutes so you can add `--rate` overrides.

## Accuracy and limits

- **Agents that leave no trace can't be detected.** If an agent commits as a person with no trailer, PR marker or recognisable branch, its work counts as human. Most agents leave at least one signal by default.
- **GitHub returns at most 1,000 runs per filtered query.** ciblame splits the time window until each slice fits, down to one hour; a single hour with more than 1,000 runs is reported as truncated.
- **API usage** is about one request per run (for its jobs) plus a few pages of runs and pull requests. Jobs of completed runs and closed pull requests are cached in your user cache directory, so re-running a report is cheap. `--fast` skips job lookups entirely.
- In-progress runs are skipped and counted under the table.

## Privacy

ciblame runs locally and talks only to the GitHub API you point it at. Nothing is sent anywhere else. Reports collapse people into one "humans" row unless you ask otherwise.

## Contributing

Issues and pull requests are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md). Good first contributions:

- Add or correct an agent signature (a JSON entry plus a test case).
- Report a run that was attributed wrongly (the output of `ciblame why <url>` helps).
- Roadmap items: budget thresholds that fail a workflow (`--max-agent-share`), GitLab CI and Buildkite sources, a `gh` extension, and Homebrew packaging.

## License

[MIT](LICENSE)
