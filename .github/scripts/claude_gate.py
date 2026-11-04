#!/usr/bin/env python3
"""Decide whether an automatic Claude run may start.

The Claude workflows spend the owner's subscription. An issue, a reply under
`clarification needed` or a fork's pull request from someone who is not a
maintainer starts a run with no maintainer in the loop, so those runs are
capped here, in a job that holds no Claude secret and only a read-only token:

- each outsider gets at most PER_AUTHOR model runs in WINDOW_HOURS, across
  both Claude workflows;
- all outsiders together get at most REPO_CAP model runs in that window;
- at most IN_FLIGHT_CAP outsider model runs are running or waiting at once;
- an outsider run never queues behind two runs of the same issue or pull
  request, nor behind a maintainer's run created after it: the per-item
  concurrency group keeps one pending run, and the newest arrival cancels it,
  so this keeps an outsider from cancelling a maintainer's waiting run.

Maintainers (OWNER, MEMBER, COLLABORATOR) are never limited, and neither is
an `@claude review` / `@claude analyze` request, which only they can make.

Only a run whose model job started counts. Runs that spent nothing never use
up anything: a comment that triggered nothing (the whole run is `skipped`),
a run the gate refused (its model job is `skipped`), a model job cancelled
before it started. Earlier runs are classified by their title, which both
workflows set with `run-name:` from the event:
`Claude <analyst|review> #<number> (maintainer|outsider <login>)`.
Their jobs are looked up only where the title cannot settle it, and never
for an author who already used up the quota, or once the repository cap is
reached: from then on every later outsider run was refused.

Only an API error refuses a run without a count (fail closed); the script
still exits 0, so the workflow stays green.

Environment: GH_TOKEN, GITHUB_REPOSITORY, GITHUB_RUN_ID, GITHUB_EVENT_PATH,
GITHUB_OUTPUT and GITHUB_STEP_SUMMARY (optional), GITHUB_API_URL (optional),
KIND (`analyst` or `review`: this workflow), WORKFLOWS (`kind=file` pairs,
comma separated), MODEL_JOBS (the model job names, comma separated). The
limits can be overridden with PER_AUTHOR, REPO_CAP, IN_FLIGHT_CAP and
WINDOW_HOURS. `--dry-run` prints the decision without writing outputs.
"""

from __future__ import annotations

import datetime as dt
import json
import os
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from collections import Counter
from dataclasses import dataclass, field
from typing import Callable

MAINTAINERS = frozenset({"OWNER", "MEMBER", "COLLABORATOR"})
TITLE = re.compile(
    r"^Claude (?P<kind>analyst|review) #(?P<number>[0-9]{1,10}) "
    r"\((?:maintainer|outsider (?P<login>[A-Za-z0-9-]{1,39}(?:\[bot\])?))\)$"
)
# GitHub returns at most 1000 runs for a query with `created=`. Past that the
# listing switches to one query per status that is not `skipped`, so a flood
# of comments that triggered nothing cannot hide the runs that spent.
LIST_LIMIT = 1000
SPENDING_STATUSES = (
    "queued", "requested", "waiting", "pending", "in_progress",
    "success", "failure", "cancelled", "timed_out",
)


@dataclass
class Limits:
    per_author: int = 2
    repo_cap: int = 15
    in_flight: int = 2
    window_hours: int = 24


@dataclass
class Decision:
    run: bool
    outsider: bool
    reason: str
    author: str = ""
    counts: dict = field(default_factory=dict)


Fetch = Callable[[str], dict]


def event_author(event: dict) -> tuple[str, str]:
    """(login, author_association) of whoever the run acts for.

    The order matches the workflows' `run-name:`: a comment first (a reporter's
    reply or a maintainer's request), then the pull request, then the issue.
    """
    for key in ("comment", "pull_request", "issue"):
        obj = event.get(key)
        if isinstance(obj, dict) and obj.get("user"):
            return str(obj["user"].get("login") or ""), str(obj.get("author_association") or "")
    return "", ""


def event_number(event: dict) -> str:
    for key in ("pull_request", "issue"):
        obj = event.get(key)
        if isinstance(obj, dict) and obj.get("number") is not None:
            return str(obj["number"])
    return ""


def _list(fetch: Fetch, base: str) -> tuple[list[dict], bool]:
    """Every run of one query, and whether GitHub held some back."""
    runs: list[dict] = []
    page = 1
    while True:
        data = fetch(f"{base}&per_page=100&page={page}")
        batch = data.get("workflow_runs") or []
        runs.extend(batch)
        total = int(data.get("total_count", len(runs)))
        if len(batch) < 100 or len(runs) >= total:
            return runs, False
        if len(runs) >= LIST_LIMIT:
            return runs, True
        page += 1


def list_runs(fetch: Fetch, repo: str, workflow: str, since: str) -> list[dict]:
    created = urllib.parse.quote(f">={since}", safe="")
    base = f"/repos/{repo}/actions/workflows/{workflow}/runs?created={created}"
    runs, truncated = _list(fetch, base)
    if not truncated:
        return runs
    seen: dict[int, dict] = {}
    for status in SPENDING_STATUSES:
        part, more = _list(fetch, f"{base}&status={status}")
        if more:
            print(f"::warning::More than {LIST_LIMIT} '{status}' runs of {workflow} in the window; counting the newest.")
        for run in part:
            seen[int(run["id"])] = run
    return list(seen.values())


def model_state(jobs: list[dict], model_jobs: frozenset, completed: bool) -> str:
    """'none' (spent nothing), 'active' (running or still to run) or 'spent'."""
    gate = next((j for j in jobs if j.get("name") == "gate"), None)
    if gate is not None and gate.get("conclusion") == "skipped":
        return "none"
    model = next((j for j in jobs if j.get("name") in model_jobs), None)
    if model is None:
        return "none" if completed else "active"
    if model.get("status") != "completed":
        return "active"
    conclusion = model.get("conclusion")
    if conclusion == "skipped":
        return "none"
    if conclusion == "cancelled" and not model.get("steps"):
        return "none"  # cancelled while it waited for its group
    return "spent"


def decide(
    event: dict,
    fetch: Fetch,
    repo: str,
    run_id: int,
    kind: str,
    workflows: dict[str, str],
    model_jobs: list[str],
    limits: Limits,
    now: dt.datetime | None = None,
    retry_delay: float = 5.0,
) -> Decision:
    login, association = event_author(event)
    if association in MAINTAINERS:
        return Decision(True, False, "maintainer", login)
    me = login.lower()
    item = (kind, event_number(event))
    now = now or dt.datetime.now(dt.timezone.utc)
    since = (now - dt.timedelta(hours=limits.window_hours)).strftime("%Y-%m-%dT%H:%M:%SZ")
    jobs_of = lambda rid: fetch(f"/repos/{repo}/actions/runs/{rid}/jobs?per_page=100").get("jobs") or []  # noqa: E731
    models = frozenset(model_jobs)

    per_author: Counter = Counter()
    total = flying = item_active = lookups = 0
    later_maintainer = False
    try:
        runs: list[dict] = []
        for wf in workflows.values():
            runs.extend(list_runs(fetch, repo, wf, since))
        runs = [r for r in runs if r.get("conclusion") != "skipped" and int(r.get("id", 0)) != run_id]
        runs.sort(key=lambda r: int(r["id"]))
        for run in runs:
            m = TITLE.match(str(run.get("display_title") or ""))
            completed = run.get("status") == "completed"
            if int(run["id"]) > run_id:
                # A maintainer's run on this item created after this one would
                # be cancelled if this one queued behind it.
                if m and not m.group("login") and not completed and (m.group("kind"), m.group("number")) == item:
                    later_maintainer = True
                continue
            if m is None:
                if completed:
                    continue  # a run from before the gate existed
                who = str((run.get("triggering_actor") or run.get("actor") or {}).get("login") or "").lower()
                per_author[who] += 1  # not classifiable yet: charge whoever started it
                continue
            run_item = (m.group("kind"), m.group("number"))
            who = (m.group("login") or "").lower()
            if not who:
                # A maintainer's run: never counted, but it holds its item.
                if not completed and run_item == item:
                    item_active += 1
                continue
            if per_author[who] >= limits.per_author:
                continue  # the gate refused it: its author was already at the quota
            if total >= limits.repo_cap:
                break  # every later outsider run was refused by the cap as well
            lookups += 1
            jobs = jobs_of(run["id"])
            if completed:
                if model_state(jobs, models, True) == "spent":
                    per_author[who] += 1
                    total += 1
                continue
            # Still running: it is either about to end having spent nothing, or holds a slot.
            if not jobs and retry_delay:
                time.sleep(retry_delay)
                lookups += 1
                jobs = jobs_of(run["id"])
            if not jobs:
                # Jobs not listed yet: charge the author and the item, not the
                # repository, so a burst of comments cannot make others wait.
                per_author[who] += 1
                if run_item == item:
                    item_active += 1
                continue
            if model_state(jobs, models, False) == "none":
                continue
            per_author[who] += 1
            total += 1
            flying += 1
            if run_item == item:
                item_active += 1
    except (urllib.error.URLError, OSError, ValueError, KeyError, TypeError) as exc:
        return Decision(False, True, f"api-error: {exc}", login)
    counts = {"author": per_author[me], "repository": total, "in_flight": flying, "item": item_active, "job_lookups": lookups}
    if per_author[me] >= limits.per_author:
        return Decision(False, True, "author-quota", login, counts)
    if total >= limits.repo_cap:
        return Decision(False, True, "repository-cap", login, counts)
    if flying >= limits.in_flight:
        return Decision(False, True, "busy", login, counts)
    if item_active >= 2 or later_maintainer:
        return Decision(False, True, "item-busy", login, counts)
    return Decision(True, True, "under-quota", login, counts)


def github_fetch(api: str, token: str) -> Fetch:
    def fetch(path: str) -> dict:
        req = urllib.request.Request(
            api.rstrip("/") + path,
            headers={
                "Accept": "application/vnd.github+json",
                "Authorization": f"Bearer {token}",
                "X-GitHub-Api-Version": "2022-11-28",
                "User-Agent": "still-ui-claude-gate",
            },
            method="GET",
        )
        with urllib.request.urlopen(req, timeout=30) as resp:
            return json.load(resp)

    return fetch


def env_int(name: str, default: int) -> int:
    value = os.environ.get(name, "").strip()
    return int(value) if value else default


def parse_workflows(value: str) -> dict[str, str]:
    pairs = {}
    for part in value.split(","):
        kind, _, wf = part.strip().partition("=")
        if kind and wf:
            pairs[kind] = wf
    return pairs


def main(argv: list[str]) -> int:
    dry_run = "--dry-run" in argv
    with open(os.environ["GITHUB_EVENT_PATH"], encoding="utf-8") as fh:
        event = json.load(fh)
    limits = Limits(
        per_author=env_int("PER_AUTHOR", 2),
        repo_cap=env_int("REPO_CAP", 15),
        in_flight=env_int("IN_FLIGHT_CAP", 2),
        window_hours=env_int("WINDOW_HOURS", 24),
    )
    decision = decide(
        event,
        github_fetch(os.environ.get("GITHUB_API_URL", "https://api.github.com"), os.environ.get("GH_TOKEN", "")),
        os.environ["GITHUB_REPOSITORY"],
        int(os.environ["GITHUB_RUN_ID"]),
        os.environ["KIND"],
        parse_workflows(os.environ["WORKFLOWS"]),
        [j.strip() for j in os.environ["MODEL_JOBS"].split(",") if j.strip()],
        limits,
    )
    summary = (
        f"run={str(decision.run).lower()} outsider={str(decision.outsider).lower()} "
        f"reason={decision.reason} author={decision.author or '-'} counts={json.dumps(decision.counts, sort_keys=True)}"
    )
    print(summary)
    if dry_run:
        return 0
    out = os.environ.get("GITHUB_OUTPUT")
    if out:
        with open(out, "a", encoding="utf-8") as fh:
            fh.write(f"run={str(decision.run).lower()}\n")
            fh.write(f"outsider={str(decision.outsider).lower()}\n")
            fh.write(f"reason={decision.reason.splitlines()[0][:200]}\n")
    if not decision.run:
        limit = {
            "author-quota": f"its author already had {limits.per_author} automatic Claude runs in {limits.window_hours} h",
            "repository-cap": f"outsiders already had {limits.repo_cap} automatic Claude runs in {limits.window_hours} h",
            "busy": f"{limits.in_flight} outsider Claude runs are already running or waiting",
            "item-busy": "other Claude runs on this issue or pull request are already running or waiting",
        }.get(decision.reason, "the GitHub API did not answer, so the run quota could not be checked")
        print(f"::notice::No automatic Claude run: {limit}. A maintainer can still ask for one.")
        step_summary = os.environ.get("GITHUB_STEP_SUMMARY")
        if step_summary:
            with open(step_summary, "a", encoding="utf-8") as fh:
                fh.write(f"No automatic Claude run: {limit}. A maintainer can still ask for one.\n\n`{summary}`\n")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
