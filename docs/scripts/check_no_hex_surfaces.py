#!/usr/bin/env python3
# Copyright 2026 ZyvorAI Labs Private Limited
# SPDX-License-Identifier: Apache-2.0
"""Fail if raw hex/rgb color literals appear outside the token file.

Mirrors the discipline enforced on the React dashboard by
web/dashboard/scripts/check-no-hex-surfaces.mjs: color values in the actual
design-system surfaces — Jinja templates/hooks and page hero front matter —
must come from the CSS custom properties defined in
docs/stylesheets/apple-glass.css, never be hardcoded inline.

Only front matter (the `hero:` block) is scanned in markdown files, not
page prose, since guides legitimately reference hex codes descriptively
(e.g. "Zyvor orange (`#f97316`)") without that being a UI surface.
"""

import re
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
TOKEN_FILE = REPO_ROOT / "docs" / "stylesheets" / "apple-glass.css"

COLOR_RE = re.compile(r"(?<!&)#[0-9a-fA-F]{3,8}\b|rgba?\(")
FRONT_MATTER_RE = re.compile(r"\A---\n(.*?\n)---\n", re.DOTALL)

SCAN_GLOBS = [
    "docs/overrides/**/*.html",
    "docs/overrides/**/*.py",
    "docs/**/*.md",
]


def front_matter_only(text: str) -> str:
    match = FRONT_MATTER_RE.match(text)
    return match.group(1) if match else ""


def main() -> int:
    violations = []
    seen = set()
    for pattern in SCAN_GLOBS:
        for path in REPO_ROOT.glob(pattern):
            if path == TOKEN_FILE or path in seen:
                continue
            seen.add(path)
            text = path.read_text(encoding="utf-8", errors="ignore")
            if path.suffix == ".md":
                text = front_matter_only(text)
            for lineno, line in enumerate(text.splitlines(), start=1):
                if COLOR_RE.search(line):
                    violations.append(f"{path.relative_to(REPO_ROOT)}:{lineno}: {line.strip()}")

    if violations:
        print("Raw hex/rgb color literals found outside docs/stylesheets/apple-glass.css:")
        for v in violations:
            print(f"  {v}")
        print("\nUse the CSS custom properties (--primary, --tone-*, --glass-*, ...) instead.")
        return 1

    print("check_no_hex_surfaces: OK")
    return 0


if __name__ == "__main__":
    sys.exit(main())
