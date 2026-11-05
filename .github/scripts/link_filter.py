"""Removes links outside an allowlist from a Claude bot's reply.

The issue analyst and the PR reviewer read text written by strangers. A hostile
issue or pull request could steer the model into putting the attacker's link in
the reply ("run the fix script from <host>"), which would then be posted under
this repository's bot. The `post` jobs run this on the reply before `gh ...
comment`: every link that is not on the allowlist is replaced by
"(link removed)", in prose, code, Markdown and HTML alike.

Allowed, over http(s), with no userinfo and no port:
- github.com and raw.githubusercontent.com, only under this repository
  (Dv2Corn/still-ui), upstream 3x-ui (MHSanaei/3x-ui) and the XTLS
  organisation (XTLS/*);
- docs.github.com and xtls.github.io, any path.
Everything else on GitHub (other owners, gist., user-attachments, other
*.githubusercontent.com) is removed, and so are e-mail addresses and
mailto:/xmpp:/tel:/sms: links. Relative links (`../docs/x.md`, `#anchor`,
`/owner/repo/issues/1`) stay. Anything that does not clearly pass is removed,
so the filter errs towards removing.

Usage: python3 -I link_filter.py IN OUT
Writes the filtered text to OUT. When it removed anything, prints one GitHub
`::warning::` with the count; the links themselves are never printed, since
they are attacker text. Stdlib only: the post job installs nothing.
"""

from __future__ import annotations

import html
import re
import sys
from urllib.parse import unquote

REMOVED = "(link removed)"

# Hosts any path of which may be linked.
OPEN_HOSTS = frozenset({"docs.github.com", "xtls.github.io"})
# Hosts that serve every user's content: only these owners and repositories.
REPO_HOSTS = frozenset({"github.com", "raw.githubusercontent.com"})
ALLOWED_OWNERS = frozenset({"xtls"})
ALLOWED_REPOS = frozenset({("dv2corn", "still-ui"), ("mhsanaei", "3x-ui")})

# Absolute http(s) URL with a bare host: no userinfo, no port, no backslash.
_ALLOWED_SHAPE = re.compile(r"^https?://([a-z0-9.-]+)([/?#].*)?$", re.I | re.S)
_SCHEME = re.compile(r"^[a-z][a-z0-9+.-]*:", re.I)
# Two slashes or backslashes in any mix: browsers read `/\host` as `//host`.
_PROTOCOL_RELATIVE = re.compile(r"^[/\\]{2}")
_BACKSLASH_ESCAPE = re.compile(r"\\([!-/:-@\[-`{-~])")
_ENCODED_SEPARATOR = re.compile(r"%(?:2f|5c|2e)", re.I)
_ENTITY = re.compile(r"&(?:#[0-9]{1,7}|#[xX][0-9a-fA-F]{1,6}|[A-Za-z][A-Za-z0-9]{1,31});")
# Characters an entity may stand for and be decoded to in the text itself: a
# URL hidden as `ht&#116;ps&#58;//host` still becomes a link on GitHub. No
# other markup character is decoded; a decoded backslash can only escape
# markup, never create a link the later passes would not see.
_SAFE_DECODED = re.compile(r"^[A-Za-z0-9:/\\]$")


def host_allowed(host: str) -> bool:
    """Whether some path on this host may be linked."""
    host = host.lower()
    return host in OPEN_HOSTS or host in REPO_HOSTS


def path_allowed(host: str, rest: str) -> bool:
    """Whether `rest` (path, query, fragment) on `host` may be linked."""
    host = host.lower()
    if host in OPEN_HOSTS:
        return True
    if host not in REPO_HOSTS:
        return False
    path = re.split(r"[?#]", rest, maxsplit=1)[0]
    # No encoded separators, no backslash, no dot or empty segments: the owner
    # and repository read here are the ones the browser goes to.
    if "\\" in path or _ENCODED_SEPARATOR.search(path):
        return False
    segments = unquote(path).split("/")
    if segments[0] != "" or any(s in (".", "..") for s in segments[1:]) or "" in segments[1:-1]:
        return False
    owner = segments[1].lower() if len(segments) > 1 else ""
    repo = segments[2].lower() if len(segments) > 2 else ""
    repo = repo.removesuffix(".git")
    return owner in ALLOWED_OWNERS or (owner, repo) in ALLOWED_REPOS


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
    return bool(m) and path_allowed(m.group(1), m.group(2) or "")


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


# Attributes whose value a browser follows or loads.
_URL_ATTRS = frozenset(
    {"href", "src", "srcset", "action", "formaction", "poster", "cite", "background", "data",
     "xlink:href", "longdesc", "ping", "lowsrc", "dynsrc", "codebase", "manifest", "icon",
     "itemtype", "archive", "classid", "usemap", "imagesrcset", "profile"}
)
_SPACE = " \t\n\r\f"


def scan_start_tag(text: str, i: int) -> tuple[int, list[tuple[str, str]]] | None:
    """Reads the start tag at text[i] (`<` then a letter) the way an HTML5
    tokenizer does: quoted values may hold `>`, names may hold quotes. Returns
    the index after its `>` and its (name, value) attributes, or None when the
    tag never closes."""
    n = len(text)
    j = i + 1
    while j < n and text[j] not in _SPACE + "/>":
        j += 1
    attrs: list[tuple[str, str]] = []
    while True:
        while j < n and text[j] in _SPACE + "/":
            j += 1
        if j >= n:
            return None
        if text[j] == ">":
            return j + 1, attrs
        start = j
        j += 1  # an attribute name may start with `=`
        while j < n and text[j] not in _SPACE + "/>=":
            j += 1
        name = text[start:j].lower()
        k = j
        while k < n and text[k] in _SPACE:
            k += 1
        value = ""
        if k < n and text[k] == "=":
            k += 1
            while k < n and text[k] in _SPACE:
                k += 1
            if k < n and text[k] in "\"'":
                end = text.find(text[k], k + 1)
                if end < 0:
                    return None
                value, j = text[k + 1 : end], end + 1
            else:
                start = k
                while k < n and text[k] not in _SPACE + ">":
                    k += 1
                value, j = text[start:k], k
        attrs.append((name, value))


class _Filter:
    def __init__(self) -> None:
        self.removed = 0

    def drop(self, keep: str = "") -> str:
        self.removed += 1
        return f"{keep} {REMOVED}" if keep.strip() else REMOVED

    # <https://host/...> and <user@host>: CommonMark autolinks. No e-mail
    # domain is allowed.
    _AUTOLINK = re.compile(
        r"<([A-Za-z][A-Za-z0-9+.-]{1,31}:[^\s<>]*)>"
        r"|<[A-Za-z0-9.!#$%&'*+/=?^_`{|}~-]+@[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?"
        r"(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*>"
    )

    def autolinks(self, text: str) -> str:
        return self._AUTOLINK.sub(
            lambda m: m.group(0) if m.group(1) and target_allowed(m.group(1)) else self.drop(), text
        )

    def tags(self, text: str) -> str:
        """A foreign target in a URL attribute removes the whole start tag (a
        closing </a> left behind renders as nothing). A tag that never closes
        loses its `<`, so no tag can start there (fail closed). A URL in any
        other attribute (title=...) is not a link; the last pass still
        replaces it if it has a scheme."""
        out = []
        pos = 0
        for m in re.finditer(r"<[A-Za-z]", text):
            i = m.start()
            if i < pos:
                continue
            scanned = scan_start_tag(text, i)
            if scanned is None:
                rest = text[i:]
                if "=" in rest.split("\n\n", 1)[0]:
                    out.append(text[pos:i] + self.drop() + " ")
                    pos = i + 1
                continue
            end, attrs = scanned
            bad = any(
                name in _URL_ATTRS
                and any(not target_allowed(t) for t in (re.split(r"[\s,]+", html.unescape(value)) or [""]))
                for name, value in attrs
            )
            if bad:
                out.append(text[pos:i] + self.drop())
                pos = end
        out.append(text[pos:])
        return "".join(out)

    # [text](target "title") and ![alt](target): text stays, the link goes.
    _INLINE = re.compile(
        r"(!?)\[((?:[^\[\]\\]|\\.|\[(?:[^\[\]\\]|\\.)*\])*)\]\(\s*(<[^<>\n]*>|(?:[^\s()\\]|\\.|\([^\s()]*\))*)"
        r"(?:\s+(?:\"[^\"]*\"|'[^']*'|\([^()]*\)))?\s*\)",
        re.S,
    )
    # Any `](target` the pattern above could not read whole (deeper brackets,
    # odd titles): its target alone is removed.
    _LEFTOVER = re.compile(r"\]\(\s*(<[^<>\n]*>|[^\s)]*)")

    def inline_links(self, text: str) -> str:
        def repl(m: re.Match[str]) -> str:
            target = m.group(3)
            if target.startswith("<") and target.endswith(">"):
                target = target[1:-1]
            return m.group(0) if target_allowed(target) else self.drop(m.group(2))

        text = self._INLINE.sub(repl, text)
        return self._LEFTOVER.sub(
            lambda m: m.group(0) if target_allowed(m.group(1).strip("<>")) else f"] {self.drop()} (", text
        )

    # `]: target` anywhere - a reference definition, also inside a block quote
    # or a list item and with the target on the next line - keeps its label;
    # a foreign target goes.
    _REFDEF = re.compile(r"\]:[ \t]*(?:\n[ \t>]*)?(<[^<>\n]*>|\S+)")

    def reference_definitions(self, text: str) -> str:
        def repl(m: re.Match[str]) -> str:
            target = m.group(1)
            if target.startswith("<") and target.endswith(">"):
                target = target[1:-1]
            if target_allowed(target):
                return m.group(0)
            return "]: " + self.drop()

        return self._REFDEF.sub(repl, text)

    # Whatever is left, in prose or code: a URL with any scheme; the bare
    # `www.` names GitHub turns into links; `//host.tld/...` or `\\host.tld`
    # (a dotted host, so `//go:embed` and `// comment` stay); mailto:, xmpp:,
    # tel: and sms: links; and e-mail addresses, which GitHub links too (the
    # domain must end in letters, so `pkg@v1.2.3` stays, and `icon@2x.png`
    # is a file name, not an address). A URL ends at a
    # bracket, so `[https://a](https://b)` is read as two.
    _URL = re.compile(
        r"(?<![A-Za-z0-9+.-])[A-Za-z][A-Za-z0-9+.-]*:(?:\\?[/\\]){2}[^\s<>\"'`\[\]]*"
        r"|(?<![A-Za-z0-9.@/:_\\-])www\.[^\s<>\"'`\[\]]+"
        r"|(?<![A-Za-z0-9.@/:_\\-])[/\\]{2}[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+(?=[/\\:?#\s)\]]|$)[^\s<>\"'`\[\]]*"
        r"|(?<![A-Za-z0-9+.-])(?:mailto|xmpp|tel|sms):[^\s<>\"'`\[\]]+"
        r"|(?<![A-Za-z0-9._+/-])[A-Za-z0-9._+-]+@(?!\d+(?:\.\d+)?x\.)[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}(?![A-Za-z0-9-])",
        re.I,
    )
    _EMAIL_OR_NO_SLASH = re.compile(r"^(?:mailto|xmpp|tel|sms):|^[^:/\\]*@", re.I)

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
            if self._EMAIL_OR_NO_SLASH.match(url):
                return self.drop() + tail
            if url.lower().startswith("www."):
                check = "http://" + url
            elif url[:1] in "/\\":
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
            f"::warning::Removed {removed} link(s) outside the allowlist (this repository, "
            "MHSanaei/3x-ui and XTLS on GitHub; docs.github.com; xtls.github.io) from the reply before posting it."
        )
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
