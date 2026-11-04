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
WORKFLOWS = {"analyst": "claude-issue-analyst.yml", "review": "claude-pr-review.yml"}
FILE = {v: k for k, v in WORKFLOWS.items()}
MODEL = {"analyst": "analyze", "review": "review"}
MODEL_JOBS = ["analyze", "review"]
NOW = dt.datetime(2026, 11, 5, 12, 0, tzinfo=dt.timezone.utc)


def issue_event(login="stranger", association="NONE", number=7):
    return {"action": "opened", "issue": {"number": number, "user": {"login": login}, "author_association": association}}


def pr_event(login="stranger", association="FIRST_TIME_CONTRIBUTOR", number=8):
    return {"action": "opened", "pull_request": {"number": number, "user": {"login": login}, "author_association": association}}


def comment_event(login, association, number=7, issue_author="stranger"):
    return {
        "action": "created",
        "issue": {"number": number, "user": {"login": issue_author}, "author_association": "NONE"},
        "comment": {"user": {"login": login}, "author_association": association},
    }


def title(kind, number, login=None):
    who = f"outsider {login}" if login else "maintainer"
    return f"Claude {kind} #{number} ({who})"


class FakeAPI:
    """Workflow runs and their jobs, served like the REST API; records every GET."""

    def __init__(self, fail=False):
        self.runs: dict[str, list[dict]] = {wf: [] for wf in WORKFLOWS.values()}
        self.jobs: dict[int, list[dict]] = {}
        self.fail = fail
        self.calls: list[str] = []
        self.next_id = 100

    # -- building state ------------------------------------------------------
    def add(self, kind, number, login, state="spent", status="completed", run_id=None, display=None, jobs=None):
        """state: spent | refused (gate ran, model skipped) | skipped (whole run) |
        cancelled-early (model cancelled before it started) | active | unlisted."""
        run_id = run_id or self.next_id
        self.next_id = max(self.next_id, run_id) + 1
        conclusion = None if status != "completed" else ("skipped" if state == "skipped" else "success")
        run = {
            "id": run_id,
            "status": status,
            "conclusion": conclusion,
            "display_title": display if display is not None else title(kind, number, login),
            "actor": {"login": login or "Dv2Corn"},
        }
        self.runs[WORKFLOWS[kind]].append(run)
        model = MODEL[kind]
        if jobs is not None:
            self.jobs[run_id] = jobs
        elif state == "skipped":
            self.jobs[run_id] = [{"name": "gate", "status": "completed", "conclusion": "skipped"}]
        elif state == "spent":
            self.jobs[run_id] = [
                {"name": "gate", "status": "completed", "conclusion": "success"},
                {"name": model, "status": "completed", "conclusion": "success", "steps": [{"name": "Set up job"}]},
            ]
        elif state == "refused":
            self.jobs[run_id] = [
                {"name": "gate", "status": "completed", "conclusion": "success"},
                {"name": model, "status": "completed", "conclusion": "skipped", "steps": []},
            ]
        elif state == "cancelled-early":
            self.jobs[run_id] = [
                {"name": "gate", "status": "completed", "conclusion": "success"},
                {"name": model, "status": "completed", "conclusion": "cancelled", "steps": []},
            ]
        elif state == "active":
            self.jobs[run_id] = [
                {"name": "gate", "status": "completed", "conclusion": "success"},
                {"name": model, "status": "queued", "conclusion": None},
            ]
        elif state == "unlisted":
            self.jobs[run_id] = []
        return run_id

    # -- serving -------------------------------------------------------------
    def __call__(self, path: str) -> dict:
        self.calls.append(path)
        if self.fail:
            raise urllib.error.URLError("boom")
        parts = urllib.parse.urlsplit(path)
        q = urllib.parse.parse_qs(parts.query)
        if "/actions/workflows/" in parts.path:
            wf = parts.path.split("/actions/workflows/")[1].split("/")[0]
            runs = sorted(self.runs[wf], key=lambda r: -r["id"])  # newest first, like GitHub
            if "status" in q:
                st = q["status"][0]
                runs = [r for r in runs if r["status"] == st or r["conclusion"] == st]
            total = len(runs)
            runs = runs[: claude_gate.LIST_LIMIT]  # GitHub's ceiling with created=
            page, per = int(q["page"][0]), int(q["per_page"][0])
            return {"total_count": total, "workflow_runs": runs[(page - 1) * per : page * per]}
        run_id = int(parts.path.split("/actions/runs/")[1].split("/")[0])
        return {"jobs": self.jobs.get(run_id, [])}

    def job_lookups(self):
        return [c for c in self.calls if c.endswith("jobs?per_page=100")]


def decide(event, api, run_id=None, kind=None, limits=None):
    kind = kind or ("review" if "pull_request" in event else "analyst")
    run_id = run_id or api.next_id + 1000
    return claude_gate.decide(
        event, api, REPO, run_id, kind, WORKFLOWS, MODEL_JOBS, limits or claude_gate.Limits(), now=NOW, retry_delay=0
    )


class GateTest(unittest.TestCase):
    # -- the basic decisions ---------------------------------------------------
    def test_under_quota_runs(self):
        api = FakeAPI()
        api.add("analyst", 1, "stranger")
        d = decide(issue_event(), api)
        self.assertTrue(d.run)
        self.assertTrue(d.outsider)
        self.assertEqual(d.reason, "under-quota")
        self.assertEqual(d.counts["author"], 1)

    def test_at_author_quota_skips_across_both_workflows(self):
        api = FakeAPI()
        api.add("analyst", 1, "stranger")
        api.add("review", 2, "Stranger")  # logins are case-insensitive
        d = decide(pr_event(), api)
        self.assertFalse(d.run)
        self.assertEqual(d.reason, "author-quota")

    def test_reporter_reply_counts_as_outsider(self):
        api = FakeAPI()
        api.add("analyst", 1, "stranger")
        api.add("analyst", 2, "stranger")
        d = decide(comment_event("stranger", "NONE"), api)
        self.assertFalse(d.run)
        self.assertEqual(d.reason, "author-quota")

    def test_maintainer_runs_without_api_calls(self):
        for event in (
            comment_event("Dv2Corn", "OWNER"),  # @claude analyze / @claude review
            comment_event("helper", "COLLABORATOR"),
            issue_event("Dv2Corn", "OWNER"),
            pr_event("member", "MEMBER"),
        ):
            api = FakeAPI()
            api.add("analyst", 1, "Dv2Corn")
            d = decide(event, api)
            self.assertTrue(d.run, event)
            self.assertFalse(d.outsider)
            self.assertEqual(api.calls, [])

    def test_repository_cap_skips(self):
        api = FakeAPI()
        for i in range(15):
            api.add(["analyst", "review"][i % 2], i, f"user{i}")
        d = decide(issue_event("newcomer"), api)
        self.assertFalse(d.run)
        self.assertEqual(d.reason, "repository-cap")

    def test_below_repository_cap_runs(self):
        api = FakeAPI()
        for i in range(14):
            api.add("analyst", i, f"user{i}")
        self.assertTrue(decide(issue_event("newcomer"), api).run)

    def test_api_error_fails_closed(self):
        d = decide(issue_event(), FakeAPI(fail=True))
        self.assertFalse(d.run)
        self.assertTrue(d.reason.startswith("api-error"))

    def test_api_error_in_a_job_lookup_fails_closed(self):
        api = FakeAPI()
        api.add("analyst", 1, "someone")
        real = api.__call__

        def flaky(path):
            if "/jobs" in path:
                raise urllib.error.HTTPError(path, 502, "Bad Gateway", {}, None)
            return real(path)

        d = claude_gate.decide(issue_event(), flaky, REPO, 10_000, "analyst", WORKFLOWS, MODEL_JOBS, claude_gate.Limits(), now=NOW, retry_delay=0)
        self.assertFalse(d.run)
        self.assertTrue(d.reason.startswith("api-error"))

    # -- runs that spent nothing never count -----------------------------------
    def test_200_comment_runs_that_spent_nothing_do_not_block_an_outsider(self):
        api = FakeAPI()
        for i in range(100):  # every comment triggers both workflows
            api.add("analyst", i, f"chatter{i % 7}", state="skipped")
            api.add("review", i, f"chatter{i % 7}", state="skipped")
        d = decide(issue_event("newcomer"), api)
        self.assertTrue(d.run)
        self.assertEqual(d.reason, "under-quota")
        self.assertEqual(api.job_lookups(), [], "a wholly skipped run needs no job lookup")

    def test_refused_and_cancelled_before_start_do_not_count(self):
        api = FakeAPI()
        for _ in range(5):
            api.add("analyst", 1, "stranger", state="refused")
            api.add("review", 2, "stranger", state="cancelled-early")
        api.add("review", 3, None)  # a maintainer's finished run
        d = decide(issue_event(), api)
        self.assertTrue(d.run)
        self.assertEqual(d.counts["author"], 0)
        self.assertEqual(d.counts["repository"], 0)

    def test_61_issues_from_one_author(self):
        """Replays 61 issues one by one: the author gets 2 runs, then only
        refusals, the refusals do not pile up into a ceiling, and the next
        outsider still gets a run."""
        api = FakeAPI()
        allowed = 0
        for n in range(1, 62):
            run_id = api.next_id
            d = decide(issue_event("flooder", number=n), api, run_id=run_id)
            allowed += d.run
            api.add("analyst", n, "flooder", state="spent" if d.run else "refused", run_id=run_id)
        self.assertEqual(allowed, 2)
        api.calls.clear()
        d = decide(issue_event("someone-else", number=99), api)
        self.assertTrue(d.run)
        self.assertEqual(d.reason, "under-quota")
        # Only the flooder's two spending runs needed a look at their jobs.
        self.assertEqual(len(api.job_lookups()), 2)

    def test_listing_past_github_limit_falls_back_to_status_queries(self):
        api = FakeAPI()
        api.add("analyst", 1, "stranger")
        api.add("analyst", 2, "stranger")
        for i in range(1100):  # pushes the two spending runs past the 1000-run ceiling
            api.add("analyst", 3, "chatter", state="skipped")
        d = decide(issue_event(), api)
        self.assertFalse(d.run)
        self.assertEqual(d.reason, "author-quota")
        self.assertTrue(any("status=success" in c for c in api.calls))

    # -- runs in flight -----------------------------------------------------------
    def test_two_outsider_runs_in_flight_are_busy(self):
        api = FakeAPI()
        api.add("analyst", 1, "x", state="active", status="in_progress")
        api.add("review", 2, "y", state="active", status="in_progress")
        d = decide(issue_event("z", number=3), api)
        self.assertFalse(d.run)
        self.assertEqual(d.reason, "busy")

    def test_in_flight_comment_run_that_will_skip_does_not_count(self):
        api = FakeAPI()
        for i in range(5):
            api.add("analyst", i, "chatter", state="skipped", status="in_progress")
        self.assertTrue(decide(issue_event("z", number=3), api).run)

    def test_unlisted_jobs_charge_the_author_not_the_repository(self):
        api = FakeAPI()
        api.add("analyst", 1, "stranger")
        api.add("analyst", 2, "stranger", state="unlisted", status="queued")
        d = decide(issue_event(number=5), api)
        self.assertFalse(d.run)
        self.assertEqual(d.reason, "author-quota")
        for i in range(5):
            api.add("analyst", 10 + i, f"chatter{i}", state="unlisted", status="queued")
        d = decide(issue_event("newcomer", number=20), api)
        self.assertTrue(d.run)
        self.assertEqual(d.counts["repository"], 1)

    def test_truncated_status_listing_fails_closed(self):
        api = FakeAPI()
        for i in range(claude_gate.LIST_LIMIT + 1):
            api.add("analyst", i, f"user{i}", state="refused")  # finished runs: conclusion success
        for i in range(100):
            api.add("analyst", 5000, "chatter", state="skipped")
        d = decide(issue_event("newcomer"), api)
        self.assertFalse(d.run)
        self.assertTrue(d.reason.startswith("api-error"), d.reason)
        self.assertIn("'success'", d.reason)

    # -- the per-item outsider group -------------------------------------------
    def test_outsider_never_arrives_third_in_its_item_group(self):
        api = FakeAPI()
        api.add("analyst", 7, "x", state="active", status="in_progress")
        api.add("analyst", 7, "y", state="active", status="queued")
        d = decide(comment_event("stranger", "NONE", number=7), api, limits=claude_gate.Limits(in_flight=5))
        self.assertFalse(d.run)
        self.assertEqual(d.reason, "item-busy")
        self.assertEqual(d.counts["item"], 2)

    def test_maintainer_runs_do_not_hold_the_outsider_group(self):
        api = FakeAPI()
        api.add("analyst", 7, None, state="active", status="in_progress")
        api.add("analyst", 7, None, state="active", status="queued")
        mine = api.next_id
        api.next_id += 1
        api.add("analyst", 7, None, state="active", status="queued", run_id=mine + 1)  # created after this one
        d = decide(comment_event("stranger", "NONE", number=7), api, run_id=mine)
        self.assertTrue(d.run)
        self.assertEqual(d.counts["item"], 0)
        self.assertEqual(api.job_lookups(), [], "a maintainer's run is never looked up")

    def test_one_run_on_the_item_still_lets_an_outsider_queue(self):
        api = FakeAPI()
        api.add("analyst", 7, "x", state="active", status="in_progress")
        self.assertTrue(decide(comment_event("stranger", "NONE", number=7), api).run)

    def test_same_number_in_the_other_workflow_is_another_item(self):
        api = FakeAPI()
        api.add("review", 7, "x", state="active", status="in_progress")
        api.add("review", 7, "y", state="active", status="in_progress")
        d = decide(issue_event(number=7), api, limits=claude_gate.Limits(in_flight=5))
        self.assertTrue(d.run)

    def test_later_outsider_runs_are_ignored(self):
        api = FakeAPI()
        mine = api.next_id
        api.next_id += 1
        api.add("analyst", 1, "stranger", run_id=mine + 1)
        api.add("analyst", 2, "stranger", run_id=mine + 2)
        self.assertTrue(decide(issue_event(), api, run_id=mine).run)

    def test_runs_from_before_the_gate_are_ignored(self):
        api = FakeAPI()
        api.add("review", 1, "x", display="docs: something")
        api.add("review", 2, "x", display="fix: another")
        self.assertTrue(decide(pr_event("x"), api).run)
        self.assertEqual(api.job_lookups(), [])

    # -- plumbing -------------------------------------------------------------------
    def test_main_writes_outputs_and_stays_green(self):
        api = FakeAPI()
        api.add("analyst", 1, "stranger")
        api.add("review", 2, "stranger")
        with tempfile.TemporaryDirectory() as tmp:
            event_path = Path(tmp, "event.json")
            event_path.write_text(json.dumps(issue_event()))
            out, summary = Path(tmp, "out"), Path(tmp, "summary")
            env = {
                "GITHUB_EVENT_PATH": str(event_path),
                "GITHUB_OUTPUT": str(out),
                "GITHUB_STEP_SUMMARY": str(summary),
                "GITHUB_REPOSITORY": REPO,
                "GITHUB_RUN_ID": "100000",
                "KIND": "analyst",
                "WORKFLOWS": "analyst=claude-issue-analyst.yml,review=claude-pr-review.yml",
                "MODEL_JOBS": "analyze,review",
            }
            with mock.patch.dict(os.environ, env), mock.patch.object(claude_gate, "github_fetch", lambda *_: api):
                buf = io.StringIO()
                with redirect_stdout(buf):
                    rc = claude_gate.main([])
            self.assertEqual(rc, 0)
            self.assertEqual(out.read_text(), "run=false\noutsider=true\nreason=author-quota\n")
            self.assertIn("::notice::No automatic Claude run", buf.getvalue())
            self.assertIn("already had 2 automatic Claude runs", summary.read_text())

    def test_title_pattern(self):
        t = claude_gate.TITLE
        self.assertEqual(t.match("Claude analyst #12 (outsider a-b1)").group("login"), "a-b1")
        self.assertEqual(t.match("Claude review #3 (outsider renovate[bot])").group("login"), "renovate[bot]")
        self.assertIsNone(t.match("Claude review #3 (maintainer)").group("login"))
        for bad in ("Claude analyst #12 (outsider )", "Claude analyst #12 (outsider x) y", "Claude other #1 (maintainer)", "x Claude analyst #1 (maintainer)"):
            self.assertIsNone(t.match(bad), bad)

    def test_workflow_titles_match_the_pattern(self):
        """The `run-name:` of both workflows, rendered for each kind of author, parses."""
        root = Path(__file__).resolve().parents[1] / "workflows"
        for wf, kind in FILE.items():
            text = (root / wf).read_text()
            self.assertIn(f"Claude {kind} #${{{{", text)
            line = next(ln for ln in text.splitlines() if ln.strip().startswith("(${{ contains(fromJSON("))
            self.assertIn("&& 'maintainer' || format('outsider {0}'", line)
            self.assertIn("github.event.comment.author_association || github.event.pull_request.author_association || github.event.issue.author_association", line)
            self.assertIn("github.event.comment.user.login || github.event.pull_request.user.login || github.event.issue.user.login", line)


if __name__ == "__main__":
    unittest.main()
