"""Runs the issue analyst's "Post the reply" step against a fake `gh`.

The step's shell is taken from claude-issue-analyst.yml as it is, so the test
covers the workflow's own text. Needs bash and jq (both on GitHub's runners).

Run: python3 -m unittest discover -s .github/scripts -p 'test_*.py'
"""

from __future__ import annotations

import json
import os
import shutil
import subprocess
import tempfile
import textwrap
import unittest
from pathlib import Path

WORKFLOW = Path(__file__).resolve().parents[1] / "workflows" / "claude-issue-analyst.yml"
STEP = "- name: Post the reply and apply its labels, title and state"
RUN_ID = 5000

FAKE_GH = r"""#!/usr/bin/env bash
# Answers the gh calls the step makes from files in $FAKE; logs the writes.
set -euo pipefail
echo "$*" >> "$FAKE/calls.log"
case "$1 $2" in
  "issue comment") cp "${@: -1}" "$FAKE/posted.md" ;;
  "issue view") cat "$FAKE/issue.json" ;;
  "label list") jq -r '.[]' "$FAKE/repo-labels.json" ;;
  "issue edit"|"issue close") echo "$*" >> "$FAKE/writes.log" ;;
  "api repos/"*)
    expr=""
    while [ $# -gt 0 ]; do if [ "$1" = --jq ]; then expr=$2; fi; shift; done
    jq -r "$expr" "$FAKE/comments.json" ;;
  *) echo "unexpected gh call: $*" >&2; exit 3 ;;
esac
"""


def step_script() -> str:
    lines = WORKFLOW.read_text().splitlines()
    start = next(i for i, ln in enumerate(lines) if ln.strip() == STEP)
    run = next(i for i in range(start, len(lines)) if lines[i].strip() == "run: |")
    indent = len(lines[run]) - len(lines[run].lstrip()) + 2
    body = []
    for ln in lines[run + 1 :]:
        if ln.strip() and len(ln) - len(ln.lstrip()) < indent:
            break
        body.append(ln)
    return textwrap.dedent("\n".join(body)) + "\n"


def bot(body: str) -> dict:
    return {"user": {"login": "github-actions[bot]"}, "body": body}


@unittest.skipUnless(shutil.which("jq") and shutil.which("bash"), "needs jq and bash")
class AnalystPostTest(unittest.TestCase):
    def run_step(self, reply, actions, issue, comments, title_at_start="Old title"):
        tmp = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, tmp)
        (tmp / "bin").mkdir()
        gh = tmp / "bin" / "gh"
        gh.write_text(FAKE_GH)
        gh.chmod(0o755)
        (tmp / "analyst").mkdir()
        (tmp / "analyst" / "comment.md").write_text(reply)
        if actions is not None:
            (tmp / "analyst" / "actions.json").write_text(json.dumps(actions))
        (tmp / "issue.json").write_text(json.dumps(issue))
        (tmp / "repo-labels.json").write_text(json.dumps(["bug", "triage", "clarification needed", "duplicate"]))
        (tmp / "comments.json").write_text(json.dumps(comments))
        (tmp / "step.sh").write_text(step_script())
        env = dict(
            os.environ,
            PATH=f"{tmp / 'bin'}:{os.environ['PATH']}",
            FAKE=str(tmp),
            DIR=str(tmp / "analyst"),
            RUNNER_TEMP=str(tmp),
            GITHUB_RUN_ID=str(RUN_ID),
            ISSUE="7",
            REPO="owner/repo",
            TRIGGER="issues",
            TITLE_AT_START=title_at_start,
        )
        proc = subprocess.run(["bash", str(tmp / "step.sh")], env=env, capture_output=True, text=True)
        self.assertEqual(proc.returncode, 0, proc.stderr + proc.stdout)
        writes = (tmp / "writes.log").read_text().splitlines() if (tmp / "writes.log").exists() else []
        return (tmp / "posted.md").read_text(), writes, proc.stdout

    REPLY = "Analysis.\n<!-- claude-issue:run 99999999 -->\n<!-- claude-issue:analyst -->\n"
    ACTIONS = {"add_labels": ["bug", "clarification needed"], "remove_labels": ["triage", "duplicate"], "title": "New title", "close": "not planned"}
    OPEN = {"labels": [{"name": "bug"}, {"name": "triage"}], "title": "Old title", "state": "OPEN"}

    def test_reply_carries_only_the_workflows_run_marker(self):
        posted, _, _ = self.run_step(self.REPLY, None, self.OPEN, [])
        self.assertEqual(posted, f"Analysis.\n<!-- claude-issue:run {RUN_ID} -->\n<!-- claude-issue:analyst -->\n")

    def test_applies_changes_against_the_issue_as_it_is_now(self):
        older = [bot(f"x\n<!-- claude-issue:run {RUN_ID - 3} -->\n<!-- claude-issue:analyst -->"), bot("<!-- claude-issue:run 99999999 --> inline, not a marker line")]
        _, writes, _ = self.run_step(self.REPLY, self.ACTIONS, self.OPEN, older)
        self.assertEqual(
            writes,
            [
                "issue edit 7 -R owner/repo --add-label clarification needed",
                "issue edit 7 -R owner/repo --remove-label triage",
                "issue edit 7 -R owner/repo --title New title",
                "issue close 7 -R owner/repo --reason not planned",
            ],
        )

    def test_a_newer_runs_reply_wins(self):
        newer = [bot(f"y\n<!-- claude-issue:run {RUN_ID + 1} -->\n<!-- claude-issue:analyst -->")]
        posted, writes, out = self.run_step(self.REPLY, self.ACTIONS, self.OPEN, newer)
        self.assertIn("Analysis.", posted)
        self.assertEqual(writes, [])
        self.assertIn(f"Run {RUN_ID + 1} started after this one", out)

    def test_a_forged_marker_from_someone_else_is_ignored(self):
        forged = [{"user": {"login": "stranger"}, "body": f"<!-- claude-issue:run {RUN_ID + 50} -->"}]
        _, writes, _ = self.run_step(self.REPLY, self.ACTIONS, self.OPEN, forged)
        self.assertEqual(len(writes), 4)

    def test_title_and_state_changed_meanwhile_are_left_alone(self):
        moved = {"labels": [], "title": "Renamed by a maintainer", "state": "CLOSED"}
        _, writes, _ = self.run_step(self.REPLY, {"title": "New title", "close": "completed"}, moved, [])
        self.assertEqual(writes, [])


if __name__ == "__main__":
    unittest.main()
