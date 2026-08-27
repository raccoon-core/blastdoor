"""Rewrite repository-relative links so they work on the published site.

Three pages are symlinks to files that live outside `docs/`: AGENTS.md and
CONTRIBUTING.md are read at the repository root by convention, and
examples/README.md sits beside the plans it describes. Symlinking them is what
stops the site and the repository drifting apart — but their links are written
relative to where the real file lives, and MkDocs resolves them relative to
where the page sits in `docs/`.

So each link is resolved against the real file's directory, and then:

  * anything the site publishes is re-pointed at its page, wherever the real
    file happens to live. CONTRIBUTING.md's `AGENTS.md` and AGENTS.md's
    `docs/hardening.md` are both on the site, so both become site links;
  * anything else — source files, the GitLab template, a `.rego` example — is
    sent to GitHub, because it is not on the site to link to.

The alternative was editing those links to absolute URLs in the source files,
which makes them worse to follow when reading the repository itself. Doing it
at build time keeps one source and costs nobody anything.
"""

from __future__ import annotations

import os
import posixpath
import re
from pathlib import Path

BRANCH = "main"

# [text](target) or [text](target "title"). Bare autolinks and reference-style
# links are not used in this repository, and are deliberately left alone.
LINK = re.compile(r"(?P<text>\[[^\]]*\])\((?P<target>[^)\s]+)(?P<title>\s+\"[^\"]*\")?\)")

# Fenced blocks are masked before substitution: a Rego sample contains things
# like `allow[...]` that must not be treated as a link.
FENCE = re.compile(r"(?ms)^(?P<fence>```|~~~).*?^(?P=fence)[ \t]*$")

SKIP_PREFIXES = ("http://", "https://", "//", "#", "mailto:", "tel:")

# Real path on disk -> the page's address within the site. Rebuilt every build,
# and every reload of `mkdocs serve`.
_published: dict[str, str] = {}


def on_files(files, config):  # noqa: ARG001
    """Record where each published file really lives, symlinks resolved."""
    _published.clear()
    for f in files:
        if f.is_documentation_page():
            _published[os.path.realpath(f.abs_src_path)] = f.src_uri
    return files


def _mask_fences(markdown: str) -> tuple[str, list[str]]:
    blocks: list[str] = []

    def stash(match: re.Match[str]) -> str:
        blocks.append(match.group(0))
        return f"\x00FENCE{len(blocks) - 1}\x00"

    return FENCE.sub(stash, markdown), blocks


def _restore_fences(markdown: str, blocks: list[str]) -> str:
    for i, block in enumerate(blocks):
        markdown = markdown.replace(f"\x00FENCE{i}\x00", block)
    return markdown


def on_page_markdown(markdown: str, page, config, files) -> str:  # noqa: ARG001
    repo_root = Path(config.config_file_path).parent.resolve()
    docs_dir = Path(config.docs_dir).resolve()
    repo_url = config.repo_url.rstrip("/")

    real_source = Path(os.path.realpath(page.file.abs_src_path))
    # Only the symlinked pages need this; a page authored in docs/ already has
    # its links written relative to where it sits.
    if real_source.parent == docs_dir:
        return markdown

    page_dir_in_site = posixpath.dirname(page.file.src_uri)
    masked, fences = _mask_fences(markdown)

    def rewrite(match: re.Match[str]) -> str:
        target = match.group("target")
        if target.startswith(SKIP_PREFIXES):
            return match.group(0)

        path_part, _, anchor = target.partition("#")
        if not path_part:
            return match.group(0)

        resolved = (real_source.parent / path_part).resolve()
        anchor = f"#{anchor}" if anchor else ""

        if site_uri := _published.get(str(resolved)):
            new_target = posixpath.relpath(site_uri, page_dir_in_site or ".")
        else:
            try:
                from_root = resolved.relative_to(repo_root)
            except ValueError:
                # Escapes the repository entirely; leave it for validation.
                return match.group(0)
            kind = "tree" if resolved.is_dir() else "blob"
            new_target = f"{repo_url}/{kind}/{BRANCH}/{from_root.as_posix()}"

        return f"{match.group('text')}({new_target}{anchor}{match.group('title') or ''})"

    return _restore_fences(LINK.sub(rewrite, masked), fences)
