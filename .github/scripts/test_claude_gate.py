"""Unit tests for claude_gate.py, with the GitHub API mocked.

Run: python3 -m unittest discover -s .github/scripts -p 'test_*.py'
"""

from __future__ import annotations

import datetime as dt
import io
import json
import os
import sys
import tempfile
import unittest
import urllib.error
import urllib.parse
from contextlib import redirect_stdout
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))

import claude_gate  # noqa: E402

REPO = "owner/repo"
WORKFLOWS = ["claude-issue-analyst.yml", "claude-pr-review.yml"]
MODEL_JOBS = ["analyze", "review"]
NOW = dt.datetime(2026, 11, 5, 12, 0, tzinfo=dt.timezone.utc)
CURRENT = 1000


def issue_event(login="stranger", association="NONE"):
    return {"action": "opened", "issue": {"number": 7, "user": {"login": login}, "author_association": association}}


def pr_event(login="stranger", association="FIRST_TIME_CONTRIBUTOR"):
    return {"action": "opened", "pull_request": {"number": 8, "user": {"login": login}, "author_association": association}}


def comment_event(login, association, issue_author="stranger"):
    return {
        "action": "created",
        "issue": {"number": 7, "user": {"login": issue_author}, "author_association": "NONE"},
        "comment": {"user": {"login": login}, "author_association": association},
    }


class FakeAPI:
    """Serves workflow runs and their jobs; records every path asked for."""

    def __init__(self, runs_by_workflow=None, jobs_by_run=None, fail=False):
        self.runs = runs_by_workflow or {}
        self.jobs = jobs_by_run or {}
        self.fail = fail
        self.calls: list[str] = []

    def __call__(self, path: str) -> dict:
        self.calls.append(path)
        if self.fail:
            raise urllib.error.URLError("boom")
        if "/actions/workflows/" in path:
            wf = path.split("/actions/workflows/")[1].split("/")[0]
            page = int(urllib.parse.parse_qs(urllib.parse.urlsplit(path).query)["page"][0])
            runs = self.runs.get(wf, [])
            return {"workflow_runs": runs[(page - 1) * 100 : page * 100]}
        run_id = int(path.split("/actions/runs/")[1].split("/")[0])
        return {"jobs": self.jobs.get(run_id, [])}


def finished_outsider_run(run_id, login, model="analyze", model_conclusion="success"):
    run = {"id": run_id, "status": "completed", "conclusion": "success", "actor": {"login": login}}
    jobs = [
        {"name": f"gate (outsider {login})", "status": "completed", "conclusion": "success"},
        {"name": model, "status": "completed", "conclusion": model_conclusion},
        {"name": "post", "status": "completed", "conclusion": "success"},
    ]
    return run, jobs


def build(*entries):
    """entries: (workflow, run, jobs) -> FakeAPI."""
    runs, jobs = {}, {}
    for wf, run, run_jobs in entries:
        runs.setdefault(wf, []).append(run)
        jobs[run["id"]] = run_jobs
    return FakeAPI(runs, jobs)


def decide(event, api, run_id=CURRENT, limits=None):
    return claude_gate.decide(event, api, REPO, run_id, WORKFLOWS, MODEL_JOBS, limits or claude_gate.Limits(), now=NOW)


class GateTest(unittest.TestCase):
    def test_under_quota_runs(self):
        run, jobs = finished_outsider_run(10, "stranger")
        d = decide(issue_event(), build((WORKFLOWS[0], run, jobs)))
        self.assertTrue(d.run)
        self.assertTrue(d.outsider)
        self.assertEqual(d.reason, "under-quota")
        self.assertEqual(d.counts["author"], 1)

    def test_at_author_quota_skips_across_both_workflows(self):
        a, aj = finished_outsider_run(10, "stranger", "analyze")
        b, bj = finished_outsider_run(11, "Stranger", "review")  # logins are case-insensitive
        d = decide(pr_event(), build((WORKFLOWS[0], a, aj), (WORKFLOWS[1], b, bj)))
        self.assertFalse(d.run)
        self.assertEqual(d.reason, "author-quota")
        self.assertEqual(d.counts["author"], 2)

    def test_reporter_reply_counts_as_outsider(self):
        a, aj = finished_outsider_run(10, "stranger")
        b, bj = finished_outsider_run(11, "stranger")
        d = decide(comment_event("stranger", "NONE"), build((WORKFLOWS[0], a, aj), (WORKFLOWS[0], b, bj)))
        self.assertFalse(d.run)
        self.assertEqual(d.reason, "author-quota")

    def test_maintainer_runs_without_api_calls(self):
        a, aj = finished_outsider_run(10, "Dv2Corn")
        b, bj = finished_outsider_run(11, "Dv2Corn")
        for event in (
            comment_event("Dv2Corn", "OWNER"),  # @claude analyze / @claude review
            comment_event("helper", "COLLABORATOR"),
            issue_event("Dv2Corn", "OWNER"),
            pr_event("member", "MEMBER"),
        ):
            api = build((WORKFLOWS[0], a, aj), (WORKFLOWS[0], b, bj))
            d = decide(event, api)
            self.assertTrue(d.run, event)
            self.assertFalse(d.outsider)
            self.assertEqual(api.calls, [])

    def test_repository_cap_skips(self):
        entries = []
        for i in range(15):
            run, jobs = finished_outsider_run(100 + i, f"user{i}")
            entries.append((WORKFLOWS[i % 2], run, jobs))
        d = decide(issue_event("newcomer"), build(*entries))
        self.assertFalse(d.run)
        self.assertEqual(d.reason, "repository-cap")
        self.assertEqual(d.counts["repository"], 15)

    def test_below_repository_cap_runs(self):
        entries = []
        for i in range(14):
            run, jobs = finished_outsider_run(100 + i, f"user{i}")
            entries.append((WORKFLOWS[0], run, jobs))
        self.assertTrue(decide(issue_event("newcomer"), build(*entries)).run)

    def test_maintainer_and_skipped_runs_do_not_count(self):
        maint = {"id": 20, "status": "completed", "conclusion": "success", "actor": {"login": "Dv2Corn"}}
        maint_jobs = [
            {"name": "gate (maintainer)", "status": "completed", "conclusion": "success"},
            {"name": "review", "status": "completed", "conclusion": "success"},
        ]
        refused, refused_jobs = finished_outsider_run(21, "stranger", "analyze", "skipped")  # gate said no
        filtered = {"id": 22, "status": "completed", "conclusion": "success", "actor": {"login": "stranger"}}
        filtered_jobs = [{"name": "gate (outsider stranger)", "status": "completed", "conclusion": "skipped"}]
        all_skipped = {"id": 23, "status": "completed", "conclusion": "skipped", "actor": {"login": "stranger"}}
        api = build(
            (WORKFLOWS[1], maint, maint_jobs),
            (WORKFLOWS[0], refused, refused_jobs),
            (WORKFLOWS[0], filtered, filtered_jobs),
            (WORKFLOWS[0], all_skipped, []),
        )
        d = decide(issue_event(), api)
        self.assertTrue(d.run)
        self.assertEqual(d.counts, {"author": 0, "repository": 0, "in_flight": 0})
        self.assertFalse(any("/runs/23/jobs" in c for c in api.calls), "a skipped run needs no job lookup")

    def test_later_and_current_runs_are_ignored(self):
        a, aj = finished_outsider_run(CURRENT, "stranger")
        b, bj = finished_outsider_run(CURRENT + 5, "stranger")
        self.assertTrue(decide(issue_event(), build((WORKFLOWS[0], a, aj), (WORKFLOWS[0], b, bj))).run)

    def test_runs_in_flight_count_and_cap_concurrency(self):
        pending = {"id": 30, "status": "in_progress", "conclusion": None, "actor": {"login": "x"}}
        pending_jobs = [
            {"name": "gate (outsider x)", "status": "completed", "conclusion": "success"},
            {"name": "analyze", "status": "queued", "conclusion": None},
        ]
        brand_new = {"id": 31, "status": "queued", "conclusion": None, "actor": {"login": "y"}}
        d = decide(pr_event("z"), build((WORKFLOWS[0], pending, pending_jobs), (WORKFLOWS[1], brand_new, [])))
        self.assertFalse(d.run)
        self.assertEqual(d.reason, "busy")
        self.assertEqual(d.counts["in_flight"], 2)

    def test_unlisted_gate_counts_against_its_actor(self):
        brand_new = {"id": 31, "status": "queued", "conclusion": None, "triggering_actor": {"login": "stranger"}}
        a, aj = finished_outsider_run(10, "stranger")
        d = decide(issue_event(), build((WORKFLOWS[0], brand_new, []), (WORKFLOWS[0], a, aj)))
        self.assertFalse(d.run)
        self.assertEqual(d.reason, "author-quota")

    def test_api_error_fails_closed(self):
        d = decide(issue_event(), FakeAPI(fail=True))
        self.assertFalse(d.run)
        self.assertTrue(d.reason.startswith("api-error"))

    def test_too_many_runs_fails_closed(self):
        entries = []
        for i in range(claude_gate.MAX_JOB_LOOKUPS + 1):
            run = {"id": 100 + i, "status": "completed", "conclusion": "success", "actor": {"login": "u"}}
            entries.append((WORKFLOWS[0], run, []))
        d = decide(issue_event(), build(*entries))
        self.assertFalse(d.run)
        self.assertTrue(d.reason.startswith("api-error"))

    def test_window_and_pagination(self):
        entries = []
        for i in range(150):
            run = {"id": 100 + i, "status": "completed", "conclusion": "skipped", "actor": {"login": "u"}}
            entries.append((WORKFLOWS[0], run, []))
        api = build(*entries)
        self.assertTrue(decide(issue_event(), api).run)
        listed = [c for c in api.calls if "/actions/workflows/" in c]
        self.assertIn("created=%3E%3D2026-11-04T12%3A00%3A00Z", listed[0])
        self.assertEqual(len([c for c in listed if WORKFLOWS[0] in c]), 2)

    def test_main_writes_outputs_and_stays_green(self):
        a, aj = finished_outsider_run(10, "stranger")
        b, bj = finished_outsider_run(11, "stranger")
        api = build((WORKFLOWS[0], a, aj), (WORKFLOWS[1], b, bj))
        with tempfile.TemporaryDirectory() as tmp:
            event_path = Path(tmp, "event.json")
            event_path.write_text(json.dumps(issue_event()))
            out, summary = Path(tmp, "out"), Path(tmp, "summary")
            env = {
                "GITHUB_EVENT_PATH": str(event_path),
                "GITHUB_OUTPUT": str(out),
                "GITHUB_STEP_SUMMARY": str(summary),
                "GITHUB_REPOSITORY": REPO,
                "GITHUB_RUN_ID": str(CURRENT),
                "WORKFLOWS": ",".join(WORKFLOWS),
                "MODEL_JOBS": ",".join(MODEL_JOBS),
            }
            with mock.patch.dict(os.environ, env), mock.patch.object(claude_gate, "github_fetch", lambda *_: api):
                buf = io.StringIO()
                with redirect_stdout(buf):
                    rc = claude_gate.main([])
            self.assertEqual(rc, 0)
            self.assertEqual(out.read_text(), "run=false\noutsider=true\nreason=author-quota\n")
            self.assertIn("::notice::No automatic Claude run", buf.getvalue())
            self.assertIn("already had 2 automatic Claude runs", summary.read_text())

    def test_gate_name_pattern_matches_workflow_names(self):
        self.assertIsNone(claude_gate.GATE_NAME.match("gate (outsider )"))
        self.assertEqual(claude_gate.GATE_NAME.match("gate (outsider a-b1)").group("login"), "a-b1")
        self.assertIsNone(claude_gate.GATE_NAME.match("gate (maintainer)").group("login"))
        self.assertIsNone(claude_gate.GATE_NAME.match("gate (outsider x) extra"))


if __name__ == "__main__":
    unittest.main()
