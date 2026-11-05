"""Removes links outside an allowlist from a Claude bot's reply.

The issue analyst and the PR reviewer read text written by strangers. A hostile
issue or pull request could steer the model into putting the attacker's link in
the reply ("run the fix script from <host>"), which would then be posted under
this repository's bot. The `post` jobs run this on the reply before `gh ...
comment`: every link that is not on the allowlist is replaced by
"(link removed)", in prose, code, Markdown and HTML alike.

Allowed, over http(s), with no userinfo and no port:
- github.com and raw.githubusercontent.com, only under this repository
  (--repo), upstream 3x-ui (MHSanaei/3x-ui) and the XTLS organisation;
- docs.github.com and xtls.github.io, any path.
Under an allowed repository, GitHub also serves commits of every fork, so a
path that names a commit (blob, tree, raw, blame, commit, commits, compare,
archive, raw.githubusercontent.com) passes only for the default branch, a tag,
a full SHA that the default branch contains (asked of the GitHub API, `gh
api`, failing closed) or the head under review (--head-sha). Issue, pull
request, discussion and release pages pass; release downloads only for this
repository; issue attachments (/files/, /assets/) never.

Relative links are resolved against the page the reply is posted on (--base,
e.g. https://github.com/OWNER/REPO/issues/7) and pass the same rules; `#anchor`
links stay. E-mail addresses and mailto:/xmpp:/tel:/sms: links go. Anything
that does not clearly pass is removed, so the filter errs towards removing.

Two layers:
- the text pass (filter_links) reads the Markdown with patterns and removes
  what fails. It is cosmetics: it keeps the reply readable, but a pattern
  reader can always disagree with GitHub's renderer somewhere;
- the gate (--gate) is the security boundary. It has GitHub render the
  finished text (`gh api markdown`, mode gfm, in this repository's context),
  reads every URL attribute of the returned HTML (href, src, srcset,
  data-canonical-src, ...; camo image URLs only through a data-canonical-src
  that passes) and checks each against the same policy. If any fails, or the
  render fails, OUT is the text as plain text inside a fence that renders no
  link, under one line saying why.

Usage: python3 -I link_filter.py [--repo OWNER/REPO] [--base URL]
                                  [--head-sha SHA] [--gate] [--no-filter] IN OUT
Writes the result to OUT. Prints one GitHub `::warning::` per layer that
acted, with counts only: the links themselves are attacker text and never
printed. Stdlib only: the post job installs nothing.
"""

from __future__ import annotations

import argparse
import html
import re
import subprocess
import sys
from dataclasses import dataclass, field
from html.parser import HTMLParser
from urllib.parse import quote, unquote, urljoin

REMOVED = "(link removed)"
DEFAULT_REPO = "Dv2Corn/still-ui"

# Hosts any path of which may be linked.
OPEN_HOSTS = frozenset({"docs.github.com", "xtls.github.io"})
# Hosts that serve every user's content: only these owners and repositories.
REPO_HOSTS = frozenset({"github.com", "raw.githubusercontent.com"})
ALLOWED_OWNERS = frozenset({"xtls"})
UPSTREAM_REPOS = frozenset({("mhsanaei", "3x-ui")})

# Repository pages that show no code of a chosen commit.
PAGE_SECTIONS = frozenset(
    {"issues", "pull", "pulls", "discussions", "tags", "labels", "milestones", "milestone", "actions", "security"}
)
# Sections whose next segment is a ref: GitHub resolves a SHA there across
# the whole fork network.
REF_SECTIONS = frozenset({"blob", "tree", "raw", "blame", "commit", "commits"})

_NAME = re.compile(r"^[A-Za-z0-9_.-]+$")
_FULL_SHA = re.compile(r"^[0-9a-f]{40}$", re.I)
_SHORT_HEX = re.compile(r"^[0-9a-f]{4,39}$", re.I)
_REF_NAME = re.compile(r"^[A-Za-z0-9._-]{1,200}$")

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

# Stand-ins for spans already checked and kept (a link, a tag, an autolink),
# so the last pass reads the text around them the way GitHub does.
_P_OPEN, _P_CLOSE = "\ue000", "\ue001"
_PLACEHOLDER = re.compile(f"{_P_OPEN}([0-9]+){_P_CLOSE}")


class RefChecker:
    """Answers whether a ref of a repository may be linked. This base knows
    nothing, so every ref is refused (fail closed)."""

    def default_branch(self, owner: str, repo: str) -> str | None:
        return None

    def is_tag(self, owner: str, repo: str, name: str) -> bool:
        return False

    def sha_on_default(self, owner: str, repo: str, sha: str) -> bool:
        return False


class StaticChecker(RefChecker):
    """Fixed answers, for tests. Keys are `owner/repo`, any case."""

    def __init__(self, default_branches=None, tags=None, shas=None):
        self.branches = {k.lower(): v for k, v in (default_branches or {}).items()}
        self.tags = {k.lower(): set(v) for k, v in (tags or {}).items()}
        self.shas = {k.lower(): {s.lower() for s in v} for k, v in (shas or {}).items()}

    def default_branch(self, owner, repo):
        return self.branches.get(f"{owner}/{repo}".lower())

    def is_tag(self, owner, repo, name):
        return name in self.tags.get(f"{owner}/{repo}".lower(), ())

    def sha_on_default(self, owner, repo, sha):
        return sha.lower() in self.shas.get(f"{owner}/{repo}".lower(), ())


class GhChecker(RefChecker):
    """Asks the GitHub API through `gh api` (GH_TOKEN from the job). Every
    error, timeout or unexpected answer counts as no; answers are cached and
    the number of calls is capped."""

    MAX_CALLS = 40

    def __init__(self) -> None:
        self.cache: dict[tuple[str, str], str | None] = {}
        self.calls = 0

    def _api(self, path: str, jq: str) -> str | None:
        key = (path, jq)
        if key not in self.cache:
            out = None
            if self.calls < self.MAX_CALLS:
                self.calls += 1
                try:
                    proc = subprocess.run(["gh", "api", path, "--jq", jq], capture_output=True, text=True, timeout=30)
                    if proc.returncode == 0:
                        out = proc.stdout.strip()
                except (OSError, subprocess.SubprocessError):
                    out = None
            self.cache[key] = out
        return self.cache[key]

    def default_branch(self, owner, repo):
        name = self._api(f"repos/{owner}/{repo}", ".default_branch")
        return name if name and _REF_NAME.match(name) else None

    def is_tag(self, owner, repo, name):
        if not _REF_NAME.match(name):
            return False
        return self._api(f"repos/{owner}/{repo}/git/ref/tags/{quote(name, safe='')}", ".ref") == f"refs/tags/{name}"

    def sha_on_default(self, owner, repo, sha):
        branch = self.default_branch(owner, repo)
        if not branch or not _FULL_SHA.match(sha):
            return False
        # `behind`/`identical`: the commit is an ancestor of the default branch.
        status = self._api(f"repos/{owner}/{repo}/compare/{quote(branch, safe='')}...{sha.lower()}", ".status")
        return status in ("behind", "identical")


@dataclass
class Policy:
    repo: str = DEFAULT_REPO
    # The page the reply is posted on; relative links resolve against it.
    base: str = ""
    # The commit under review: the PR reviewer links its code.
    head_sha: str = ""
    checker: RefChecker = field(default_factory=RefChecker)

    def __post_init__(self) -> None:
        owner, _, name = self.repo.partition("/")
        if not (_NAME.match(owner) and _NAME.match(name)):
            raise ValueError("--repo must be OWNER/REPO")
        self.own = (owner.lower(), name.lower())
        if not self.base:
            self.base = f"https://github.com/{self.repo}/issues/1"

    def repo_allowed(self, owner: str, repo: str) -> bool:
        owner, repo = owner.lower(), repo.lower()
        return bool(repo) and (owner in ALLOWED_OWNERS or (owner, repo) in UPSTREAM_REPOS or (owner, repo) == self.own)

    def ref_ok(self, owner: str, repo: str, ref: str) -> bool:
        if _FULL_SHA.match(ref):
            if self.head_sha and ref.lower() == self.head_sha.lower() and (owner.lower(), repo.lower()) == self.own:
                return True
            return self.checker.sha_on_default(owner, repo, ref)
        # A short SHA may name any commit of the fork network.
        if _SHORT_HEX.match(ref) or not _REF_NAME.match(ref):
            return False
        if ref == self.checker.default_branch(owner, repo):
            return True
        return self.checker.is_tag(owner, repo, ref)


_DEFAULT_POLICY = Policy()


def _ref_path_allowed(owner: str, repo: str, parts: list[str], policy: Policy, *, need_file: bool = False) -> bool:
    """`parts` starts at a ref: `<ref>/...`, `refs/heads/<b>/...` or `refs/tags/<t>/...`."""
    if not parts:
        return False
    if parts[0] == "refs":
        if len(parts) < 3:
            return False
        if parts[1] == "tags":
            # Resolved only among this repository's own tags.
            ok = bool(_REF_NAME.match(parts[2]))
        elif parts[1] == "heads":
            ok = parts[2] == policy.checker.default_branch(owner, repo)
        else:  # refs/pull/* and anything else
            return False
        rest = parts[3:]
    else:
        ok = policy.ref_ok(owner, repo, parts[0])
        rest = parts[1:]
    return ok and (bool(rest) or not need_file)


def _section_allowed(owner: str, repo: str, rest: list[str], policy: Policy) -> bool:
    if not rest:
        return True
    section, sub = rest[0].lower(), rest[1:]
    if section in PAGE_SECTIONS:
        return True
    if section == "commits" and not sub:
        return True
    if section in REF_SECTIONS:
        return _ref_path_allowed(owner, repo, sub, policy)
    if section == "archive" and sub:
        last = sub[-1]
        for ext in (".tar.gz", ".zip"):
            if last.endswith(ext) and len(last) > len(ext):
                return _ref_path_allowed(owner, repo, sub[:-1] + [last[: -len(ext)]], policy)
        return False
    if section == "compare":
        if not sub:
            return True
        sides = re.split(r"\.{2,3}", "/".join(sub), maxsplit=1)
        return all(side and policy.ref_ok(owner, repo, side) for side in sides)
    if section == "releases":
        if not sub or sub == ["latest"] or (sub[0] == "tag" and len(sub) >= 2):
            return True
        download = sub[:1] == ["download"] or sub[:2] == ["latest", "download"]
        return download and (owner.lower(), repo.lower()) == policy.own
    return False


def host_allowed(host: str) -> bool:
    """Whether some path on this host may be linked."""
    host = host.lower()
    return host in OPEN_HOSTS or host in REPO_HOSTS


def path_allowed(host: str, rest: str, policy: Policy | None = None) -> bool:
    """Whether `rest` (path, query, fragment) on `host` may be linked."""
    policy = policy or _DEFAULT_POLICY
    host = host.lower()
    if host in OPEN_HOSTS:
        return True
    if host not in REPO_HOSTS:
        return False
    path = re.split(r"[?#]", rest, maxsplit=1)[0]
    # No encoded separators, no backslash, no dot or empty segments: the owner,
    # repository and ref read here are the ones the browser goes to.
    if "\\" in path or _ENCODED_SEPARATOR.search(path):
        return False
    segments = unquote(path).split("/")
    if segments[0] != "" or any(s in (".", "..") for s in segments[1:]) or "" in segments[1:-1]:
        return False
    segs = segments[1:]
    if segs and segs[-1] == "":
        segs = segs[:-1]
    if not segs or not all(_NAME.match(s) for s in segs[:2]):
        return False
    if len(segs) == 1:
        return host == "github.com" and segs[0].lower() in ALLOWED_OWNERS
    owner, repo = segs[0], segs[1]
    if repo.lower().endswith(".git") and len(segs) == 2:
        repo = repo[:-4]
    if not policy.repo_allowed(owner, repo):
        return False
    if host == "raw.githubusercontent.com":
        return _ref_path_allowed(owner, repo, segs[2:], policy, need_file=True)
    return _section_allowed(owner, repo, segs[2:], policy)


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


def _absolute_allowed(url: str, policy: Policy) -> bool:
    m = _ALLOWED_SHAPE.match(url)
    return bool(m) and path_allowed(m.group(1), m.group(2) or "", policy)


def target_allowed(target: str, policy: Policy | None = None, *, relative_ok: bool = True) -> bool:
    """Whether a link target may stay. Every reading of it must pass; a
    relative one is resolved against the page the reply is posted on."""
    policy = policy or _DEFAULT_POLICY
    for v in _variants(target):
        if _PROTOCOL_RELATIVE.match(v):
            url = "https:" + v
        elif _SCHEME.match(v):
            url = v
        elif not relative_ok:
            return False
        elif v == "" or v.startswith("#"):
            continue
        else:
            try:
                url = urljoin(policy.base, v)
            except ValueError:
                return False
        if not _absolute_allowed(url, policy):
            return False
    return True


def _decode_safe_entities(text: str) -> str:
    def repl(m: re.Match[str]) -> str:
        ch = html.unescape(m.group(0))
        return ch if _SAFE_DECODED.match(ch) else m.group(0)

    return _ENTITY.sub(repl, text)


def gfm_trim(url: str) -> str:
    """What GitHub's autolink extension keeps of `url`: trailing ?!.,:*_~,
    an unbalanced trailing `)` and a trailing entity-like `&name;` go."""
    while url:
        if url[-1] in "?!.,:*_~":
            url = url[:-1]
        elif url[-1] == ")" and url.count(")") > url.count("("):
            url = url[:-1]
        elif re.search(r"&[A-Za-z0-9]+;$", url):
            url = re.sub(r"&[A-Za-z0-9]+;$", "", url)
        else:
            break
    return url


# A URL in code or in a quote ends at these too; whatever of them GitHub's
# autolink keeps can only lengthen the last path segment of the URL that
# passed, never lead to another repository or ref.
_QUOTE_TAIL = "`'\"]>)?!.,:*_~"


def _strip_quote_tail(url: str) -> str:
    while url and url[-1] in _QUOTE_TAIL:
        url = url[:-1]
    return url


# Attributes whose value a browser follows or loads.
_URL_ATTRS = frozenset(
    {"href", "src", "srcset", "action", "formaction", "poster", "cite", "background", "data",
     "xlink:href", "longdesc", "ping", "lowsrc", "dynsrc", "codebase", "manifest", "icon",
     "itemtype", "archive", "classid", "usemap", "imagesrcset", "profile"}
)
_SPACE = " \t\n\r\f"

# A CommonMark raw-HTML open tag (spec 6.6), kept narrower than the spec:
# quoted values hold no line ending. Only such a span is set aside.
_CM_WS = r"(?:[ \t]+\n?[ \t]*|\n[ \t]*)"
_CM_OPT_WS = r"[ \t]*\n?[ \t]*"
_CM_OPEN_TAG = re.compile(
    r"<[A-Za-z][A-Za-z0-9-]*"
    rf"(?:{_CM_WS}[A-Za-z_:][A-Za-z0-9_.:-]*(?:{_CM_OPT_WS}={_CM_OPT_WS}"
    r"(?:[^\s\"'=<>`]+|'[^'\n]*'|\"[^\"\n]*\"))?)*"
    rf"{_CM_OPT_WS}/?>"
)


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
    def __init__(self, policy: Policy) -> None:
        self.policy = policy
        self.removed = 0
        self.kept: list[str] = []

    def allowed(self, target: str, *, relative_ok: bool = True) -> bool:
        # A span set aside below is never a link target: GitHub may not read
        # it as the span this pass did (fail closed).
        if _P_OPEN in target or _P_CLOSE in target:
            return False
        return target_allowed(target, self.policy, relative_ok=relative_ok)

    def drop(self, keep: str = "") -> str:
        self.removed += 1
        return f"{keep} {REMOVED}" if keep.strip() else REMOVED

    def protect(self, span: str) -> str:
        self.kept.append(span)
        return f"{_P_OPEN}{len(self.kept) - 1}{_P_CLOSE}"

    def restore(self, text: str) -> str:
        while _PLACEHOLDER.search(text):
            text = _PLACEHOLDER.sub(lambda m: self.kept[int(m.group(1))], text)
        return text

    # <https://host/...> and <user@host>: CommonMark autolinks. No e-mail
    # domain is allowed.
    _AUTOLINK = re.compile(
        r"<([A-Za-z][A-Za-z0-9+.-]{1,31}:[^\s<>\x00-\x1f\x7f]*)>"
        r"|<[A-Za-z0-9.!#$%&'*+/=?^_`{|}~-]+@[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?"
        r"(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*>"
    )

    def autolinks(self, text: str) -> str:
        return self._AUTOLINK.sub(
            lambda m: self.protect(m.group(0)) if m.group(1) and self.allowed(m.group(1)) else self.drop(), text
        )

    def tags(self, text: str) -> str:
        """A foreign target in a URL attribute removes the whole start tag (a
        closing </a> left behind renders as nothing). A tag that never closes
        loses its `<`, so no tag can start there (fail closed). A kept tag
        still loses any foreign URL in its other attributes (title=...)."""
        out = []
        pos = 0
        for m in re.finditer(r"<[A-Za-z]", text):
            i = m.start()
            if i < pos:
                continue
            scanned = scan_start_tag(text, i)
            if scanned is None:
                if "=" in text[i:].split("\n\n", 1)[0]:
                    out.append(text[pos:i] + self.drop() + " ")
                    pos = i + 1
                continue
            end, attrs = scanned
            bad = any(
                name in _URL_ATTRS
                and any(not self.allowed(t) for t in (re.split(r"[\s,]+", html.unescape(value)) or [""]))
                for name, value in attrs
            )
            if bad:
                out.append(text[pos:i] + self.drop())
            elif _CM_OPEN_TAG.fullmatch(text, i, end):
                # A CommonMark raw-HTML open tag stays as it is; its quoted
                # values are read only here. Anything GitHub might read as
                # text instead is left to the passes below.
                out.append(text[pos:i] + self.protect(self.scan(text[i:end], quoted=True)))
            else:
                # Not a tag GitHub renders (`<https://...>` with a space): the
                # passes below read it.
                continue
            pos = end
        out.append(text[pos:])
        return "".join(out)

    # [text](target "title") and ![alt](target): text stays, the link goes.
    # An escaped `\[` opens no link.
    _INLINE = re.compile(
        r"(?<!\\)(!?)\[((?:[^\[\]\\]|\\.|\[(?:[^\[\]\\]|\\.)*\])*)\]\(\s*(<[^<>\n]*>|(?:[^\s()\\]|\\.|\([^\s()]*\))*)"
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
            # Link text holding link syntax may be parsed differently by
            # GitHub: the outer link goes and the inner ones are read below.
            if not self.allowed(target) or "](" in m.group(2) or "]:" in m.group(2):
                return self.drop(m.group(2))
            # The link stays; URLs in its text are checked on their own.
            s, a, b = m.start(), m.start(2), m.end(2)
            whole = m.group(0)
            return self.protect(whole[: a - s] + self.scan(m.group(2)) + whole[b - s :])

        text = self._INLINE.sub(repl, text)
        return self._LEFTOVER.sub(
            lambda m: m.group(0) if self.allowed(m.group(1).strip("<>")) else f"] {self.drop()} (", text
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
            if self.allowed(target):
                return m.group(0)
            return "]: " + self.drop()

        return self._REFDEF.sub(repl, text)

    # Whatever is left, in prose or code. A URL with any scheme and a `www.`
    # name run, as in GitHub's autolinks, to whitespace or `<` (or a span kept
    # above); `//host.tld/...` or `\\host.tld` (a dotted host, so `//go:embed`
    # and `// comment` stay); mailto:, xmpp:, tel: and sms: links; and e-mail
    # addresses, which GitHub links too (the domain must end in letters, so
    # `pkg@v1.2.3` stays, and `icon@2x.png` is a file name, not an address).
    _END = r"[^\s<" + _P_OPEN + r"]"
    _URL = re.compile(
        r"(?<![A-Za-z0-9+.-])[A-Za-z][A-Za-z0-9+.-]*:(?:\\?[/\\]){2}" + _END + "*"
        r"|(?<![A-Za-z0-9.@/:_\\-])www\." + _END + "+"
        r"|(?<![A-Za-z0-9.@/:_\\-])[/\\]{2}[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+(?=[/\\:?#\s)\]]|$)[^\s<>\"'`\[\]"
        + _P_OPEN + r"]*"
        r"|(?<![A-Za-z0-9+.-])(?:mailto|xmpp|tel|sms):[^\s<>\"'`\[\]" + _P_OPEN + r"]+"
        r"|(?<![A-Za-z0-9._+/-])[A-Za-z0-9._+-]+@(?!\d+(?:\.\d+)?x\.)[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}"
        r"(?![A-Za-z0-9-])",
        re.I,
    )
    _NO_SLASH = re.compile(r"^(?:mailto|xmpp|tel|sms):|^[^:/\\]*@", re.I)
    # Inside a tag a URL ends at a quote as well.
    _QUOTED = re.compile(r"(?<![A-Za-z0-9+.-])[A-Za-z][A-Za-z0-9+.-]*:(?:\\?[/\\]){2}[^\s<>\"'`]*", re.I)

    def scan(self, text: str, *, quoted: bool = False) -> str:
        def repl(m: re.Match[str]) -> str:
            found = m.group(0)
            linked = gfm_trim(found)
            tail = found[len(linked) :]
            if self._NO_SLASH.match(found):
                return self.drop() + tail
            if found[:1] in "/\\":
                return m.group(0) if self.allowed("https:" + linked, relative_ok=False) else self.drop() + tail
            # What GitHub links is `linked`; it passes only if the URL in it
            # passes and the rest is quote or bracket characters.
            url = _strip_quote_tail(linked)
            check = "http://" + url if url.lower().startswith("www.") else url
            if url and self.allowed(check, relative_ok=False):
                return found
            return self.drop() + tail

        return (self._QUOTED if quoted else self._URL).sub(repl, text)

    def run(self, text: str) -> str:
        text = text.replace(_P_OPEN, "\ufffd").replace(_P_CLOSE, "\ufffd")
        text = _decode_safe_entities(text)
        text = self.autolinks(text)
        text = self.tags(text)
        text = self.inline_links(text)
        text = self.reference_definitions(text)
        return self.restore(self.scan(text))


def filter_links(text: str, policy: Policy | None = None) -> tuple[str, int]:
    """Returns the text with foreign links removed, and how many were removed."""
    f = _Filter(policy or _DEFAULT_POLICY)
    out = f.run(text)
    return out, f.removed


# --- The gate: what GitHub renders is what is checked. ---------------------

# Every attribute of rendered HTML that holds a URL.
_GATE_ATTRS = _URL_ATTRS | {"data-canonical-src"}
_CAMO = re.compile(r"^https?://camo\.githubusercontent\.com/", re.I)
_USER_MENTION = re.compile(r"^https://github\.com/[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})/?$")
_TEAM_MENTION = re.compile(r"^https://github\.com/orgs/[A-Za-z0-9-]+/teams/[A-Za-z0-9._-]+/?$")


class _LinkCollector(HTMLParser):
    def __init__(self) -> None:
        super().__init__(convert_charrefs=True)
        self.links: list[tuple[str, dict[str, str], str, str]] = []

    def handle_starttag(self, tag, attrs):
        attributes: dict[str, str] = {}
        for name, value in attrs:
            attributes.setdefault(name.lower(), value or "")
        for name, value in attrs:
            name = name.lower()
            if name not in _GATE_ATTRS or value is None:
                continue
            if name in ("srcset", "imagesrcset"):
                urls = [part.split()[0] for part in value.split(",") if part.split()]
            elif name == "ping":
                urls = value.split()
            else:
                urls = [value]
            for url in urls or [value]:
                self.links.append((tag.lower(), attributes, name, url))

    handle_startendtag = handle_starttag


def rendered_link_failures(rendered: str, policy: Policy | None = None) -> int:
    """How many URLs in GitHub's rendering of the reply fail the policy. A
    camo (image proxy) URL passes only as the src of an image whose
    data-canonical-src passes, and then also as the href of the link GitHub
    wraps around it; a mention passes as a link to a profile or team page."""
    policy = policy or _DEFAULT_POLICY
    collector = _LinkCollector()
    collector.feed(rendered)
    collector.close()
    camo_ok = {
        url
        for tag, attributes, name, url in collector.links
        if name == "src" and _CAMO.match(url) and attributes.get("data-canonical-src")
        and target_allowed(attributes["data-canonical-src"], policy)
    }
    failures = 0
    for tag, attributes, name, url in collector.links:
        url = url.strip()
        classes = attributes.get("class", "").split()
        if _CAMO.match(url):
            ok = url in camo_ok
        elif tag == "a" and name == "href" and "user-mention" in classes:
            ok = bool(_USER_MENTION.match(url))
        elif tag == "a" and name == "href" and "team-mention" in classes:
            ok = bool(_TEAM_MENTION.match(url))
        else:
            ok = target_allowed(url, policy)
        failures += not ok
    return failures


def render_markdown(text: str, repo: str) -> str | None:
    """GitHub's rendering of `text` as a comment in `repo`, or None on any error."""
    try:
        proc = subprocess.run(
            ["gh", "api", "markdown", "-X", "POST", "-F", "text=@-", "-f", "mode=gfm", "-f", f"context={repo}"],
            input=text, capture_output=True, text=True, timeout=60,
        )
    except (OSError, subprocess.SubprocessError):
        return None
    return proc.stdout if proc.returncode == 0 else None


FALLBACK_NOTE = "Links in this reply were removed for safety, so it is shown as plain text."


def plain_fallback(text: str) -> str:
    """`text` inside a tilde fence longer than any run of tildes in it, so
    nothing in it can close the fence: GitHub renders it as text, no link."""
    longest = max((len(run) for run in re.findall(r"~+", text)), default=0)
    fence = "~" * max(4, longest + 1)
    if not text.endswith("\n"):
        text += "\n"
    return f"{FALLBACK_NOTE}\n\n{fence}\n{text}{fence}\n"


def gate(text: str, policy: Policy) -> str:
    """Returns `text` if every link GitHub renders from it passes, else the
    plain fallback."""
    rendered = render_markdown(text, policy.repo)
    if rendered is None:
        print("::warning::Could not render the reply to check its links; it was posted as plain text.")
        return plain_fallback(text)
    try:
        failures = rendered_link_failures(rendered, policy)
    except Exception:  # noqa: BLE001 - any parse trouble fails closed
        failures = -1
    if failures:
        count = "Some" if failures < 0 else str(failures)
        print(f"::warning::{count} link(s) in the rendered reply are outside the allowlist; it was posted as plain text.")
        return plain_fallback(text)
    return text


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description="Remove links outside the allowlist from a bot reply.")
    parser.add_argument("--repo", default=DEFAULT_REPO, help="OWNER/REPO the reply is posted in")
    parser.add_argument("--base", default="", help="URL of the issue or pull request the reply is posted on")
    parser.add_argument("--head-sha", default="", help="commit under review, which may be linked")
    parser.add_argument("--gate", action="store_true", help="check GitHub's rendering; fall back to plain text")
    parser.add_argument("--no-filter", action="store_true", help="skip the text pass (the input already had it)")
    parser.add_argument("src")
    parser.add_argument("dst")
    args = parser.parse_args(argv[1:])
    if args.base and not args.base.lower().startswith("https://github.com/"):
        parser.error("--base must be a https://github.com/ URL")
    policy = Policy(repo=args.repo, base=args.base, head_sha=args.head_sha, checker=GhChecker())
    with open(args.src, encoding="utf-8", errors="replace", newline="") as fh:
        text = fh.read()
    if not args.no_filter:
        text, removed = filter_links(text, policy)
        if removed:
            print(
                f"::warning::Removed {removed} link(s) outside the allowlist (this repository, "
                "MHSanaei/3x-ui and XTLS on GitHub; docs.github.com; xtls.github.io) from the reply before posting it."
            )
    if args.gate:
        text = gate(text, policy)
    with open(args.dst, "w", encoding="utf-8", newline="") as fh:
        fh.write(text)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
