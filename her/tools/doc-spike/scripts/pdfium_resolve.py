"""doc/resolve on PDFium: stdin JSON [[x, y, w, h], ...] (PDF space), stdout the text inside each box."""
import json, sys
import pypdfium2 as pdfium
doc = pdfium.PdfDocument(sys.argv[1])
tp = doc[int(sys.argv[2])].get_textpage()
rects = json.load(sys.stdin)
print(json.dumps([tp.get_text_bounded(left=x, bottom=y, right=x + w, top=y + h) for x, y, w, h in rects], ensure_ascii=False))
