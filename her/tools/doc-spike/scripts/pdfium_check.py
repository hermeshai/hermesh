"""PDFium side of the phase-0 checks (EQ-10): the same V1/V2/V5/V6/V7 cases as src/main.rs.

Runs on pypdfium2 (raw FPDF_* API).
  annotate <in> <out> <inc|full> [password]   every markup type on page 1, CJK text box as FreeText and as Stamp
  fill <in> <out> <inc|full>                  fill the AcroForm through the form-fill environment
  sign <in> <out> <p12> <password>            a reader-recognisable signature (incremental, AcroForm /Sig field)
"""
import ctypes
import hashlib
import sys

import pypdfium2 as pdfium
import pypdfium2.raw as r

CJK_FONT = "/System/Library/Fonts/Supplemental/Arial Unicode.ttf"


def ws(s):
    return ctypes.create_unicode_buffer(s) if False else (s + "\0").encode("utf-16-le")


def wbuf(s):
    b = ws(s)
    return ctypes.cast(ctypes.create_string_buffer(b, len(b)), ctypes.POINTER(r.FPDF_WCHAR))


def save(pdf, out, incremental):
    pdf.save(out, flags=r.FPDF_INCREMENTAL if incremental else r.FPDF_NO_INCREMENTAL)


def rect(a, l, b, rt, t):
    rc = r.FS_RECTF(l, t, rt, b)
    assert r.FPDFAnnot_SetRect(a, rc)


def quad(a, l, b, rt, t):
    q = r.FS_QUADPOINTSF(l, t, rt, t, l, b, rt, b)
    assert r.FPDFAnnot_AppendAttachmentPoints(a, q)


def annotate(src, out, mode, password=None):
    pdf = pdfium.PdfDocument(src, password=password)
    page = pdf[0]
    raw = page.raw
    textpage = page.get_textpage()
    # Markup over the first three text lines that have content.
    lines = []
    for i in range(textpage.count_rects()):
        l, b, rt, t = textpage.get_rect(i)
        if rt - l > 30:
            lines.append((l, b, rt, t))
        if len(lines) == 3:
            break
    for (l, b, rt, t), sub in zip(lines, [r.FPDF_ANNOT_HIGHLIGHT, r.FPDF_ANNOT_UNDERLINE, r.FPDF_ANNOT_STRIKEOUT]):
        a = r.FPDFPage_CreateAnnot(raw, sub)
        r.FPDFAnnot_SetColor(a, r.FPDFANNOT_COLORTYPE_Color, 255, 215, 0, 255)
        quad(a, l, b, rt, t)
        rect(a, l, b, rt, t)
        r.FPDFPage_CloseAnnot(a)

    # Sticky note with CJK contents.
    a = r.FPDFPage_CreateAnnot(raw, r.FPDF_ANNOT_TEXT)
    rect(a, 40, 40, 60, 60)
    r.FPDFAnnot_SetStringValue(a, b"Contents", wbuf("便签：违约金条款需要复核 (note)"))
    r.FPDFPage_CloseAnnot(a)

    # FreeText the plain way: contents + DA only (does PDFium write an appearance?).
    for y, text in [(60, "Latin text box 123"), (110, "中文文本框：营业收入同比增长 12.5%")]:
        a = r.FPDFPage_CreateAnnot(raw, r.FPDF_ANNOT_FREETEXT)
        rect(a, 300, y, 520, y + 40)
        r.FPDFAnnot_SetStringValue(a, b"Contents", wbuf(text))
        r.FPDFAnnot_SetStringValue(a, b"DA", wbuf("/Helv 12 Tf 0 0 0 rg"))
        r.FPDFPage_CloseAnnot(a)

    # CJK text box as a Stamp whose appearance is a text object in an embedded CJK font.
    font_bytes = open(CJK_FONT, "rb").read()
    buf = (ctypes.c_uint8 * len(font_bytes)).from_buffer_copy(font_bytes)
    font = r.FPDFText_LoadFont(pdf.raw, buf, len(font_bytes), r.FPDF_FONT_TRUETYPE, 1)
    assert font, "FPDFText_LoadFont failed"
    a = r.FPDFPage_CreateAnnot(raw, r.FPDF_ANNOT_STAMP)
    rect(a, 300, 160, 520, 200)
    obj = r.FPDFPageObj_CreateTextObj(pdf.raw, font, 12.0)
    r.FPDFText_SetText(obj, wbuf("图章文本框：净利润同比下降 3.2%"))
    r.FPDFPageObj_Transform(obj, 1, 0, 0, 1, 305, 175)
    assert r.FPDFAnnot_AppendObject(a, obj)
    r.FPDFPage_CloseAnnot(a)

    # Ink and shapes.
    a = r.FPDFPage_CreateAnnot(raw, r.FPDF_ANNOT_INK)
    pts = (r.FS_POINTF * 4)(r.FS_POINTF(60, 200), r.FS_POINTF(90, 230), r.FS_POINTF(120, 200), r.FS_POINTF(150, 230))
    r.FPDFAnnot_AddInkStroke(a, pts, 4)
    r.FPDFAnnot_SetColor(a, r.FPDFANNOT_COLORTYPE_Color, 230, 30, 30, 255)
    rect(a, 55, 195, 155, 235)
    r.FPDFPage_CloseAnnot(a)
    for sub, x in [(r.FPDF_ANNOT_SQUARE, 60), (r.FPDF_ANNOT_CIRCLE, 160)]:
        a = r.FPDFPage_CreateAnnot(raw, sub)
        r.FPDFAnnot_SetColor(a, r.FPDFANNOT_COLORTYPE_Color, 0, 0, 0, 255)
        rect(a, x, 260, x + 80, 300)
        r.FPDFPage_CloseAnnot(a)

    textpage.close()
    r.FPDFPage_GenerateContent(raw)
    save(pdf, out, mode == "inc")
    pdf.close()
    report(src, out)


def fill(src, out, mode):
    pdf = pdfium.PdfDocument(src)
    pdf.init_forms()
    page = pdf[0]
    form = pdf.formenv.raw
    values = {"company": "Acme Holdings Ltd.", "signer": "王小明（财务总监）", "date": "2026-11-02"}
    for i in range(r.FPDFPage_GetAnnotCount(page.raw)):
        a = r.FPDFPage_GetAnnot(page.raw, i)
        n = r.FPDFAnnot_GetFormFieldName(form, a, None, 0)
        nb = ctypes.create_string_buffer(n)
        r.FPDFAnnot_GetFormFieldName(form, a, ctypes.cast(nb, ctypes.POINTER(r.FPDF_WCHAR)), n)
        name = nb.raw[: n - 2].decode("utf-16-le")
        if name in values:
            assert r.FORM_SetFocusedAnnot(form, a)
            r.FORM_SelectAllText(form, page.raw)
            r.FORM_ReplaceSelection(form, page.raw, wbuf(values[name]))
            r.FORM_ForceToKillFocus(form)
        elif name == "agree":
            l, b, rt, t = 150, 622, 168, 640
            r.FORM_OnLButtonDown(form, page.raw, 0, (l + rt) / 2, (b + t) / 2)
            r.FORM_OnLButtonUp(form, page.raw, 0, (l + rt) / 2, (b + t) / 2)
        r.FPDFPage_CloseAnnot(a)
    save(pdf, out, mode == "inc")
    pdf.close()
    report(src, out)


def report(src, out):
    o, n = open(src, "rb").read(), open(out, "rb").read()
    print({"out": out, "bytes": len(n), "prefix_preserved": n[: len(o)] == o, "AP": n.count(b"/AP"),
           "Encrypt": b"/Encrypt" in n[len(o):] or b"/Encrypt" in n})


def sign(src, out, p12, password):
    """Appends a signature field and a detached PKCS#7 signature as an incremental update (by hand)."""
    from cryptography.hazmat.primitives import hashes, serialization
    from cryptography.hazmat.primitives.serialization import pkcs7, pkcs12

    key, cert, extra = pkcs12.load_key_and_certificates(open(p12, "rb").read(), password.encode())
    base = open(src, "rb").read()
    # Find the catalog and the first page, and the next free object number, via pypdf (read only).
    import pypdf
    rd = pypdf.PdfReader(src)
    root_ref = rd.trailer.raw_get("/Root")
    page_ref = rd.pages[0].indirect_reference
    size = rd.trailer["/Size"]
    cat = rd.trailer["/Root"]
    sig_n, field_n = size, size + 1
    # Rewrite the catalog with /AcroForm and the page with the widget in /Annots.
    cat_items = "".join(f"/{k[1:]} {v.indirect_reference.idnum} 0 R " if hasattr(v, "indirect_reference") and v.indirect_reference else "" for k, v in [])
    page = rd.pages[0]
    annots = [a.idnum for a in page.get("/Annots", [])] if page.get("/Annots") else []
    placeholder = 16384
    objs = {}
    objs[root_ref.idnum] = f"<< /Type /Catalog /Pages {cat.raw_get('/Pages').idnum} 0 R /AcroForm << /Fields [{field_n} 0 R] /SigFlags 3 >> >>".encode()
    pg = page.get_object()
    res = pg.raw_get("/Resources")
    res_s = f"{res.idnum} 0 R" if hasattr(res, "idnum") else None
    contents = pg.raw_get("/Contents")
    mb = " ".join(str(float(x)) for x in pg["/MediaBox"])
    page_dict = f"<< /Type /Page /Parent {pg.raw_get('/Parent').idnum} 0 R /MediaBox [{mb}] /Contents {contents.idnum} 0 R"
    if res_s:
        page_dict += f" /Resources {res_s}"
    else:
        raise SystemExit("inline /Resources not handled")
    page_dict += " /Annots [" + " ".join(f"{n} 0 R" for n in annots + [field_n]) + "] >>"
    objs[page_ref.idnum] = page_dict.encode()
    objs[field_n] = f"<< /Type /Annot /Subtype /Widget /FT /Sig /T (Signature1) /Rect [0 0 0 0] /F 132 /P {page_ref.idnum} 0 R /V {sig_n} 0 R >>".encode()
    sig_dict = (b"<< /Type /Sig /Filter /Adobe.PPKLite /SubFilter /adbe.pkcs7.detached /M (D:20261102120000Z) "
                b"/ByteRange [0 0000000000 0000000000 0000000000] /Contents <" + b"0" * (placeholder * 2) + b"> >>")
    objs[sig_n] = sig_dict
    out_b = bytearray(base)
    if not out_b.endswith(b"\n"):
        out_b += b"\n"
    offs = {}
    for n in sorted(objs):
        offs[n] = len(out_b)
        out_b += f"{n} 0 obj\n".encode() + objs[n] + b"\nendobj\n"
    prev = rd.trailer and int(base[base.rfind(b"startxref") + 9:].split()[0])
    x = len(out_b)
    out_b += b"xref\n"
    for n in sorted(objs):
        out_b += f"{n} 1\n{offs[n]:010d} 00000 n \n".encode()
    out_b += f"trailer\n<< /Size {size + 2} /Root {root_ref.idnum} 0 R /Prev {prev} >>\nstartxref\n{x}\n%%EOF\n".encode()
    # ByteRange around the /Contents hex string.
    c0 = out_b.index(b"/Contents <" + b"0" * 32, offs[sig_n]) + len(b"/Contents ")
    c1 = c0 + placeholder * 2 + 2
    br = f"[0 {c0:010d} {c1:010d} {len(out_b) - c1:010d}]".encode()
    i = out_b.index(b"/ByteRange [0 0000000000", offs[sig_n])
    out_b[i + len(b"/ByteRange "): i + len(b"/ByteRange ") + len(br)] = br
    signed_data = bytes(out_b[:c0]) + bytes(out_b[c1:])
    cms = (pkcs7.PKCS7SignatureBuilder().set_data(signed_data).add_signer(cert, key, hashes.SHA256())
           .sign(serialization.Encoding.DER, [pkcs7.PKCS7Options.DetachedSignature, pkcs7.PKCS7Options.Binary]))
    hexsig = cms.hex().encode()
    assert len(hexsig) <= placeholder * 2
    out_b[c0 + 1: c0 + 1 + len(hexsig)] = hexsig
    open(out, "wb").write(out_b)
    print({"out": out, "signed_bytes": len(out_b), "sha256": hashlib.sha256(signed_data).hexdigest()[:16]})


if __name__ == "__main__":
    cmd, *args = sys.argv[1:]
    {"annotate": annotate, "fill": fill, "sign": sign}[cmd](*args)
