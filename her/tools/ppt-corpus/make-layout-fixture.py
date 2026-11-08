"""Rebuild the complete Office-compatible base deck (requires python-pptx)."""
from pathlib import Path
from io import BytesIO
from zipfile import ZipFile, ZipInfo, ZIP_DEFLATED
from pptx import Presentation
from pptx.util import Inches, Pt
from pptx.dml.color import RGBColor
from pptx.enum.shapes import MSO_SHAPE
from pptx.enum.text import MSO_AUTO_SIZE, MSO_ANCHOR

presentation = Presentation()
presentation.slide_width = Inches(13 + 1/3)
presentation.slide_height = Inches(7.5)
for title in ['Quarterly review', 'Next steps']:
    slide = presentation.slides.add_slide(presentation.slide_layouts[6])
    slide.background.fill.solid()
    slide.background.fill.fore_color.rgb = RGBColor.from_string('F4F7FB')
    shape = slide.shapes.add_textbox(Inches(1), Inches(1), Inches(11), Inches(1))
    shape.name = 'Title'
    shape.text_frame.auto_size = MSO_AUTO_SIZE.NONE
    shape.text_frame.word_wrap = True
    shape.text_frame.vertical_anchor = MSO_ANCHOR.TOP
    run = shape.text_frame.paragraphs[0].add_run()
    run.text = title
    run.font.size = Pt(36)
    run.font.bold = True
    run.font.name = 'Arial'
    run.font.color.rgb = RGBColor.from_string('163A66')
    card = slide.shapes.add_shape(MSO_SHAPE.ROUNDED_RECTANGLE, Inches(1), Inches(3), Inches(4), Inches(2))
    card.name = 'Blue card'
    card.fill.solid()
    card.fill.fore_color.rgb = RGBColor.from_string('2563EB')
    card.line.fill.background()
stream = BytesIO()
presentation.save(stream)
target = Path(__file__).resolve().parents[2] / 'crates/her-slide/tests/fixtures/text-layout.pptx'
with ZipFile(BytesIO(stream.getvalue())) as source, ZipFile(target, 'w') as output:
    for name in sorted(source.namelist()):
        info=ZipInfo(name, (2026,1,1,0,0,0)); info.compress_type=ZIP_DEFLATED
        output.writestr(info, source.read(name))
