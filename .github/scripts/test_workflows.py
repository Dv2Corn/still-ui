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


if __name__ == "__main__":
    unittest.main()
