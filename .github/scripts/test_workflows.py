"""Static checks of the Claude workflows' model jobs.

Run: python3 -m unittest discover -s .github/scripts -p 'test_*.py'
"""

from __future__ import annotations

import re
import unittest
from pathlib import Path

WORKFLOWS = Path(__file__).resolve().parents[1] / "workflows"
FILES = ("claude-issue-analyst.yml", "claude-pr-review.yml")
ACTION = "uses: anthropics/claude-code-action@"
INSTALL = "- name: Install bubblewrap for subprocess isolation"


def jobs(text: str) -> dict[str, str]:
    """Top-level jobs of a workflow, by id, as raw text."""
    body = text.split("\njobs:\n", 1)[1]
    parts = re.split(r"^  ([A-Za-z0-9_-]+):\n", body, flags=re.M)
    return {parts[i]: parts[i + 1] for i in range(1, len(parts) - 1, 2)}


class ModelJobTest(unittest.TestCase):
    def test_every_model_job_installs_and_checks_bubblewrap_before_claude(self):
        for name in FILES:
            model_jobs = {jid: j for jid, j in jobs((WORKFLOWS / name).read_text()).items() if ACTION in j}
            self.assertTrue(model_jobs, name)
            for jid, job in model_jobs.items():
                where = f"{name}:{jid}"
                # The scrub stays on; it is why bubblewrap is required at all.
                self.assertIn('CLAUDE_CODE_SUBPROCESS_ENV_SCRUB: "1"', job, where)
                self.assertIn(INSTALL, job, where)
                install = job.index(INSTALL)
                self.assertLess(install, job.index(ACTION), where)
                step = job[install : job.index("\n      - ", install + 1)]
                self.assertIn("apt-get install -y -qq --no-install-recommends bubblewrap", step, where)
                self.assertIn("timeout-minutes:", step, where)
                self.assertIn("kernel.apparmor_restrict_unprivileged_userns=0", step, where)
                self.assertIn("bwrap --ro-bind / / --unshare-all true", step, where)


class PostJobTest(unittest.TestCase):
    """The jobs that hold the write token run no model and no pull-request code."""

    POST_STEPS = {
        "claude-issue-analyst.yml": ("- name: Post the reply and apply its labels, title and state", "gh issue comment"),
        "claude-pr-review.yml": ("- name: Post the review", "gh pr comment"),
    }

    def test_post_jobs_check_out_only_the_scripts_and_filter_links_before_posting(self):
        for name, (step_name, comment) in self.POST_STEPS.items():
            job = jobs((WORKFLOWS / name).read_text())["post"]
            self.assertNotIn(ACTION, job, name)
            self.assertNotIn("secrets.CLAUDE", job, name)
            self.assertNotIn("secrets.ANTHROPIC", job, name)
            # One checkout: the workflow's own commit (no ref:, so never the
            # pull request head), .github/scripts only, no stored token.
            checkouts = [m.start() for m in re.finditer(r"uses: actions/checkout@", job)]
            self.assertEqual(len(checkouts), 1, name)
            with_block = job[checkouts[0] : job.index("\n      - ", checkouts[0] + 1)]
            self.assertIn("persist-credentials: false", with_block, name)
            self.assertIn("sparse-checkout: .github/scripts", with_block, name)
            self.assertNotIn("ref:", with_block, name)
            self.assertNotIn("path:", with_block, name)
            self.assertNotIn("allow-unsafe-pr-checkout", with_block, name)
            # Nothing else from the checkout runs but the link filter.
            self.assertEqual(set(re.findall(r"\.github/scripts/[\w.-]+", job.replace("sparse-checkout: .github/scripts", ""))),
                             {".github/scripts/link_filter.py"}, name)
            step = job[job.index(step_name) :]
            self.assertLess(step.index("python3 -I .github/scripts/link_filter.py"), step.index(comment), name)
            # The rendering gate checks the very file that is posted.
            gate = re.search(r"link_filter\.py --(?:no-filter --)?gate .*?\n?.*?\"\$\{?(\w+)\}?\"\n", step[: step.index(comment)], re.S)
            self.assertIsNotNone(gate, name)
            posted = re.search(r"--body-file \"\$(\w+)\"", step).group(1)
            self.assertEqual(gate.group(1), posted, name)
            if "awk" in step:
                self.assertLess(step.index("awk"), step.index("--gate"), name)


if __name__ == "__main__":
    unittest.main()
