#!/usr/bin/env python3
"""Generate crates/her-slide/tests/fixtures/ribbon.pptx for the PPT ribbon e2e.

The deck uses python-pptx's default master (eleven layouts and an Office theme), so layout
insertion/switching and theme-derived quick styles have real data. All text is Arial so the
isolated e2e font service can serve every face.

    /tmp/pptx-venv/bin/python tools/ppt-corpus/gen/make_ribbon_fixture.py
"""
import os
from pptx import Presentation
from pptx.dml.color import RGBColor
from pptx.enum.shapes import MSO_SHAPE
from pptx.util import Emu, Pt

HERE = os.path.dirname(os.path.abspath(__file__))
OUT = os.path.normpath(os.path.join(HERE, "..", "..", "..", "crates", "her-slide", "tests", "fixtures", "ribbon.pptx"))

prs = Presentation()
prs.slide_width, prs.slide_height = Emu(12192000), Emu(6858000)


def arial(frame, size=None, bold=None):
    for p in frame.paragraphs:
        for r in p.runs:
            r.font.name = "Arial"
            if size: r.font.size = Pt(size)
            if bold is not None: r.font.bold = bold


title_layout, content_layout = prs.slide_layouts[0], prs.slide_layouts[1]
s1 = prs.slides.add_slide(content_layout)
s1.shapes.title.text = "Ribbon check"
arial(s1.shapes.title.text_frame, 36, True)
body = s1.placeholders[1]
body.text = "Format me"
arial(body.text_frame, 20)
# Three cards at uneven positions for align / distribute; distinct names for the selection pane.
for i, (x, y, color) in enumerate([(914400, 3200400, "2563EB"), (4114800, 3657600, "16A34A"), (8686800, 3429000, "DC2626")]):
    card = s1.shapes.add_shape(MSO_SHAPE.ROUNDED_RECTANGLE, Emu(x), Emu(y), Emu(1828800), Emu(1371600))
    card.name = f"Card {i + 1}"
    card.fill.solid(); card.fill.fore_color.rgb = RGBColor.from_string(color)
    card.line.fill.background()
    card.text_frame.text = f"Card {i + 1}"
    arial(card.text_frame, 16)

s2 = prs.slides.add_slide(title_layout)
s2.shapes.title.text = "Second slide"
arial(s2.shapes.title.text_frame, 40)
s2.placeholders[1].text = "Subtitle"
arial(s2.placeholders[1].text_frame, 20)

prs.save(OUT)
print(OUT, os.path.getsize(OUT))
