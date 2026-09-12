# Copyright 2026 ZyvorAI Labs Private Limited
# SPDX-License-Identifier: Apache-2.0
"""MkDocs build hook.

Renders the Apple-glass hero / highlights / hub-band Jinja macros
(docs/overrides/partials/*.html) from a page's YAML front matter, and
prepends the resulting HTML to the page's markdown. `md_in_html` (already
enabled in mkdocs.yml) passes the raw HTML through untouched.

A page opts in with front matter like:

    ---
    hero:
      eyebrow: "PRODUCT"
      title: "Universal Runtime Portability"
      lead: "Deploy once. Move workloads across containers, Kubernetes, and VMs."
      tone: violet
      swatches:
        - {label: Podman, tone: sky}
      highlights:
        - {value: "3", label: "Runtimes, one spec"}
      hub_bands:
        - {title: Compose, description: "...", href: features/compose.md, tone: sky}
    footnotes:
      - {marker: "1", text: "16 runtime migration pairs.", href: "guides/migration/MIGRATION-INTERNALS.md", href_label: "See Migration Internals."}
    ---

A highlight item that cites a footnote adds `footnote: "1"` and the macro
renders a superscript link back to the matching `footnotes` entry, which is
appended at the very end of the page (not under the hero) -- every number
in a highlights band should be traceable to the doc that proves it.

Keeping markdown as data (front matter), not markup, means one template
change updates every hero page at once, and lets the macros mechanically
enforce the density caps (<=5 highlights, <=6 hub bands) in one place.
"""

from pathlib import Path

import jinja2

PARTIALS_DIR = Path(__file__).parent / "partials"
_env = jinja2.Environment(loader=jinja2.FileSystemLoader(str(PARTIALS_DIR)), autoescape=False)


def on_page_markdown(markdown, page, config, files):
    hero_data = page.meta.get("hero")
    footnote_items = page.meta.get("footnotes")

    if not hero_data:
        if footnote_items:
            markdown += "\n\n" + _env.get_template("footnotes.html").module.footnotes(footnote_items)
        return markdown

    tone = hero_data.get("tone", "sky")

    highlights_html = ""
    items = hero_data.get("highlights")
    if items:
        highlights_html = _env.get_template("highlights.html").module.highlights(items, tone=tone)

    hero_html = _env.get_template("hero.html").module.hero(
        eyebrow=hero_data.get("eyebrow", ""),
        title=hero_data.get("title") or page.title or "",
        lead=hero_data.get("lead", ""),
        tone=tone,
        swatches=hero_data.get("swatches", []),
        actions=hero_data.get("actions", []),
        highlights_html=highlights_html,
    )

    hub_html = ""
    bands = hero_data.get("hub_bands")
    if bands:
        hub_html = _env.get_template("hub_band.html").module.hub_band(bands)

    footnotes_html = ""
    if footnote_items:
        footnotes_html = "\n\n" + _env.get_template("footnotes.html").module.footnotes(footnote_items)

    return f"{hero_html}\n\n{hub_html}\n\n{markdown}{footnotes_html}"
