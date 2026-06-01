#!/usr/bin/env python3
"""Fixed-cost delta computer for Terraform PR diffs.

Reads a unified diff on stdin (typically `git diff <base>...HEAD -- '*.tf'`),
identifies resource blocks added or removed, evaluates literal `count` and
`for_each` sizes, and follows local module sources recursively. Emits
markdown summary to stdout with a `<!-- cost-delta-bot -->` marker so the
PR-comment workflow can upsert a single comment instead of stacking.

Resource-type → $/mo lookup MIRRORS the "Known fixed-cost services" table
in CLAUDE.md / AGENTS.md. They are kept in sync by
`quality-kit/scripts/audit-conventions.sh` (drift fails CI).

Stdlib only — no third-party packages.

Coverage:
  - resource "<type>" "<name>" { count = <int-literal> } → counts × $/mo
  - resource "<type>" "<name>" { for_each = <literal-list> } → len × $/mo
  - resource "<type>" "<name>" { for_each = <literal-map> } → len × $/mo
  - module "<name>" { source = "./..." } → recursively scan that path
  - module "<name>" { source = "../..." } → recursively scan that path
  - module "<name>" { source = "git::..." } → not expanded; annotated note

Static-analysis honesty:
  - Dynamic count/for_each (variable or expression) → treated as 1 (safe
    over-conservative — better to flag a possible cost than miss one).
  - Remote-source modules are listed by name in a "Not analyzed" section
    so reviewers know what's outside the picture.
  - References to other resources / data sources are not chased.

Usage:
  cd <repo>
  git fetch origin "$BASE_REF" --quiet
  git diff "origin/$BASE_REF...HEAD" -- '*.tf' \\
    | python3 .github/scripts/cost-delta.py \\
        --repo-root . --base-ref "origin/$BASE_REF" > comment.md
"""

from __future__ import annotations

import argparse
import os
import re
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Iterable

# ---------------------------------------------------------------------------
# Price table. MUST stay in sync with CLAUDE.md / AGENTS.md Fixed-cost section.
# Audit script `audit-conventions.sh` enforces parity.
# ---------------------------------------------------------------------------
FIXED_COST_TABLE: dict[str, float] = {
    "aws_wafv2_web_acl": 5.00,
    "aws_wafv2_rule_group": 1.00,
    "aws_cloudwatch_metric_alarm": 0.10,
    "aws_kms_key": 1.00,
    "aws_kms_replica_key": 1.00,
    "aws_nat_gateway": 32.00,
    "aws_route53_zone": 0.50,
    "aws_lb": 16.00,
    "aws_db_instance": 20.00,
    "aws_elasticache_cluster": 15.00,
    "aws_elasticache_replication_group": 20.00,
    "aws_eks_cluster": 72.00,
    "aws_msk_cluster": 130.00,
}

# ---------------------------------------------------------------------------
# HCL block extraction. Regex-based — tolerant of comments and brace depth.
# ---------------------------------------------------------------------------

# Matches the opening line of a `resource "TYPE" "NAME" {` block. Captures
# (type, name, position-of-opening-brace).
RESOURCE_HEAD_RE = re.compile(
    r'^\s*resource\s+"([a-z0-9_]+)"\s+"([A-Za-z0-9_\-]+)"\s*\{',
    re.MULTILINE,
)

# Matches the opening line of a `module "NAME" {` block.
MODULE_HEAD_RE = re.compile(
    r'^\s*module\s+"([A-Za-z0-9_\-]+)"\s*\{',
    re.MULTILINE,
)

# Block-comment stripper so brace counters don't get confused by `}` inside
# `/* ... */`. Line comments (`#` and `//`) are intentionally NOT stripped:
#   - `#` lines never match our anchored regexes (RESOURCE_HEAD_RE etc.)
#   - `//` lines would falsely catch the `//modules/...` sub-path in
#     terraform git module sources like
#     `source = "git::https://github.com/foo/bar//modules/baz"`.
BLOCK_COMMENT_RE = re.compile(r"/\*.*?\*/", re.DOTALL)

# Once we have a block's body text, find these literals.
COUNT_INT_RE = re.compile(r"^\s*count\s*=\s*(\d+)\s*(?:$|#|//)", re.MULTILINE)
FOR_EACH_LITERAL_RE = re.compile(
    r"^\s*for_each\s*=\s*(\{[^{}]*\}|\[[^\[\]]*\])",
    re.MULTILINE,
)
SOURCE_RE = re.compile(r'^\s*source\s*=\s*"([^"]+)"', re.MULTILINE)


def _strip_comments(text: str) -> str:
    return BLOCK_COMMENT_RE.sub("", text)


def _find_block_body(text: str, brace_pos: int) -> tuple[str, int]:
    """Return (body, end_index) for the block whose opening `{` is at brace_pos.

    Uses simple brace counting on comment-stripped text. Caller is responsible
    for passing already-stripped text.
    """
    depth = 1
    i = brace_pos + 1
    while i < len(text) and depth > 0:
        c = text[i]
        if c == "{":
            depth += 1
        elif c == "}":
            depth -= 1
        i += 1
    return text[brace_pos + 1 : i - 1], i


# ---------------------------------------------------------------------------
# Block extraction primitives.
# ---------------------------------------------------------------------------


@dataclass(frozen=True)
class ResourceBlock:
    type: str
    name: str
    count: int  # 0 means the block evaluates to nothing (e.g. count = 0)
    body: str


@dataclass(frozen=True)
class ModuleBlock:
    name: str
    source: str
    body: str


def _count_list_literal(literal: str) -> int:
    """Count comma-separated elements in a `[a, b, c]` literal."""
    inner = literal[1:-1].strip()
    if not inner:
        return 0
    return len([x for x in inner.split(",") if x.strip()])


def _count_map_literal(literal: str) -> int:
    """Count `key =` entries at depth 0 in a `{ ... }` literal."""
    inner = literal[1:-1].strip()
    if not inner:
        return 0
    depth = 0
    entries = 0
    for raw in inner.splitlines():
        line = raw.strip()
        if line and depth == 0 and "=" in line and not line.startswith("}"):
            entries += 1
        depth += line.count("{") - line.count("}")
    return entries


def _count_for_block(body: str) -> int:
    """Return the literal count for this block.

    Precedence:
      - `count = N` (literal integer) → N
      - `for_each = [literal-list]`   → number of comma-separated elements
      - `for_each = {literal-map}`    → number of `key = ` entries
      - dynamic / unrecognised        → 1 (safe over-conservative)
      - no count/for_each             → 1 (the resource itself)
    """
    count_match = COUNT_INT_RE.search(body)
    if count_match:
        return int(count_match.group(1))

    for_each_match = FOR_EACH_LITERAL_RE.search(body)
    if not for_each_match:
        return 1

    literal = for_each_match.group(1).strip()
    if literal.startswith("["):
        return _count_list_literal(literal)
    if literal.startswith("{"):
        return _count_map_literal(literal)
    return 1


def extract_resources(text: str) -> list[ResourceBlock]:
    """Walk text and return every resource block with its evaluated count."""
    stripped = _strip_comments(text)
    out: list[ResourceBlock] = []
    pos = 0
    while True:
        m = RESOURCE_HEAD_RE.search(stripped, pos)
        if not m:
            break
        rtype, rname = m.group(1), m.group(2)
        brace_pos = stripped.index("{", m.end() - 1)
        body, end = _find_block_body(stripped, brace_pos)
        out.append(
            ResourceBlock(
                type=rtype,
                name=rname,
                count=_count_for_block(body),
                body=body,
            )
        )
        pos = end
    return out


def extract_modules(text: str) -> list[ModuleBlock]:
    """Walk text and return every module block with its declared source."""
    stripped = _strip_comments(text)
    out: list[ModuleBlock] = []
    pos = 0
    while True:
        m = MODULE_HEAD_RE.search(stripped, pos)
        if not m:
            break
        name = m.group(1)
        brace_pos = stripped.index("{", m.end() - 1)
        body, end = _find_block_body(stripped, brace_pos)
        src_match = SOURCE_RE.search(body)
        source = src_match.group(1) if src_match else ""
        out.append(ModuleBlock(name=name, source=source, body=body))
        pos = end
    return out


# ---------------------------------------------------------------------------
# Diff parsing — find which .tf files changed and on which side.
# ---------------------------------------------------------------------------


@dataclass
class FileChange:
    path: str
    added: bool  # whether the file exists in the head version
    removed: bool  # whether it existed in the base version


DIFF_HEADER_RE = re.compile(r"^diff --git a/(.+) b/(.+)$")
NEW_FILE_RE = re.compile(r"^new file mode")
DELETED_FILE_RE = re.compile(r"^deleted file mode")
RENAME_FROM_RE = re.compile(r"^rename from (.+)$")


@dataclass
class _DiffSection:
    """Raw header + body lines for one `diff --git` block."""

    a_path: str
    b_path: str
    body_lines: list[str]


def _split_diff_sections(diff_text: str) -> list[_DiffSection]:
    """Split a unified diff into per-file sections by `diff --git` header."""
    sections: list[_DiffSection] = []
    current: _DiffSection | None = None
    for line in diff_text.splitlines():
        header = DIFF_HEADER_RE.match(line)
        if header:
            if current is not None:
                sections.append(current)
            current = _DiffSection(
                a_path=header.group(1),
                b_path=header.group(2),
                body_lines=[],
            )
            continue
        if current is not None:
            current.body_lines.append(line)
    if current is not None:
        sections.append(current)
    return sections


def _section_to_filechanges(sec: _DiffSection) -> list[FileChange]:
    """Yield FileChange objects for a single diff section.

    - Pure rename (a != b, no new/deleted markers) → two changes: a removed,
      b added.
    - Otherwise: one change for b_path with added/removed inferred from the
      `new file mode` / `deleted file mode` markers.
    """
    is_new = any(NEW_FILE_RE.match(ln) for ln in sec.body_lines)
    is_deleted = any(DELETED_FILE_RE.match(ln) for ln in sec.body_lines)
    rename_from = next(
        (
            RENAME_FROM_RE.match(ln).group(1)
            for ln in sec.body_lines
            if RENAME_FROM_RE.match(ln)
        ),
        None,
    )

    if rename_from and rename_from != sec.b_path:
        return [
            FileChange(path=rename_from, added=False, removed=True),
            FileChange(path=sec.b_path, added=True, removed=False),
        ]
    return [
        FileChange(
            path=sec.b_path,
            added=not is_deleted,
            removed=not is_new,
        )
    ]


def _dedupe_tf(changes: Iterable[FileChange]) -> list[FileChange]:
    seen: set[str] = set()
    out: list[FileChange] = []
    for c in changes:
        if c.path in seen or not c.path.endswith(".tf"):
            continue
        seen.add(c.path)
        out.append(c)
    return out


def parse_diff_file_list(diff_text: str) -> list[FileChange]:
    """From a unified diff, list the .tf files that changed.

    For renames (`rename from X` / `rename to Y`), both X and Y are
    returned — X as removed-only, Y as added-only — so the cost diff
    correctly subtracts the old contribution and adds the new.
    """
    sections = _split_diff_sections(diff_text)
    changes: list[FileChange] = []
    for sec in sections:
        changes.extend(_section_to_filechanges(sec))
    return _dedupe_tf(changes)


# ---------------------------------------------------------------------------
# Git helpers.
# ---------------------------------------------------------------------------


def git_show(ref: str, path: str, repo_root: Path) -> str | None:
    """Return the contents of `path` at `ref`, or None if it doesn't exist."""
    try:
        return subprocess.check_output(
            ["git", "show", f"{ref}:{path}"],
            cwd=repo_root,
            stderr=subprocess.DEVNULL,
            text=True,
        )
    except subprocess.CalledProcessError:
        return None


def read_head(path: Path) -> str | None:
    try:
        return path.read_text()
    except FileNotFoundError:
        return None


# ---------------------------------------------------------------------------
# Module fan-out — recursively gather resources from local module sources.
# ---------------------------------------------------------------------------


def gather_resources_from_dir(
    directory: Path,
    seen: set[Path] | None = None,
) -> tuple[list[ResourceBlock], list[ModuleBlock]]:
    """Read every *.tf in `directory` and return resources + module refs.

    Recursively expands local-source modules. Cycle-safe via `seen`.
    """
    if seen is None:
        seen = set()
    real = directory.resolve()
    if real in seen or not real.is_dir():
        return [], []
    seen.add(real)

    resources: list[ResourceBlock] = []
    modules: list[ModuleBlock] = []
    for tf in sorted(real.glob("*.tf")):
        text = read_head(tf)
        if not text:
            continue
        resources.extend(extract_resources(text))
        for mod in extract_modules(text):
            modules.append(mod)
            if mod.source.startswith(("./", "../", "/")):
                # Local-path module — recurse.
                local_path = (real / mod.source).resolve()
                sub_r, sub_m = gather_resources_from_dir(local_path, seen)
                resources.extend(sub_r)
                modules.extend(sub_m)
    return resources, modules


def gather_resources_at_ref(
    paths: Iterable[str],
    ref: str | None,
    repo_root: Path,
) -> tuple[list[ResourceBlock], list[ModuleBlock]]:
    """Gather resources + modules from .tf files at the given ref.

    `ref=None` means read the working tree (HEAD checkout).
    """
    resources: list[ResourceBlock] = []
    modules: list[ModuleBlock] = []
    seen_dirs: set[Path] = set()

    for path in paths:
        if ref is None:
            text = read_head(repo_root / path)
        else:
            text = git_show(ref, path, repo_root)
        if not text:
            continue
        resources.extend(extract_resources(text))
        for mod in extract_modules(text):
            modules.append(mod)
            if mod.source.startswith(("./", "../", "/")):
                base_dir = (repo_root / path).parent
                local_path = (base_dir / mod.source).resolve()
                # Module fan-out for a `ref` snapshot is tricky: the local
                # path on disk reflects HEAD, not the ref. We only walk it
                # for HEAD (ref is None) to avoid mixing snapshots.
                if ref is None:
                    sub_r, sub_m = gather_resources_from_dir(local_path, seen_dirs)
                    resources.extend(sub_r)
                    modules.extend(sub_m)
    return resources, modules


# ---------------------------------------------------------------------------
# Cost arithmetic.
# ---------------------------------------------------------------------------


def fixed_cost_for(resources: list[ResourceBlock]) -> dict[str, tuple[int, float]]:
    """Aggregate {type: (count, subtotal_$/mo)} across all blocks."""
    agg: dict[str, tuple[int, float]] = {}
    for r in resources:
        unit = FIXED_COST_TABLE.get(r.type)
        if unit is None or r.count == 0:
            continue
        prev_count, prev_total = agg.get(r.type, (0, 0.0))
        agg[r.type] = (prev_count + r.count, prev_total + unit * r.count)
    return agg


# ---------------------------------------------------------------------------
# Markdown rendering.
# ---------------------------------------------------------------------------


@dataclass
class _DeltaRow:
    rtype: str
    count: int
    unit: float
    subtotal: float


def _build_delta_rows(
    base_agg: dict[str, tuple[int, float]],
    head_agg: dict[str, tuple[int, float]],
) -> tuple[list[_DeltaRow], list[_DeltaRow], float]:
    """Compute (added_rows, removed_rows, net_delta_$) from base/head aggs."""
    added: list[_DeltaRow] = []
    removed: list[_DeltaRow] = []
    base_total = 0.0
    head_total = 0.0
    for t in sorted(set(head_agg) | set(base_agg)):
        b_count, b_sub = base_agg.get(t, (0, 0.0))
        h_count, h_sub = head_agg.get(t, (0, 0.0))
        base_total += b_sub
        head_total += h_sub
        d_count = h_count - b_count
        d_sub = h_sub - b_sub
        unit = FIXED_COST_TABLE.get(t, 0.0)
        if d_count > 0:
            added.append(_DeltaRow(t, d_count, unit, d_sub))
        elif d_count < 0:
            removed.append(_DeltaRow(t, -d_count, unit, -d_sub))
    return added, removed, head_total - base_total


def _render_net_header(net: float, no_changes: bool) -> list[str]:
    if no_changes:
        return [
            "**$0/mo** — no fixed-cost resource types added or removed.",
            "",
            "_Static analysis: only counts resource types listed in "
            "[CLAUDE.md / AGENTS.md → Fixed-cost discipline](../../CLAUDE.md). "
            "Pay-per-request resources (Lambda, DDB on-demand, S3, API Gateway "
            "HTTP, CloudFront) don't count toward the fixed-cost ledger._",
        ]
    if abs(net) < 0.005:
        return ["**Net: $0.00/mo** — adds and removes cancel out."]
    if net > 0:
        return [f"**Net: +${net:.2f}/mo**"]
    return [f"**Net: -${abs(net):.2f}/mo**"]


def _render_delta_table(
    rows: list[_DeltaRow],
    heading: str,
    last_column_title: str,
) -> list[str]:
    if not rows:
        return []
    out = [
        f"### {heading}",
        "",
        f"| Resource type | Count | Unit ($/mo) | {last_column_title} |",
        "|---|---|---|---|",
    ]
    for r in rows:
        out.append(f"| `{r.rtype}` | {r.count} | ${r.unit:.2f} | ${r.subtotal:.2f} |")
    out.append("")
    return out


def _render_remote_module_section(remote_modules: list[ModuleBlock]) -> list[str]:
    if not remote_modules:
        return []
    out = [
        "### Not analyzed",
        "",
        "Modules with remote sources are not expanded. Inspect manually "
        "if you suspect they add fixed-cost resources:",
        "",
    ]
    out.extend(f"- `module.{m.name}` from `{m.source}`" for m in remote_modules)
    out.append("")
    return out


def _render_soft_cap_warning(net: float) -> list[str]:
    if net <= 5.0:
        return []
    return [
        "---",
        "",
        (
            f"⚠️ Net add exceeds $5/mo. Per the [Fixed-cost discipline]"
            f"(../../CLAUDE.md) soft cap, please prefix the PR title with "
            f"`[+${net:.2f}/mo]` so the cost is visible in the PR list."
        ),
        "",
    ]


_RENDER_FOOTER = (
    "_Static analysis: evaluates literal `count` / `for_each`, recursively "
    "follows local-path modules. Dynamic values (variable references) are "
    "conservatively treated as 1. Resources referenced via remote-source "
    "modules are listed above but their cost is not summed._"
)


def render_markdown(
    base_agg: dict[str, tuple[int, float]],
    head_agg: dict[str, tuple[int, float]],
    remote_modules: list[ModuleBlock],
) -> str:
    """Compose the PR comment body."""
    added, removed, net = _build_delta_rows(base_agg, head_agg)
    no_changes = not added and not removed and abs(net) < 0.005

    # When net is zero and no remote modules are flagged either, emit only
    # the short "$0/mo" header — keeps the comment quiet on clean PRs.
    if no_changes and not remote_modules:
        parts = ["<!-- cost-delta-bot -->", "## Fixed-cost delta", ""]
        parts.extend(_render_net_header(net, no_changes=True))
        return "\n".join(parts) + "\n"

    parts: list[str] = ["<!-- cost-delta-bot -->", "## Fixed-cost delta", ""]
    parts.extend(_render_net_header(net, no_changes=no_changes))
    parts.append("")
    parts.extend(_render_delta_table(added, "Added", "Subtotal ($/mo)"))
    parts.extend(_render_delta_table(removed, "Removed", "Subtotal saved ($/mo)"))
    parts.extend(_render_remote_module_section(remote_modules))
    parts.extend(_render_soft_cap_warning(net))
    parts.append(_RENDER_FOOTER)
    return "\n".join(parts) + "\n"


# ---------------------------------------------------------------------------
# Main.
# ---------------------------------------------------------------------------


def collect_remote_modules(
    head_modules: list[ModuleBlock],
    base_modules: list[ModuleBlock],
) -> list[ModuleBlock]:
    """Return remote-source modules that are present in HEAD but not in BASE.

    A baseline-existing remote module isn't "new cost" — only added ones are
    worth flagging.
    """
    remote_predicate = lambda m: m.source and not m.source.startswith(("./", "../", "/"))
    base_keys = {(m.name, m.source) for m in base_modules if remote_predicate(m)}
    return [
        m
        for m in head_modules
        if remote_predicate(m) and (m.name, m.source) not in base_keys
    ]


def _run_full_scan(repo_root: Path) -> str:
    """Compute the markdown for the entire current checkout."""
    all_paths = [
        str(p.relative_to(repo_root))
        for p in repo_root.rglob("*.tf")
        if ".terraform" not in p.parts and "_orphaned" not in p.parts
    ]
    head_resources, _ = gather_resources_at_ref(all_paths, None, repo_root)
    head_agg = fixed_cost_for(head_resources)
    return render_markdown({}, head_agg, [])


def _run_diff(diff_text: str, base_ref: str, repo_root: Path) -> str:
    """Compute the markdown for the diff between base_ref and HEAD."""
    file_changes = parse_diff_file_list(diff_text)
    if not file_changes:
        return render_markdown({}, {}, [])
    paths = [c.path for c in file_changes]
    base_resources, base_modules = gather_resources_at_ref(paths, base_ref, repo_root)
    head_resources, head_modules = gather_resources_at_ref(paths, None, repo_root)
    return render_markdown(
        fixed_cost_for(base_resources),
        fixed_cost_for(head_resources),
        collect_remote_modules(head_modules, base_modules),
    )


def main(argv: list[str]) -> None:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n", 1)[0])
    parser.add_argument(
        "--repo-root",
        default=".",
        help="Path to the repository root (where .tf files live). Defaults to CWD.",
    )
    parser.add_argument(
        "--base-ref",
        default=os.environ.get("BASE_REF", "origin/main"),
        help="Git ref to diff against. Default: origin/main or $BASE_REF.",
    )
    parser.add_argument(
        "--full-scan",
        action="store_true",
        help=(
            "Ignore stdin; compute the cost of the entire current checkout "
            "(useful for the static fleet-cost ledger). No base-ref comparison."
        ),
    )
    args = parser.parse_args(argv)
    repo_root = Path(args.repo_root).resolve()

    if args.full_scan:
        print(_run_full_scan(repo_root), end="")
    else:
        print(_run_diff(sys.stdin.read(), args.base_ref, repo_root), end="")


if __name__ == "__main__":
    main(sys.argv[1:])
