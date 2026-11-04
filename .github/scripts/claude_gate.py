#!/usr/bin/env python3
"""Decide whether an automatic Claude run may start.

The Claude workflows spend the owner's subscription. An issue, a reply under
`clarification needed` or a fork's pull request from someone who is not a
maintainer starts a run with no maintainer in the loop, so those runs are
capped here, in a job that holds no Claude secret and only a read-only token:

- each outsider gets at most PER_AUTHOR automatic runs in WINDOW_HOURS,
  across every workflow in WORKFLOWS;
- all outsiders together get at most REPO_CAP runs in that window;
- at most IN_FLIGHT_CAP outsider runs are running or waiting at once (one
  running, one pending: a GitHub concurrency group keeps no longer queue).

Maintainers (OWNER, MEMBER, COLLABORATOR) are never limited, and neither is
an `@claude review` / `@claude analyze` request, which only they can make.

Earlier runs are classified by the name of their gate job, which the
workflows set from the event: `gate (maintainer)` or `gate (outsider LOGIN)`.
Only runs created before this one count, so two gates started at the same
moment cannot both take the last slot. Anything that cannot be classified
counts as an outsider run, and an API error refuses the run: the gate fails
closed and still exits 0, so the workflow stays green.

Environment: GH_TOKEN, GITHUB_REPOSITORY, GITHUB_RUN_ID, GITHUB_EVENT_PATH,
GITHUB_OUTPUT (optional), GITHUB_API_URL (optional), WORKFLOWS (comma list of
workflow file names), MODEL_JOBS (comma list of the job names that run the
model). The limits can be overridden with PER_AUTHOR, REPO_CAP,
IN_FLIGHT_CAP and WINDOW_HOURS. `--dry-run` prints the decision instead of
writing it to GITHUB_OUTPUT.
"""

from __future__ import annotations

import datetime as dt
import json
import os
import re
import sys
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass, field
from typing import Callable, Iterable

MAINTAINERS = frozenset({"OWNER", "MEMBER", "COLLABORATOR"})
GATE_NAME = re.compile(r"^gate \((?:maintainer|outsider (?P<login>[A-Za-z0-9-]{1,39}))\)$")

# Hard ceilings on the API calls one gate makes. Beyond them the window is
# busier than any honest day on this repository, so the run is refused.
MAX_RUNS_LISTED = 300
MAX_JOB_LOOKUPS = 60


class GateError(Exception):
    """The GitHub API could not answer; the caller refuses the run."""


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


def event_author(event: dict) -> tuple[str, str]:
    """(login, author_association) of whoever the run acts for.

    The order matches the workflows' gate job name: a comment first (a
    reporter's reply or a maintainer's request), then the pull request, then
    the issue.
    """
    for key in ("comment", "pull_request", "issue"):
        obj = event.get(key)
        if isinstance(obj, dict) and obj.get("user"):
            return str(obj["user"].get("login") or ""), str(obj.get("author_association") or "")
    return "", ""


Fetch = Callable[[str], dict]


def list_runs(fetch: Fetch, repo: str, workflows: Iterable[str], since: str) -> list[dict]:
    runs: list[dict] = []
    created = urllib.parse.quote(f">={since}", safe="")
    for wf in workflows:
        page = 1
        while True:
            data = fetch(f"/repos/{repo}/actions/workflows/{wf}/runs?per_page=100&page={page}&created={created}")
            batch = data.get("workflow_runs") or []
            runs.extend(batch)
            if len(runs) > MAX_RUNS_LISTED:
                raise GateError(f"more than {MAX_RUNS_LISTED} Claude workflow runs in the window")
            if len(batch) < 100:
                break
            page += 1
    return runs


def classify(run: dict, jobs: list[dict], model_jobs: frozenset) -> tuple[bool, str, bool, bool]:
    """(is_outsider_candidate, login, counts_against_quota, in_flight)."""
    completed = run.get("status") == "completed"
    gate = next((j for j in jobs if str(j.get("name", "")).startswith("gate")), None)
    if gate is None:
        if completed:
            return False, "", False, False
        # A run so new its jobs are not listed yet: count it, conservatively,
        # as an outsider run by whoever triggered it.
        login = str((run.get("triggering_actor") or run.get("actor") or {}).get("login") or "")
        return True, login, True, True
    if gate.get("conclusion") == "skipped":
        return False, "", False, False
    m = GATE_NAME.match(str(gate.get("name", "")))
    if m and not m.group("login"):
        return False, "", False, False  # a maintainer's run
    if m:
        login = m.group("login")
    else:
        login = str((run.get("triggering_actor") or run.get("actor") or {}).get("login") or "")
    model = next((j for j in jobs if j.get("name") in model_jobs), None)
    if model is None:
        if completed:
            return True, login, False, False  # the gate stopped it, or it failed first
        return True, login, True, True
    if model.get("status") != "completed":
        return True, login, True, True
    if model.get("conclusion") == "skipped":
        return True, login, False, False
    return True, login, True, False


def decide(
    event: dict,
    fetch: Fetch,
    repo: str,
    run_id: int,
    workflows: list[str],
    model_jobs: list[str],
    limits: Limits,
    now: dt.datetime | None = None,
) -> Decision:
    login, association = event_author(event)
    if association in MAINTAINERS:
        return Decision(True, False, "maintainer", login)
    now = now or dt.datetime.now(dt.timezone.utc)
    since = (now - dt.timedelta(hours=limits.window_hours)).strftime("%Y-%m-%dT%H:%M:%SZ")
    try:
        runs = list_runs(fetch, repo, workflows, since)
        earlier = [r for r in runs if int(r.get("id", 0)) < run_id and r.get("conclusion") != "skipped"]
        earlier.sort(key=lambda r: int(r["id"]), reverse=True)
        if len(earlier) > MAX_JOB_LOOKUPS:
            raise GateError(f"more than {MAX_JOB_LOOKUPS} active Claude workflow runs in the window")
        mine = total = flying = 0
        for run in earlier:
            jobs = fetch(f"/repos/{repo}/actions/runs/{run['id']}/jobs?per_page=100").get("jobs") or []
            outsider, who, counted, in_flight = classify(run, jobs, frozenset(model_jobs))
            if not (outsider and counted):
                continue
            total += 1
            flying += in_flight
            if who.lower() == login.lower():
                mine += 1
    except (GateError, urllib.error.URLError, OSError, ValueError, KeyError, TypeError) as exc:
        return Decision(False, True, f"api-error: {exc}", login)
    counts = {"author": mine, "repository": total, "in_flight": flying}
    if mine >= limits.per_author:
        return Decision(False, True, "author-quota", login, counts)
    if total >= limits.repo_cap:
        return Decision(False, True, "repository-cap", login, counts)
    if flying >= limits.in_flight:
        return Decision(False, True, "busy", login, counts)
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
        [w.strip() for w in os.environ["WORKFLOWS"].split(",") if w.strip()],
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
        }.get(decision.reason, "the run quota could not be checked, so it was refused")
        print(f"::notice::No automatic Claude run: {limit}. A maintainer can still ask for one.")
        step_summary = os.environ.get("GITHUB_STEP_SUMMARY")
        if step_summary:
            with open(step_summary, "a", encoding="utf-8") as fh:
                fh.write(f"No automatic Claude run: {limit}. A maintainer can still ask for one.\n\n`{summary}`\n")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
