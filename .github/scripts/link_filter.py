"""Removes links to hosts outside an allowlist from a Claude bot's reply.

The issue analyst and the PR reviewer read text written by strangers. A hostile
issue or pull request could steer the model into putting the attacker's link in
the reply ("run the fix script from <host>"), which would then be posted under
this repository's bot. The `post` jobs run this on the reply before `gh ...
comment`: every link whose host is not GitHub (or the Xray docs) is replaced by
"(link removed)", in prose, code, Markdown and HTML alike.

Allowed: http(s) to github.com and its subdomains, raw.githubusercontent.com and
xtls.github.io, with no userinfo and no port. Relative links (`../docs/x.md`,
`#anchor`, `/owner/repo/issues/1`) stay. Anything that does not clearly pass is
removed, so the filter errs towards removing.

Usage: python3 -I link_filter.py IN OUT
Writes the filtered text to OUT. When it removed anything, prints one GitHub
`::warning::` with the count; the links themselves are never printed, since
they are attacker text. Stdlib only: the post job installs nothing.
"""

from __future__ import annotations

import html
import re
import sys

REMOVED = "(link removed)"

ALLOWED_HOSTS = frozenset({"github.com", "raw.githubusercontent.com", "xtls.github.io"})
# Any subdomain of github.com (docs., gist., ...), each label well formed.
_GITHUB_SUBDOMAIN = re.compile(r"^(?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.)+github\.com$")

# Absolute http(s) URL with a bare host: no userinfo, no port, no backslash.
_ALLOWED_SHAPE = re.compile(r"^https?://([a-z0-9.-]+)(?:[/?#]|$)", re.I)
_SCHEME = re.compile(r"^[a-z][a-z0-9+.-]*:", re.I)
# Two slashes or backslashes in any mix: browsers read `/\host` as `//host`.
_PROTOCOL_RELATIVE = re.compile(r"^[/\\]{2}")
_BACKSLASH_ESCAPE = re.compile(r"\\([!-/:-@\[-`{-~])")
_ENTITY = re.compile(r"&(?:#[0-9]{1,7}|#[xX][0-9a-fA-F]{1,6}|[A-Za-z][A-Za-z0-9]{1,31});")
# Characters an entity may stand for and be decoded to in the text itself: a
# URL hidden as `ht&#116;ps&#58;//host` still becomes a link on GitHub. No
# markup character is decoded, so the rendering does not change.
_SAFE_DECODED = re.compile(r"^[A-Za-z0-9:/]$")


def host_allowed(host: str) -> bool:
    host = host.lower()
    return host in ALLOWED_HOSTS or bool(_GITHUB_SUBDOMAIN.match(host))


def _variants(target: str) -> list[str]:
    """The ways GitHub and a browser may read a link target."""
    raw = target.strip(" \t\r\n\x00\x0b\x0c")
    decoded = html.unescape(raw)
    unescaped = _BACKSLASH_ESCAPE.sub(r"\1", decoded)
    out = []
    for v in (raw, decoded, unescaped):
        # The URL parser drops tabs and newlines anywhere and C0 controls and
        # spaces at either end.
        v = re.sub(r"[\t\r\n]", "", v)
        v = v.strip("".join(chr(c) for c in range(0x21)))
        out.append(v)
    return out


def _absolute_allowed(url: str) -> bool:
    m = _ALLOWED_SHAPE.match(url)
    return bool(m) and host_allowed(m.group(1))


def target_allowed(target: str, *, relative_ok: bool = True) -> bool:
    """Whether a link target may stay. Every reading of it must pass."""
    for v in _variants(target):
        if _PROTOCOL_RELATIVE.match(v):
            if not _absolute_allowed("https:" + v):
                return False
        elif _SCHEME.match(v):
            if not _absolute_allowed(v):
                return False
        elif not relative_ok:
            return False
    return True


def _decode_safe_entities(text: str) -> str:
    def repl(m: re.Match[str]) -> str:
        ch = html.unescape(m.group(0))
        return ch if _SAFE_DECODED.match(ch) else m.group(0)

    return _ENTITY.sub(repl, text)


class _Filter:
    def __init__(self) -> None:
        self.removed = 0

    def drop(self, keep: str = "") -> str:
        self.removed += 1
        return f"{keep} {REMOVED}" if keep.strip() else REMOVED

    # <https://host/...>: a CommonMark autolink.
    _AUTOLINK = re.compile(r"<([A-Za-z][A-Za-z0-9+.-]{1,31}:[^\s<>]*)>")

    def autolinks(self, text: str) -> str:
        return self._AUTOLINK.sub(lambda m: m.group(0) if target_allowed(m.group(1)) else self.drop(), text)

    # Any HTML tag; a foreign target in an attribute that holds a URL removes
    # the tag whole (<a href>, <img src>, srcset, ...). A closing </a> left
    # behind renders as nothing. A URL in any other attribute (title=...) is
    # not a link; the last pass still replaces it if it has a scheme.
    _TAG = re.compile(r"<[A-Za-z][A-Za-z0-9-]*(?:\s[^<>]*)?>")
    _ATTR = re.compile(r"""([^\s"'<>/=]+)\s*=\s*("[^"]*"|'[^']*'|[^\s"'=<>`]+)""")
    _URL_ATTRS = frozenset(
        {"href", "src", "srcset", "action", "formaction", "poster", "cite", "background", "data",
         "xlink:href", "longdesc", "ping", "lowsrc", "dynsrc", "codebase", "manifest", "icon", "itemtype"}
    )

    def tags(self, text: str) -> str:
        def repl(m: re.Match[str]) -> str:
            for a in self._ATTR.finditer(m.group(0)):
                if a.group(1).lower() not in self._URL_ATTRS:
                    continue
                value = html.unescape(a.group(2).strip("\"'"))
                for token in re.split(r"[\s,]+", value) or [""]:
                    if not target_allowed(token):
                        return self.drop()
            return m.group(0)

        return self._TAG.sub(repl, text)

    # [text](target "title") and ![alt](target): text stays, the link goes.
    _INLINE = re.compile(
        r"(!?)\[((?:[^\[\]\\]|\\.|\[(?:[^\[\]\\]|\\.)*\])*)\]\(\s*(<[^<>\n]*>|(?:[^\s()\\]|\\.|\([^\s()]*\))*)"
        r"(?:\s+(?:\"[^\"]*\"|'[^']*'|\([^()]*\)))?\s*\)",
        re.S,
    )

    def inline_links(self, text: str) -> str:
        def repl(m: re.Match[str]) -> str:
            target = m.group(3)
            if target.startswith("<") and target.endswith(">"):
                target = target[1:-1]
            return m.group(0) if target_allowed(target) else self.drop(m.group(2))

        text = self._INLINE.sub(repl, text)
        # A link the pattern above could not read whole (deeper brackets in
        # its text): its target alone is removed.
        return self._LEFTOVER.sub(
            lambda m: m.group(0) if target_allowed(m.group(1).strip("<>")) else f"] {self.drop()} (", text
        )

    _LEFTOVER = re.compile(r"\]\(\s*(<[^<>\n]*>|[^\s)]*)")

    # [label]: target "title" - a reference definition.
    _REFDEF = re.compile(
        r"^( {0,3}\[(?:[^\[\]\\]|\\.)+\]:)[ \t]*\n?[ \t]*(<[^<>\n]*>|\S+)([^\n]*)$",
        re.M,
    )

    def reference_definitions(self, text: str) -> str:
        def repl(m: re.Match[str]) -> str:
            target = m.group(2)
            if target.startswith("<") and target.endswith(">"):
                target = target[1:-1]
            if target_allowed(target):
                return m.group(0)
            self.removed += 1
            return f"{m.group(1)} {REMOVED}"

        return self._REFDEF.sub(repl, text)

    # Whatever is left: a URL with any scheme anywhere, prose or code, the
    # bare `www.` names GitHub turns into links, and `//host.tld/...` (a
    # dotted host, so `//go:embed` and `// comment` stay). A URL ends at a
    # bracket, so `[https://a](https://b)` is read as two.
    _URL = re.compile(
        r"(?<![A-Za-z0-9+.-])[A-Za-z][A-Za-z0-9+.-]*:(?:\\?[/\\]){2}[^\s<>\"'`\[\]]*"
        r"|(?<![A-Za-z0-9.@/:_\\-])www\.[^\s<>\"'`\[\]]+"
        r"|(?<![A-Za-z0-9.@/:_\\-])//[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+(?=[/:?#\s)\]]|$)[^\s<>\"'`\[\]]*",
    )

    @staticmethod
    def _trim(url: str) -> tuple[str, str]:
        """Splits off trailing punctuation that is prose, as GitHub does."""
        end = len(url)
        while end:
            ch = url[end - 1]
            if ch in ".,:;!?*_~'\"":
                end -= 1
            elif ch == ")" and url[:end].count(")") > url[:end].count("("):
                end -= 1
            else:
                break
        return url[:end], url[end:]

    def bare_urls(self, text: str) -> str:
        def repl(m: re.Match[str]) -> str:
            url, tail = self._trim(m.group(0))
            if url.lower().startswith("www."):
                check = "http://" + url
            elif url.startswith("//"):
                check = "https:" + url
            else:
                check = url
            if url and target_allowed(check, relative_ok=False):
                return m.group(0)
            return self.drop() + tail

        return self._URL.sub(repl, text)

    def run(self, text: str) -> str:
        text = _decode_safe_entities(text)
        text = self.autolinks(text)
        text = self.tags(text)
        text = self.inline_links(text)
        text = self.reference_definitions(text)
        return self.bare_urls(text)


def filter_links(text: str) -> tuple[str, int]:
    """Returns the text with foreign links removed, and how many were removed."""
    f = _Filter()
    out = f.run(text)
    return out, f.removed


def main(argv: list[str]) -> int:
    if len(argv) != 3:
        print("usage: link_filter.py IN OUT", file=sys.stderr)
        return 2
    with open(argv[1], encoding="utf-8", errors="replace", newline="") as fh:
        text = fh.read()
    out, removed = filter_links(text)
    with open(argv[2], "w", encoding="utf-8", newline="") as fh:
        fh.write(out)
    if removed:
        print(
            f"::warning::Removed {removed} link(s) to hosts outside the allowlist "
            "(github.com, raw.githubusercontent.com, xtls.github.io) from the reply before posting it."
        )
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
