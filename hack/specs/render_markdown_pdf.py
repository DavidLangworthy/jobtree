#!/usr/bin/env python3
"""Render a small markdown note to a simple PDF.

This is intentionally lightweight: the node-failure spec artifact needs to be
downloadable and readable in CI, but it does not need a full pandoc/LaTeX
toolchain.  Headings, bullets, plain paragraphs, and fenced code blocks are
supported well enough for the literate spec notes under `specs/`.
"""

from __future__ import annotations

import html
import sys
from pathlib import Path

from reportlab.lib.pagesizes import letter
from reportlab.lib.styles import ParagraphStyle, getSampleStyleSheet
from reportlab.lib.units import inch
from reportlab.platypus import Paragraph, Preformatted, SimpleDocTemplate, Spacer


def flush_paragraph(lines: list[str], story: list[object], style: ParagraphStyle) -> None:
    if not lines:
        return
    text = " ".join(line.strip() for line in lines).strip()
    if text:
        story.append(Paragraph(html.escape(text), style))
        story.append(Spacer(1, 0.12 * inch))
    lines.clear()


def build_story(markdown: str) -> list[object]:
    styles = getSampleStyleSheet()
    body = styles["BodyText"]
    body.fontName = "Helvetica"
    body.fontSize = 10
    body.leading = 14

    bullet = ParagraphStyle(
        "SpecBullet",
        parent=body,
        leftIndent=16,
        firstLineIndent=-8,
        spaceAfter=6,
    )
    h1 = ParagraphStyle("SpecH1", parent=styles["Heading1"], fontName="Helvetica-Bold", fontSize=18, leading=22, spaceAfter=10)
    h2 = ParagraphStyle("SpecH2", parent=styles["Heading2"], fontName="Helvetica-Bold", fontSize=14, leading=18, spaceAfter=8)
    h3 = ParagraphStyle("SpecH3", parent=styles["Heading3"], fontName="Helvetica-Bold", fontSize=12, leading=16, spaceAfter=6)
    code = ParagraphStyle("SpecCode", parent=body, fontName="Courier", fontSize=8.5, leading=11)

    story: list[object] = []
    paragraph_lines: list[str] = []
    code_lines: list[str] = []
    in_code = False

    for raw in markdown.splitlines():
        line = raw.rstrip("\n")

        if line.startswith("```"):
            flush_paragraph(paragraph_lines, story, body)
            if in_code:
                story.append(Preformatted("\n".join(code_lines), code))
                story.append(Spacer(1, 0.12 * inch))
                code_lines.clear()
                in_code = False
            else:
                in_code = True
            continue

        if in_code:
            code_lines.append(line)
            continue

        if not line.strip():
            flush_paragraph(paragraph_lines, story, body)
            continue

        if line.startswith("# "):
            flush_paragraph(paragraph_lines, story, body)
            story.append(Paragraph(html.escape(line[2:].strip()), h1))
            continue
        if line.startswith("## "):
            flush_paragraph(paragraph_lines, story, body)
            story.append(Paragraph(html.escape(line[3:].strip()), h2))
            continue
        if line.startswith("### "):
            flush_paragraph(paragraph_lines, story, body)
            story.append(Paragraph(html.escape(line[4:].strip()), h3))
            continue

        if line.startswith("- "):
            flush_paragraph(paragraph_lines, story, body)
            story.append(Paragraph("&bull; " + html.escape(line[2:].strip()), bullet))
            continue

        paragraph_lines.append(line)

    flush_paragraph(paragraph_lines, story, body)
    if code_lines:
        story.append(Preformatted("\n".join(code_lines), code))

    return story


def main() -> int:
    if len(sys.argv) != 3:
        print("usage: render_markdown_pdf.py INPUT.md OUTPUT.pdf", file=sys.stderr)
        return 2

    src = Path(sys.argv[1])
    dst = Path(sys.argv[2])
    dst.parent.mkdir(parents=True, exist_ok=True)

    story = build_story(src.read_text(encoding="utf-8"))
    doc = SimpleDocTemplate(
        str(dst),
        pagesize=letter,
        leftMargin=0.8 * inch,
        rightMargin=0.8 * inch,
        topMargin=0.8 * inch,
        bottomMargin=0.8 * inch,
        title=src.stem,
    )
    doc.build(story)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
