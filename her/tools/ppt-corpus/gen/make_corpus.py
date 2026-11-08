#!/usr/bin/env python3
"""Generate the PPTX feature-coverage corpus (cov_*.pptx) into tools/ppt-corpus/corpus/.

Run with a venv that has python-pptx, lxml, Pillow:
    /tmp/pptx-venv/bin/python tools/ppt-corpus/gen/make_corpus.py
"""
import copy, os, re, sys
from xml.sax.saxutils import escape
from lxml import etree
from PIL import Image, ImageDraw, ImageFont
from pptx import Presentation
from pptx.chart.data import CategoryChartData, XyChartData
from pptx.enum.chart import XL_CHART_TYPE, XL_LEGEND_POSITION, XL_LABEL_POSITION
from pptx.opc.constants import RELATIONSHIP_TYPE as RT
from pptx.util import Inches, Pt, Emu
from pptx.dml.color import RGBColor

HERE = os.path.dirname(os.path.abspath(__file__))
OUT = os.path.normpath(os.path.join(HERE, "..", "corpus"))
IMG = os.path.join(OUT, "_img")
os.makedirs(IMG, exist_ok=True)

NS = ('xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" '
      'xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" '
      'xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"')
E = lambda v: int(round(v * 914400))

# ---------- images ----------
def make_images():
    im = Image.new("RGBA", (400, 300), (0, 0, 0, 0))
    d = ImageDraw.Draw(im)
    for i in range(150, 0, -5):
        a = int(255 * (1 - i / 150))
        d.ellipse((200 - i, 150 - i, 200 + i, 150 + i), fill=(220, 40, 40, a))
    d.line((20, 20, 380, 280), fill=(0, 0, 200, 255), width=8)
    d.line((20, 280, 380, 20), fill=(0, 150, 0, 255), width=8)
    im.save(f"{IMG}/alpha.png")
    jp = Image.new("RGB", (600, 400))
    px = jp.load()
    for x in range(600):
        for y in range(400):
            px[x, y] = (int(255 * x / 600), int(255 * y / 400), 160)
    d = ImageDraw.Draw(jp)
    for k in range(0, 600, 100):
        d.line((k, 0, k, 400), fill=(255, 255, 255), width=2)
    for k in range(0, 400, 100):
        d.line((0, k, 600, k), fill=(255, 255, 255), width=2)
    try:
        f = ImageFont.load_default(size=60)
    except TypeError:
        f = ImageFont.load_default()
    d.text((200, 160), "JPEG", fill=(0, 0, 0), font=f)
    d.rectangle((0, 0, 599, 399), outline=(0, 0, 0), width=6)
    jp.save(f"{IMG}/photo.jpg", quality=92)
    t = Image.new("RGB", (64, 64), (255, 240, 200))
    d = ImageDraw.Draw(t)
    d.rectangle((0, 0, 31, 31), fill=(230, 90, 60))
    d.rectangle((32, 32, 63, 63), fill=(60, 120, 200))
    d.line((0, 63, 63, 0), fill=(0, 0, 0), width=2)
    t.save(f"{IMG}/tile.png")
make_images()

# ---------- xml helpers ----------
def frag(xml):
    return etree.fromstring(xml.replace("<p:", f"<p:", 1) if False else xml)

def sf(c): return f'<a:solidFill><a:srgbClr val="{c}"/></a:solidFill>'
def scf(c, mods=""): return f'<a:solidFill><a:schemeClr val="{c}">{mods}</a:schemeClr></a:solidFill>'
def lnx(w=1, c="000000", attrs="", inner="", fill=None):
    f = fill if fill is not None else sf(c)
    return f'<a:ln w="{int(w*12700)}" {attrs}>{f}{inner}</a:ln>'

def r(t, attrs="", inner=""):
    return f'<a:r><a:rPr lang="en-US" {attrs} dirty="0">{inner}</a:rPr><a:t>{escape(t)}</a:t></a:r>'
def rz(t, attrs="", inner=""):  # CJK-lang run
    return f'<a:r><a:rPr lang="zh-CN" altLang="en-US" {attrs} dirty="0">{inner}</a:rPr><a:t>{escape(t)}</a:t></a:r>'
def para(runs="", ppr="", ppr_inner="", end=""):
    if isinstance(runs, list): runs = "".join(runs)
    return f'<a:p><a:pPr {ppr}>{ppr_inner}</a:pPr>{runs}{end}</a:p>'
def P(text, sz=18, ppr="", ppr_inner="", attrs="", inner=""):
    return para(r(text, f'sz="{sz*100}" {attrs}', inner), ppr, ppr_inner)

NOFILL = "<a:noFill/>"
def shape_xml(x, y, w, h, prst="rect", av="", fill=NOFILL, ln="", fx="", paras=None, body="",
              body_inner="", rot=0, flipH=False, flipV=False, name="Shape", txbox=False,
              geom=None, id="{ID}", descr=""):
    xa = ""
    if rot: xa += f' rot="{int(rot*60000)}"'
    if flipH: xa += ' flipH="1"'
    if flipV: xa += ' flipV="1"'
    g = geom if geom else f'<a:prstGeom prst="{prst}"><a:avLst>{av}</a:avLst></a:prstGeom>'
    if paras is None: paras = '<a:p><a:endParaRPr lang="en-US"/></a:p>'
    if isinstance(paras, list): paras = "".join(paras)
    cnv = ' txBox="1"' if txbox else ""
    return (f'<p:sp {NS}><p:nvSpPr><p:cNvPr id="{id}" name="{name}" descr="{descr}"/><p:cNvSpPr{cnv}/><p:nvPr/></p:nvSpPr>'
            f'<p:spPr><a:xfrm{xa}><a:off x="{E(x)}" y="{E(y)}"/><a:ext cx="{E(w)}" cy="{E(h)}"/></a:xfrm>'
            f'{g}{fill}{ln}{fx}</p:spPr>'
            f'<p:txBody><a:bodyPr rtlCol="0" {body}>{body_inner}</a:bodyPr><a:lstStyle/>{paras}</p:txBody></p:sp>')

def cxn_xml(x, y, w, h, prst="line", ln="", rot=0, flipH=False, flipV=False, st=None, en=None, name="Connector"):
    xa = ""
    if rot: xa += f' rot="{int(rot*60000)}"'
    if flipH: xa += ' flipH="1"'
    if flipV: xa += ' flipV="1"'
    c = ""
    if st: c += f'<a:stCxn id="{st[0]}" idx="{st[1]}"/>'
    if en: c += f'<a:endCxn id="{en[0]}" idx="{en[1]}"/>'
    return (f'<p:cxnSp {NS}><p:nvCxnSpPr><p:cNvPr id="{{ID}}" name="{name}"/><p:cNvCxnSpPr>{c}</p:cNvCxnSpPr><p:nvPr/></p:nvCxnSpPr>'
            f'<p:spPr><a:xfrm{xa}><a:off x="{E(x)}" y="{E(y)}"/><a:ext cx="{E(w)}" cy="{E(h)}"/></a:xfrm>'
            f'<a:prstGeom prst="{prst}"><a:avLst/></a:prstGeom>{ln}</p:spPr></p:cxnSp>')

def grp_xml(x, y, w, h, chx, chy, chw, chh, children, rot=0, flipH=False, flipV=False, name="Group"):
    xa = ""
    if rot: xa += f' rot="{int(rot*60000)}"'
    if flipH: xa += ' flipH="1"'
    if flipV: xa += ' flipV="1"'
    # children are full <p:sp ...> strings; strip their own ns decl duplicates is not needed
    return (f'<p:grpSp {NS}><p:nvGrpSpPr><p:cNvPr id="{{ID}}" name="{name}"/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr>'
            f'<p:grpSpPr><a:xfrm{xa}><a:off x="{E(x)}" y="{E(y)}"/><a:ext cx="{E(w)}" cy="{E(h)}"/>'
            f'<a:chOff x="{E(chx)}" y="{E(chy)}"/><a:chExt cx="{E(chw)}" cy="{E(chh)}"/></a:xfrm></p:grpSpPr>'
            + "".join(children) + '</p:grpSp>')

def add(slide, xml):
    tree = slide.shapes._spTree
    ids = [int(i) for i in tree.xpath("//p:cNvPr/@id")] + [1]
    counter = [max(ids) + 1]
    def sub(_):
        v = counter[0]; counter[0] += 1; return f'id="{v}"'
    xml = re.sub(r'id="\{ID\}"', sub, xml)
    el = etree.fromstring(xml)
    tree.append(el)
    return el

def S(slide, *a, **k):
    return add(slide, shape_xml(*a, **k))

def label(slide, text, w=None):
    pw = w or (Emu(slide.part.package.presentation_part.presentation.slide_width).inches - 0.4)
    S(slide, 0.2, 0.08, pw, 0.4, paras=P(text, 12, attrs='', inner=sf("666666")), name="Label",
      txbox=True, body='wrap="square" anchor="ctr"')

def new_deck(w=13.333, h=7.5, std43=False):
    prs = Presentation()
    old_w = prs.slide_width
    prs.slide_width = Inches(w); prs.slide_height = Inches(h)
    sz = prs.part._element.find("{http://schemas.openxmlformats.org/presentationml/2006/main}sldSz")
    if "type" in sz.attrib: del sz.attrib["type"]
    if w != 10:  # rescale master / layouts horizontally so inherited placeholders fit
        fx, fy = Inches(w) / old_w, Inches(h) / 6858000
        for holder in [prs.slide_master] + list(prs.slide_layouts):
            for off in holder._element.iter("{http://schemas.openxmlformats.org/drawingml/2006/main}off"):
                off.set("x", str(int(int(off.get("x")) * fx))); off.set("y", str(int(int(off.get("y")) * fy)))
            for ext in holder._element.iter("{http://schemas.openxmlformats.org/drawingml/2006/main}ext"):
                if ext.get("cx") is not None:
                    ext.set("cx", str(int(int(ext.get("cx")) * fx))); ext.set("cy", str(int(int(ext.get("cy")) * fy)))
    return prs

def blank(prs, lab):
    s = prs.slides.add_slide(prs.slide_layouts[6])
    label(s, lab)
    return s

def save(prs, name):
    path = f"{OUT}/{name}.pptx"
    prs.save(path)
    return path

def box(slide, x, y, w, h, paras, body='wrap="square"', fill=NOFILL, ln=lnx(0.75, "999999"), **k):
    return S(slide, x, y, w, h, fill=fill, ln=ln, paras=paras, body=body, txbox=True, **k)

LOREM = "The quick brown fox jumps over the lazy dog. Pack my box with five dozen liquor jugs."

# ---------- decks ----------
def cov_text_basic():
    prs = new_deck()
    s = blank(prs, "text_basic 1: font sizes and families")
    ps = [P(f"Size {z}pt Arial", z, attrs='', inner=sf("000000") + '<a:latin typeface="Arial"/>') for z in (8, 12, 18, 24, 32)]
    box(s, 0.4, 0.7, 6, 4.2, ps)
    fams = ["Arial", "Times New Roman", "Courier New", "Georgia", "Verdana", "Helvetica", "Trebuchet MS", "Comic Sans MS"]
    box(s, 6.8, 0.7, 6, 5.5, [P(f"{f}: Hamburgefonstiv 123", 20, inner=f'<a:latin typeface="{f}"/>') for f in fams])
    s = blank(prs, "text_basic 2: bold/italic/underline/strike/colour/highlight/baseline/spacing")
    ps = [
        para([r("Bold ", 'sz="2400" b="1"'), r("Italic ", 'sz="2400" i="1"'), r("Both ", 'sz="2400" b="1" i="1"'), r("Plain", 'sz="2400"')]),
        para([r("Single ", 'sz="2400" u="sng"'), r("Double ", 'sz="2400" u="dbl"'), r("Wavy ", 'sz="2400" u="wavy"'), r("Heavy ", 'sz="2400" u="heavy"'), r("Dash", 'sz="2400" u="dash"')]),
        para([r("Strike ", 'sz="2400" strike="sngStrike"'), r("DoubleStrike ", 'sz="2400" strike="dblStrike"'), r("Caps ", 'sz="2400" cap="all"'), r("SmallCaps", 'sz="2400" cap="small"')]),
        para([r("Red ", 'sz="2400"', sf("FF0000")), r("Green ", 'sz="2400"', sf("00AA00")), r("Blue ", 'sz="2400"', sf("0000FF")),
              r("Accent2 ", 'sz="2400"', scf("accent2")), r("Alpha50", 'sz="2400"', '<a:solidFill><a:srgbClr val="000000"><a:alpha val="50000"/></a:srgbClr></a:solidFill>')]),
        para([r("Highlight yellow ", 'sz="2400"', '<a:highlight><a:srgbClr val="FFFF00"/></a:highlight>'),
              r("cyan ", 'sz="2400"', '<a:highlight><a:srgbClr val="00FFFF"/></a:highlight>'),
              r("pink", 'sz="2400"', sf("000000") + '<a:highlight><a:srgbClr val="FF99CC"/></a:highlight>')]),
        para([r("E=mc", 'sz="2400"'), r("2", 'sz="2400" baseline="30000"'), r("  H", 'sz="2400"'), r("2", 'sz="2400" baseline="-25000"'), r("O  x", 'sz="2400"'),
              r("n+1", 'sz="2400" baseline="30000" b="1"')]),
        para([r("Spacing normal  ", 'sz="2400"'), r("spc=+300  ", 'sz="2400" spc="300"'), r("spc=-100  ", 'sz="2400" spc="-100"'), r("spc=1000", 'sz="2400" spc="1000"')]),
        para([r("Outline-ish: kerning ", 'sz="2400" kern="1200"'), r("AVAWA To Te", 'sz="2400" kern="0"')]),
    ]
    box(s, 0.4, 0.7, 12.5, 6.2, ps)
    return prs

def cov_text_layout():
    prs = new_deck()
    s = blank(prs, "text_layout 1: alignment l / ctr / r / just / dist")
    for i, al in enumerate(["l", "ctr", "r", "just", "dist"]):
        x = 0.3 + (i % 3) * 4.3; y = 0.7 + (i // 3) * 3.2
        box(s, x, y, 4.0, 3.0, [para(r(f"[{al}] " + LOREM + " " + LOREM, 'sz="1600"'), f'algn="{al}"')])
    s = blank(prs, "text_layout 2: anchor t/ctr/b/just/dist + insets")
    for i, an in enumerate(["t", "ctr", "b", "just", "dist"]):
        box(s, 0.3 + i * 2.55, 0.7, 2.4, 3.2, [P(f"anchor={an}", 14), P("second line", 14)], body=f'wrap="square" anchor="{an}"')
    box(s, 0.3, 4.3, 4, 2.5, [P("insets 0", 16)], body='wrap="square" lIns="0" tIns="0" rIns="0" bIns="0"')
    box(s, 4.5, 4.3, 4, 2.5, [P("insets L=1in T=0.5in", 16)], body=f'wrap="square" lIns="{E(1)}" tIns="{E(0.5)}" rIns="0" bIns="0"')
    box(s, 8.7, 4.3, 4.2, 2.5, [P("insets big all-around 0.4in  " + LOREM, 16)], body=f'wrap="square" lIns="{E(.4)}" tIns="{E(.4)}" rIns="{E(.4)}" bIns="{E(.4)}"')
    s = blank(prs, "text_layout 3: line spacing pct/pts, space before/after")
    def ls(label_, lnspc, bef="", aft=""):
        return para(r(label_ + " " + LOREM, 'sz="1400"'), "", f'<a:lnSpc>{lnspc}</a:lnSpc>{bef}{aft}')
    box(s, 0.3, 0.7, 4.1, 5.5, [ls("80%", '<a:spcPct val="80000"/>'), ls("100%", '<a:spcPct val="100000"/>'), ls("150%", '<a:spcPct val="150000"/>')])
    box(s, 4.6, 0.7, 4.1, 5.5, [ls("exact 12pt", '<a:spcPts val="1200"/>'), ls("exact 24pt", '<a:spcPts val="2400"/>'), ls("exact 36pt", '<a:spcPts val="3600"/>')])
    box(s, 8.9, 0.7, 4.1, 5.5, [
        para(r("A: before 0 after 0", 'sz="1400"'), "", '<a:spcBef><a:spcPts val="0"/></a:spcBef><a:spcAft><a:spcPts val="0"/></a:spcAft>'),
        para(r("B: before 24pt", 'sz="1400"'), "", '<a:spcBef><a:spcPts val="2400"/></a:spcBef>'),
        para(r("C: after 24pt", 'sz="1400"'), "", '<a:spcAft><a:spcPts val="2400"/></a:spcAft>'),
        para(r("D: before 50% pct", 'sz="1400"'), "", '<a:spcBef><a:spcPct val="50000"/></a:spcBef>'),
        para(r("E: end", 'sz="1400"'))])
    s = blank(prs, "text_layout 4: wrap none, normAutofit fontScale, spAutoFit, noAutofit")
    box(s, 0.3, 0.7, 3.0, 1.0, [P("wrap=none: this text does not wrap and overflows the box on the right", 16)], body='wrap="none"')
    box(s, 0.3, 2.2, 3.0, 2.0, [P(LOREM + " " + LOREM + " " + LOREM, 20)], body='wrap="square"', body_inner='<a:normAutofit fontScale="70000" lnSpcReduction="20000"/>')
    box(s, 3.7, 2.2, 3.0, 2.0, [P(LOREM + " " + LOREM + " " + LOREM, 20)], body='wrap="square"', body_inner='<a:noAutofit/>')
    box(s, 7.1, 2.2, 3.0, 0.5, [P("spAutoFit: box grows to fit " + LOREM, 16)], body='wrap="square"', body_inner='<a:spAutoFit/>')
    box(s, 10.4, 2.2, 2.6, 2.0, [P("normAutofit no scale " + LOREM, 16)], body='wrap="square"', body_inner='<a:normAutofit/>')
    return prs

def cov_bullets():
    prs = new_deck()
    s = blank(prs, "bullets 1: bullet characters and fonts")
    def b(t, ch, font="Arial"):
        return para(r(t, 'sz="2000"'), 'marL="457200" indent="-342900"', f'<a:buFont typeface="{font}"/><a:buChar char="{ch}"/>')
    box(s, 0.4, 0.7, 6, 5, [b("Dot", "•"), b("Dash", "–"), b("Square (Wingdings)", "§", "Wingdings"), b("Arrow", "➢", "Arial"), b("Check (Wingdings)", "ü", "Wingdings"), b("Star", "★", "Arial"), b("Diamond", "◆", "Arial"), b("Plain 'o'", "o", "Courier New")])
    box(s, 6.8, 0.7, 6, 5, [para(r("No bullet paragraph", 'sz="2000"'), "", "<a:buNone/>"), b("then bullet", "•"), para(r("hanging indent with wrapping: " + LOREM, 'sz="2000"'), 'marL="457200" indent="-342900"', '<a:buFont typeface="Arial"/><a:buChar char="•"/>')])
    s = blank(prs, "bullets 2: numbered lists arabicPeriod / romanUcPeriod / alphaLcParenR / startAt")
    def n(t, typ, start=None):
        sa = f' startAt="{start}"' if start else ""
        return para(r(t, 'sz="2000"'), 'marL="571500" indent="-571500"', f'<a:buFont typeface="+mj-lt"/><a:buAutoNum type="{typ}"{sa}/>')
    box(s, 0.4, 0.7, 4, 5, [n("one", "arabicPeriod"), n("two", "arabicPeriod"), n("three", "arabicPeriod"), n("ten starts", "arabicPeriod", 10), n("eleven", "arabicPeriod", 10)])
    box(s, 4.7, 0.7, 4, 5, [n("Roman upper", "romanUcPeriod"), n("second", "romanUcPeriod"), n("third", "romanUcPeriod"), n("fourth", "romanUcPeriod"), n("fifth", "romanUcPeriod")])
    box(s, 9.0, 0.7, 4, 5, [n("alpha paren", "alphaLcParenR"), n("second", "alphaLcParenR"), n("third", "alphaLcParenR"), n("alpha upper dot", "alphaUcPeriod"), n("arabicParenR", "arabicParenR")])
    s = blank(prs, "bullets 3: multi-level indent, custom bullet colour and size")
    def lv(t, l, ch="•"):
        return para(r(t, 'sz="1800"'), f'marL="{342900 + l*457200}" lvl="{l}" indent="-342900"', f'<a:buFont typeface="Arial"/><a:buChar char="{ch}"/>')
    box(s, 0.4, 0.7, 6, 5.5, [lv("Level 0", 0), lv("Level 1", 1, "–"), lv("Level 2", 2, "»"), lv("Level 3", 3, "·"), lv("Level 1 again", 1, "–"), lv("Level 0 again", 0)])
    def cb(t, clr, pct):
        return para(r(t, 'sz="2000"'), 'marL="457200" indent="-457200"', f'<a:buClr><a:srgbClr val="{clr}"/></a:buClr><a:buSzPct val="{pct}"/><a:buFont typeface="Arial"/><a:buChar char="●"/>')
    box(s, 6.8, 0.7, 6, 5.5, [cb("red bullet 100%", "FF0000", 100000), cb("green bullet 50%", "00AA00", 50000), cb("blue bullet 150%", "0000FF", 150000), cb("orange bullet 200%", "FF8800", 200000),
        para(r("bullet size in points 30pt", 'sz="2000"'), 'marL="457200" indent="-457200"', '<a:buClr><a:srgbClr val="880088"/></a:buClr><a:buSzPts val="3000"/><a:buChar char="•"/>'),
        para([r("colored ", 'sz="2000"', sf("FF0000")), r("text, bullet follows first run (buClrTx)", 'sz="2000"')], 'marL="457200" indent="-457200"', '<a:buFont typeface="Arial"/><a:buChar char="•"/>')])
    return prs

def cov_cjk():
    prs = new_deck()
    s = blank(prs, "cjk 1: Chinese fonts (PingFang SC / Microsoft YaHei fallback), mixed CJK+Latin")
    def cz(t, ea, sz=24, latin="Arial"):
        return para(rz(t, f'sz="{sz*100}"', f'<a:latin typeface="{latin}"/><a:ea typeface="{ea}"/>'))
    box(s, 0.4, 0.7, 6.2, 5.8, [cz("你好，世界！PingFang SC", "PingFang SC"), cz("微软雅黑 字体回退测试", "微软雅黑"), cz("宋体 SimSun 回退", "宋体"), cz("黑体 SimHei 回退", "黑体"), cz("楷体 KaiTi 回退", "楷体"),
        cz("Heiti SC 黑体-简", "Heiti SC"), cz("不存在的字体 NoSuchFont", "NoSuchFont")])
    box(s, 6.9, 0.7, 6, 5.8, [cz("中英文混排 Mixed 123 abc 测试ABC。", "PingFang SC"), cz("Price ¥128.50 / $19.9 共计 100%", "PingFang SC"),
        para(rz("加粗", 'sz="2400" b="1"', '<a:ea typeface="PingFang SC"/>') + rz("斜体", 'sz="2400" i="1"', '<a:ea typeface="PingFang SC"/>') + rz("下划线", 'sz="2400" u="sng"', '<a:ea typeface="PingFang SC"/>') + rz("红色", 'sz="2400"', sf("FF0000") + '<a:ea typeface="PingFang SC"/>')),
        cz("日本語のテキスト、ひらがなカタカナ", "Hiragino Sans"), cz("한국어 텍스트 테스트", "Apple SD Gothic Neo")])
    s = blank(prs, "cjk 2: line breaking and punctuation (narrow boxes)")
    txt = "这是一段用于测试换行的中文文本，其中包含逗号、句号。还有“引号”和（括号）以及《书名号》！问号？结尾。"
    box(s, 0.4, 0.7, 2.6, 5.5, [para(rz(txt, 'sz="1800"', '<a:ea typeface="PingFang SC"/>'))])
    box(s, 3.3, 0.7, 2.6, 5.5, [para(rz(txt, 'sz="1800"', '<a:ea typeface="PingFang SC"/>'), 'eaLnBrk="0" latinLnBrk="0" hangingPunct="0"')])
    box(s, 6.2, 0.7, 2.6, 5.5, [para(rz(txt, 'sz="1800"', '<a:ea typeface="PingFang SC"/>'), 'algn="just"')])
    box(s, 9.1, 0.7, 3.9, 5.5, [para(rz("Latin words wrap with 中文 mixed inside a long sentence 的情况 supercalifragilisticexpialidocious 超长单词。", 'sz="1800"', '<a:ea typeface="PingFang SC"/>'))])
    s = blank(prs, "cjk 3: vertical text eaVert / vert270 / vert / wordArtVert")
    for i, v in enumerate(["eaVert", "vert", "vert270", "wordArtVert"]):
        box(s, 0.4 + i * 3.2, 0.7, 2.8, 5.8, [para(rz(f"垂直文字 {v}：春眠不觉晓，处处闻啼鸟。ABC 123", 'sz="2000"', '<a:ea typeface="PingFang SC"/>'))], body=f'vert="{v}" wrap="square"')
    return prs

def cov_shapes_preset():
    prs = new_deck()
    groups = [
        ("rect", "roundRect:adj=<a:gd name=\"adj\" fmla=\"val 40000\"/>", "roundRect", "ellipse", "triangle", "rtTriangle", "parallelogram", "trapezoid", "diamond", "pentagon", "hexagon", "octagon"),
        ("rightArrow", "leftArrow", "upDownArrow", "star5", "star8", "chevron", "homePlate", "cloud", "heart", "lightningBolt", "sun", "moon"),
        ("wedgeRectCallout", "wedgeRoundRectCallout", "wedgeEllipseCallout", "borderCallout1", "flowChartProcess", "flowChartDecision", "flowChartTerminator", "flowChartDocument", "flowChartMagneticDisk", "donut", "arc", "blockArc"),
        ("mathPlus", "cube", "can", "bevel", "frame", "smileyFace", "noSmoking", "foldedCorner", "wave", "leftBracket", "rightBrace", "plaque"),
    ]
    cols = ["4472C4", "ED7D31", "70AD47", "FFC000", "5B9BD5", "A5A5A5"]
    for gi, g in enumerate(groups):
        s = blank(prs, f"shapes_preset {gi+1}: preset geometries")
        for i, item in enumerate(g):
            av = ""
            if ":" in item:
                item, av = item.split(":", 1); av = av.replace("adj=", "")
            x = 0.4 + (i % 4) * 3.15; y = 0.7 + (i // 4) * 2.2
            S(s, x, y, 2.2, 1.5, prst=item, av=av, fill=sf(cols[i % 6]), ln=lnx(1.5, "333333"),
              paras=P(item, 11, ppr='algn="ctr"', inner=sf("000000")), body='anchor="ctr"', name=item)
            S(s, x, y + 1.55, 2.5, 0.3, paras=P(item + (" adj=40000" if av else ""), 10, inner=sf("444444")), txbox=True)
    return prs

def cov_fill_line():
    prs = new_deck()
    s = blank(prs, "fill_line 1: solid, alpha, linear/radial gradient, pattern, none")
    def t(n): return P(n, 11, ppr='algn="ctr"', inner=sf("000000"))
    items = [
        ("solid", sf("4472C4")),
        ("alpha 40%", '<a:solidFill><a:srgbClr val="FF0000"><a:alpha val="40000"/></a:srgbClr></a:solidFill>'),
        ("linear 0deg 2stop", '<a:gradFill rotWithShape="1"><a:gsLst><a:gs pos="0"><a:srgbClr val="FF0000"/></a:gs><a:gs pos="100000"><a:srgbClr val="0000FF"/></a:gs></a:gsLst><a:lin ang="0" scaled="0"/></a:gradFill>'),
        ("linear 90deg", '<a:gradFill><a:gsLst><a:gs pos="0"><a:srgbClr val="FFFF00"/></a:gs><a:gs pos="100000"><a:srgbClr val="00AA00"/></a:gs></a:gsLst><a:lin ang="5400000" scaled="0"/></a:gradFill>'),
        ("linear 45deg 3stop", '<a:gradFill><a:gsLst><a:gs pos="0"><a:srgbClr val="FF0000"/></a:gs><a:gs pos="50000"><a:srgbClr val="FFFFFF"/></a:gs><a:gs pos="100000"><a:srgbClr val="0000FF"/></a:gs></a:gsLst><a:lin ang="2700000" scaled="0"/></a:gradFill>'),
        ("linear alpha stops", '<a:gradFill><a:gsLst><a:gs pos="0"><a:srgbClr val="000000"><a:alpha val="100000"/></a:srgbClr></a:gs><a:gs pos="100000"><a:srgbClr val="000000"><a:alpha val="0"/></a:srgbClr></a:gs></a:gsLst><a:lin ang="0" scaled="0"/></a:gradFill>'),
        ("radial circle center", '<a:gradFill><a:gsLst><a:gs pos="0"><a:srgbClr val="FFFFFF"/></a:gs><a:gs pos="100000"><a:srgbClr val="C00000"/></a:gs></a:gsLst><a:path path="circle"><a:fillToRect l="50000" t="50000" r="50000" b="50000"/></a:path></a:gradFill>'),
        ("radial corner", '<a:gradFill><a:gsLst><a:gs pos="0"><a:srgbClr val="FFFF00"/></a:gs><a:gs pos="100000"><a:srgbClr val="0070C0"/></a:gs></a:gsLst><a:path path="circle"><a:fillToRect l="0" t="0" r="100000" b="100000"/></a:path></a:gradFill>'),
        ("path rect", '<a:gradFill><a:gsLst><a:gs pos="0"><a:srgbClr val="FFFFFF"/></a:gs><a:gs pos="100000"><a:srgbClr val="7030A0"/></a:gs></a:gsLst><a:path path="rect"><a:fillToRect l="50000" t="50000" r="50000" b="50000"/></a:path></a:gradFill>'),
        ("pattern pct50", '<a:pattFill prst="pct50"><a:fgClr><a:srgbClr val="000000"/></a:fgClr><a:bgClr><a:srgbClr val="FFFFFF"/></a:bgClr></a:pattFill>'),
        ("pattern dkUpDiag", '<a:pattFill prst="dkUpDiag"><a:fgClr><a:srgbClr val="C00000"/></a:fgClr><a:bgClr><a:srgbClr val="FFE699"/></a:bgClr></a:pattFill>'),
        ("pattern smCheck", '<a:pattFill prst="smCheck"><a:fgClr><a:srgbClr val="0070C0"/></a:fgClr><a:bgClr><a:srgbClr val="FFFFFF"/></a:bgClr></a:pattFill>'),
        ("pattern horz", '<a:pattFill prst="horz"><a:fgClr><a:srgbClr val="00AA00"/></a:fgClr><a:bgClr><a:srgbClr val="FFFFFF"/></a:bgClr></a:pattFill>'),
        ("pattern cross", '<a:pattFill prst="lgGrid"><a:fgClr><a:srgbClr val="000000"/></a:fgClr><a:bgClr><a:srgbClr val="FFFF99"/></a:bgClr></a:pattFill>'),
        ("noFill + line", NOFILL),
    ]
    for i, (n, f) in enumerate(items):
        x = 0.4 + (i % 5) * 2.55; y = 0.7 + (i // 5) * 2.1
        S(s, x, y, 2.3, 1.7, fill=f, ln=lnx(1.5, "333333"), paras=t(n), body='anchor="ctr"')
    s = blank(prs, "fill_line 2: line widths and dash styles")
    for i, w in enumerate([0.25, 0.5, 1, 2, 4, 8, 12, 20]):
        S(s, 0.5, 0.8 + i * 0.5, 4, 0.01, prst="line", ln=lnx(w, "000000"), name=f"w{w}")
        S(s, 4.7, 0.65 + i * 0.5, 1, 0.3, paras=P(f"{w}pt", 11), txbox=True)
    for i, d in enumerate(["solid", "dot", "dash", "lgDash", "dashDot", "lgDashDot", "lgDashDotDot", "sysDash", "sysDot", "sysDashDot", "sysDashDotDot"]):
        S(s, 6.5, 0.8 + i * 0.5, 4, 0.01, prst="line", ln=lnx(3, "C00000", inner=f'<a:prstDash val="{d}"/>'))
        S(s, 10.7, 0.65 + i * 0.5, 2.3, 0.3, paras=P(d, 11), txbox=True)
    s = blank(prs, "fill_line 3: arrowheads head/tail types and sizes")
    for i, ty in enumerate(["triangle", "stealth", "diamond", "oval", "arrow", "none"]):
        S(s, 0.6, 0.9 + i * 0.7, 4.5, 0.01, prst="line", ln=lnx(3, "1F4E79", inner=f'<a:headEnd type="{ty}" w="med" len="med"/><a:tailEnd type="{ty}" w="med" len="med"/>'))
        S(s, 5.3, 0.75 + i * 0.7, 1.5, 0.3, paras=P(ty, 11), txbox=True)
    for i, (w, l) in enumerate([("sm", "sm"), ("med", "med"), ("lg", "lg"), ("sm", "lg"), ("lg", "sm")]):
        S(s, 7.2, 0.9 + i * 0.7, 4.0, 0.01, prst="line", ln=lnx(3, "C00000", inner=f'<a:tailEnd type="triangle" w="{w}" len="{l}"/>'))
        S(s, 11.4, 0.75 + i * 0.7, 1.6, 0.3, paras=P(f"w={w} len={l}", 11), txbox=True)
    for i, w in enumerate([1, 3, 6]):
        S(s, 1 + i * 3.5, 5.3, 2.8, 0.01, prst="line", ln=lnx(w, "000000", inner='<a:headEnd type="triangle"/><a:tailEnd type="stealth"/>'))
        S(s, 1 + i * 3.5, 5.6, 2.8, 0.3, paras=P(f"{w}pt line ends scale", 11), txbox=True)
    S(s, 1, 6.2, 4, 0.8, prst="curvedConnector3", ln=lnx(2, "000000", inner='<a:tailEnd type="triangle"/>'))
    s = blank(prs, "fill_line 4: compound lines, caps, joins")
    for i, c in enumerate(["sng", "dbl", "thickThin", "thinThick", "tri"]):
        S(s, 0.6, 0.9 + i * 0.8, 4.5, 0.01, prst="line", ln=lnx(10, "000000", attrs=f'cmpd="{c}"'))
        S(s, 5.3, 0.75 + i * 0.8, 1.6, 0.3, paras=P(c, 11), txbox=True)
    for i, c in enumerate(["flat", "rnd", "sq"]):
        S(s, 7.5, 1.0 + i * 0.8, 3, 0.01, prst="line", ln=lnx(16, "1F4E79", attrs=f'cap="{c}"'))
        S(s, 10.8, 0.85 + i * 0.8, 2, 0.3, paras=P("cap " + c, 11), txbox=True)
    for i, j in enumerate(["round", "bevel", "miter"]):
        S(s, 0.8 + i * 3, 4.3, 2.0, 2.0, prst="triangle", fill=NOFILL, ln=lnx(14, "C00000", inner=f'<a:{j}' + (' lim="800000"/>' if j == "miter" else '/>')))
        S(s, 0.8 + i * 3, 6.4, 2.0, 0.3, paras=P("join " + j, 11), txbox=True)
    S(s, 10, 4.3, 2.5, 2.0, prst="rect", fill=NOFILL, ln=lnx(14, "00AA00", inner='<a:miter lim="800000"/>'))
    return prs

def cov_transform():
    prs = new_deck()
    s = blank(prs, "transform 1: rotation 0/15/45/90/180/270 and flips")
    for i, a in enumerate([0, 15, 45, 90, 180, 270]):
        S(s, 0.6 + i * 2.1, 0.9, 1.6, 0.9, prst="rightArrow", fill=sf("4472C4"), ln=lnx(1, "000000"), rot=a, paras=P(f"{a}°", 12, ppr='algn="ctr"', inner=sf("FFFFFF")), body='anchor="ctr"')
    for i, (fh, fv) in enumerate([(0, 0), (1, 0), (0, 1), (1, 1)]):
        S(s, 0.8 + i * 3.1, 3.2, 2.0, 1.6, prst="rtTriangle", fill=sf("ED7D31"), ln=lnx(1, "000000"), flipH=bool(fh), flipV=bool(fv),
          paras=P(f"flipH={fh} flipV={fv} text", 12, ppr='algn="ctr"', inner=sf("000000")), body='anchor="ctr"')
    S(s, 0.8, 5.4, 2.0, 1.4, prst="rightArrow", fill=sf("70AD47"), ln=lnx(1, "000000"), rot=30, flipH=True, paras=P("rot30+flipH", 12, ppr='algn="ctr"'), body='anchor="ctr"')
    S(s, 4.0, 5.4, 2.0, 1.4, prst="rect", fill=sf("FFC000"), ln=lnx(1, "000000"), rot=15, paras=P("rotated text in rotated rect", 12, ppr='algn="ctr"'), body='anchor="ctr"')
    S(s, 7.0, 5.4, 2.0, 1.4, prst="rect", fill=sf("FFC000"), ln=lnx(1, "000000"), flipV=True, paras=P("flipV rect text", 12, ppr='algn="ctr"'), body='anchor="ctr"')
    S(s, 10.0, 5.4, 2.0, 1.4, prst="rect", fill=sf("FFC000"), ln=lnx(1, "000000"), paras=P("bodyPr rot 90", 12, ppr='algn="ctr"'), body='anchor="ctr" rot="5400000"')
    s = blank(prs, "transform 2: groups with chOff/chExt scaling, nested, rotated")
    def kids(color):
        return [shape_xml(0, 0, 2, 1, fill=sf(color), ln=lnx(1, "000000"), paras=P("A", 14, ppr='algn="ctr"'), body='anchor="ctr"', name="a"),
                shape_xml(2, 1, 2, 1, prst="ellipse", fill=sf("FFFFFF"), ln=lnx(2, "C00000"), paras=P("B", 14, ppr='algn="ctr"'), body='anchor="ctr"', name="b")]
    add(s, grp_xml(0.5, 1.0, 4, 2, 0, 0, 4, 2, kids("4472C4")))
    S(s, 0.5, 3.0, 3, 0.3, paras=P("1:1 child space", 11), txbox=True)
    add(s, grp_xml(5.0, 1.0, 4, 2, 0, 0, 2, 1, [shape_xml(0, 0, 1, 0.5, fill=sf("70AD47"), ln=lnx(1, "000000"), paras=P("A", 14), name="a"), shape_xml(1, 0.5, 1, 0.5, prst="ellipse", fill=sf("FFFFFF"), ln=lnx(2, "C00000"), name="b")]))
    S(s, 5.0, 3.0, 4, 0.3, paras=P("2x scale (chExt half of ext)", 11), txbox=True)
    add(s, grp_xml(9.5, 1.0, 3.5, 2, 10, 20, 4, 2, [shape_xml(10, 20, 2, 1, fill=sf("ED7D31"), ln=lnx(1, "000000"), name="a"), shape_xml(12, 21, 2, 1, prst="triangle", fill=sf("FFC000"), ln=lnx(1, "000000"), name="b")]))
    S(s, 9.5, 3.0, 3.5, 0.3, paras=P("chOff far away, non-uniform scale", 11), txbox=True)
    inner = grp_xml(0.5, 0.5, 3, 1.5, 0, 0, 3, 1.5, [shape_xml(0, 0, 1.5, 1.5, prst="star5", fill=sf("FFC000"), ln=lnx(1, "000000"), name="s1"), shape_xml(1.5, 0.3, 1.5, 0.9, fill=sf("5B9BD5"), ln=lnx(1, "000000"), paras=P("inner", 12, ppr='algn="ctr"'), body='anchor="ctr"', name="s2")], name="inner")
    add(s, grp_xml(0.8, 4.0, 5, 2.5, 0, 0, 4, 2, [inner, shape_xml(0, 0, 4, 2, fill=NOFILL, ln=lnx(1, "999999", inner='<a:prstDash val="dash"/>'), name="frame")], name="outer"))
    S(s, 0.8, 6.6, 5, 0.3, paras=P("nested group inside scaled group", 11), txbox=True)
    add(s, grp_xml(7.0, 4.0, 3, 2, 0, 0, 3, 2, kids("A5A5A5")[:1] + [shape_xml(1, 0.5, 2, 1.5, prst="rightArrow", fill=sf("4472C4"), ln=lnx(1, "000000"), name="arrow")], rot=25))
    add(s, grp_xml(10.5, 4.0, 2.5, 2, 0, 0, 2.5, 2, [shape_xml(0, 0, 2.5, 2, prst="rtTriangle", fill=sf("70AD47"), ln=lnx(1, "000000"), paras=P("flipH group", 12, ppr='algn="ctr"'), body='anchor="ctr"', name="t")], flipH=True))
    return prs

def cov_effects():
    prs = new_deck()
    s = blank(prs, "effects 1: outer shadow variants, inner shadow")
    base = dict(fill=sf("4472C4"), ln=lnx(1, "1F3864"))
    def fx(n, x, y, effect, prst="roundRect", fill=None):
        S(s, x, y, 2.6, 1.5, prst=prst, fill=fill or sf("4472C4"), ln=lnx(1, "1F3864"), fx=f'<a:effectLst>{effect}</a:effectLst>',
          paras=P(n, 12, ppr='algn="ctr"', inner=sf("FFFFFF")), body='anchor="ctr"')
    fx("outerShdw blur4 dist4 dir45", 0.5, 0.9, '<a:outerShdw blurRad="50800" dist="50800" dir="2700000" algn="tl" rotWithShape="0"><a:srgbClr val="000000"><a:alpha val="50000"/></a:srgbClr></a:outerShdw>')
    fx("outerShdw blur 0 sharp", 3.6, 0.9, '<a:outerShdw blurRad="0" dist="76200" dir="2700000" algn="tl" rotWithShape="0"><a:srgbClr val="7F7F7F"/></a:outerShdw>')
    fx("outerShdw big blur centered", 6.7, 0.9, '<a:outerShdw blurRad="190500" dist="0" dir="0" algn="ctr" rotWithShape="0"><a:srgbClr val="FF0000"><a:alpha val="60000"/></a:srgbClr></a:outerShdw>')
    fx("outerShdw scaled sx=sy=110%", 9.8, 0.9, '<a:outerShdw blurRad="63500" dist="127000" dir="5400000" sx="110000" sy="110000" algn="t" rotWithShape="0"><a:srgbClr val="000000"><a:alpha val="40000"/></a:srgbClr></a:outerShdw>')
    fx("innerShdw", 0.5, 3.2, '<a:innerShdw blurRad="114300" dist="50800" dir="2700000"><a:srgbClr val="000000"><a:alpha val="70000"/></a:srgbClr></a:innerShdw>', fill=sf("A9D18E"))
    fx("innerShdw blur0", 3.6, 3.2, '<a:innerShdw blurRad="0" dist="76200" dir="13500000"><a:srgbClr val="000000"/></a:innerShdw>', fill=sf("FFD966"))
    fx("shadow on ellipse", 6.7, 3.2, '<a:outerShdw blurRad="76200" dist="76200" dir="2700000" algn="tl" rotWithShape="0"><a:srgbClr val="000000"><a:alpha val="50000"/></a:srgbClr></a:outerShdw>', prst="ellipse")
    fx("shadow on noFill+line", 9.8, 3.2, '<a:outerShdw blurRad="50800" dist="50800" dir="2700000"><a:srgbClr val="000000"><a:alpha val="60000"/></a:srgbClr></a:outerShdw>', fill=NOFILL)
    s = blank(prs, "effects 2: glow, soft edges, reflection, combined")
    fx = lambda n, x, y, e, prst="roundRect", fill=None: S(s, x, y, 2.6, 1.5, prst=prst, fill=fill or sf("ED7D31"), ln=lnx(1, "843C0C"), fx=f'<a:effectLst>{e}</a:effectLst>', paras=P(n, 12, ppr='algn="ctr"'), body='anchor="ctr"')
    fx("glow 10pt", 0.5, 0.9, '<a:glow rad="127000"><a:srgbClr val="FFC000"><a:alpha val="40000"/></a:srgbClr></a:glow>')
    fx("glow 20pt blue", 3.6, 0.9, '<a:glow rad="254000"><a:srgbClr val="0070C0"><a:alpha val="35000"/></a:srgbClr></a:glow>')
    fx("softEdge 10pt", 6.7, 0.9, '<a:softEdge rad="127000"/>', prst="rect")
    fx("softEdge 25pt ellipse", 9.8, 0.9, '<a:softEdge rad="317500"/>', prst="ellipse")
    fx("reflection", 0.5, 3.4, '<a:reflection blurRad="6350" stA="50000" endA="300" endPos="90000" dir="5400000" sy="-100000" algn="bl" rotWithShape="0"/>')
    fx("reflection dist+rot", 3.6, 3.4, '<a:reflection blurRad="12700" stA="40000" endA="0" endPos="60000" dist="63500" dir="5400000" sy="-60000" algn="bl" rotWithShape="0"/>', prst="ellipse")
    fx("shadow + glow", 6.7, 3.4, '<a:glow rad="101600"><a:srgbClr val="70AD47"><a:alpha val="40000"/></a:srgbClr></a:glow><a:outerShdw blurRad="50800" dist="63500" dir="2700000"><a:srgbClr val="000000"><a:alpha val="50000"/></a:srgbClr></a:outerShdw>')
    S(s, 9.8, 3.4, 3.0, 1.5, fill=NOFILL, paras=P("Text shadow", 28, inner=sf("C00000") + '<a:effectLst><a:outerShdw blurRad="38100" dist="38100" dir="2700000"><a:srgbClr val="000000"><a:alpha val="50000"/></a:srgbClr></a:outerShdw></a:effectLst>'), txbox=True, body='anchor="ctr"')
    return prs

def cov_custgeom():
    prs = new_deck()
    def cg(paths, w=100, h=100):
        return f'<a:custGeom><a:avLst/><a:gdLst/><a:ahLst/><a:cxnLst/><a:rect l="0" t="0" r="r" b="b"/><a:pathLst>{paths}</a:pathLst></a:custGeom>'
    def pt(x, y): return f'<a:pt x="{x}" y="{y}"/>'
    s = blank(prs, "custgeom 1: moveTo/lnTo/close, cubicBezTo, quadBezTo, arcTo")
    star = '<a:path w="100" h="100"><a:moveTo>%s</a:moveTo>%s<a:close/></a:path>' % (pt(50, 0), "".join(f"<a:lnTo>{pt(x, y)}</a:lnTo>" for x, y in [(61, 35), (98, 35), (68, 57), (79, 91), (50, 70), (21, 91), (32, 57), (2, 35), (39, 35)]))
    S(s, 0.5, 0.9, 2.8, 2.8, geom=cg(star), fill=sf("FFC000"), ln=lnx(2, "7F6000"), name="star")
    blob = f'<a:path w="100" h="100"><a:moveTo>{pt(20,50)}</a:moveTo><a:cubicBezTo>{pt(20,10)}{pt(60,0)}{pt(80,30)}</a:cubicBezTo><a:cubicBezTo>{pt(110,60)}{pt(90,100)}{pt(50,90)}</a:cubicBezTo><a:cubicBezTo>{pt(20,85)}{pt(5,70)}{pt(20,50)}</a:cubicBezTo><a:close/></a:path>'
    S(s, 3.8, 0.9, 2.8, 2.8, geom=cg(blob), fill=sf("5B9BD5"), ln=lnx(2, "1F4E79"), name="blob")
    quad = f'<a:path w="100" h="100"><a:moveTo>{pt(0,100)}</a:moveTo><a:quadBezTo>{pt(50,-20)}{pt(100,100)}</a:quadBezTo><a:close/></a:path>'
    S(s, 7.1, 0.9, 2.8, 2.8, geom=cg(quad), fill=sf("A9D18E"), ln=lnx(2, "375623"), name="quad")
    pie = f'<a:path w="100" h="100"><a:moveTo>{pt(50,50)}</a:moveTo><a:lnTo>{pt(100,50)}</a:lnTo><a:arcTo wR="50" hR="50" stAng="0" swAng="16200000"/><a:close/></a:path>'
    S(s, 10.3, 0.9, 2.6, 2.8, geom=cg(pie), fill=sf("ED7D31"), ln=lnx(2, "843C0C"), name="pie")
    arc_open = f'<a:path w="100" h="100" fill="none"><a:moveTo>{pt(0,50)}</a:moveTo><a:arcTo wR="50" hR="50" stAng="10800000" swAng="10800000"/></a:path>'
    S(s, 0.5, 4.3, 2.8, 2.2, geom=cg(arc_open), fill=sf("FF0000"), ln=lnx(4, "000000", attrs='cap="rnd"'), name="open arc")
    multi = (f'<a:path w="100" h="100"><a:moveTo>{pt(0,0)}</a:moveTo><a:lnTo>{pt(100,0)}</a:lnTo><a:lnTo>{pt(100,45)}</a:lnTo><a:lnTo>{pt(0,45)}</a:lnTo><a:close/></a:path>'
             f'<a:path w="100" h="100" fill="darken"><a:moveTo>{pt(0,55)}</a:moveTo><a:lnTo>{pt(100,55)}</a:lnTo><a:lnTo>{pt(100,100)}</a:lnTo><a:lnTo>{pt(0,100)}</a:lnTo><a:close/></a:path>')
    S(s, 3.8, 4.3, 2.8, 2.2, geom=cg(multi), fill=sf("70AD47"), ln=lnx(2, "000000"), name="two paths fill darken")
    nostroke = f'<a:path w="100" h="100" stroke="0"><a:moveTo>{pt(0,100)}</a:moveTo><a:lnTo>{pt(50,0)}</a:lnTo><a:lnTo>{pt(100,100)}</a:lnTo><a:close/></a:path>'
    S(s, 7.1, 4.3, 2.8, 2.2, geom=cg(nostroke), fill=sf("7030A0"), ln=lnx(4, "FF0000"), name="stroke=0")
    hole = (f'<a:path w="100" h="100"><a:moveTo>{pt(0,0)}</a:moveTo><a:lnTo>{pt(100,0)}</a:lnTo><a:lnTo>{pt(100,100)}</a:lnTo><a:lnTo>{pt(0,100)}</a:lnTo><a:close/>'
            f'<a:moveTo>{pt(25,25)}</a:moveTo><a:lnTo>{pt(25,75)}</a:lnTo><a:lnTo>{pt(75,75)}</a:lnTo><a:lnTo>{pt(75,25)}</a:lnTo><a:close/></a:path>')
    S(s, 10.3, 4.3, 2.6, 2.2, geom=cg(hole), fill=sf("00B0F0"), ln=lnx(2, "000000"), name="evenodd hole")
    s = blank(prs, "custgeom 2: freeform with text rect, non-square path coordinate space, flipped")
    wave = f'<a:path w="1000" h="400"><a:moveTo>{pt(0,200)}</a:moveTo><a:cubicBezTo>{pt(150,-100)}{pt(350,-100)}{pt(500,200)}</a:cubicBezTo><a:cubicBezTo>{pt(650,500)}{pt(850,500)}{pt(1000,200)}</a:cubicBezTo><a:lnTo>{pt(1000,400)}</a:lnTo><a:lnTo>{pt(0,400)}</a:lnTo><a:close/></a:path>'
    S(s, 0.5, 1.0, 6.0, 2.4, geom=cg(wave), fill=sf("4472C4"), ln=lnx(2, "1F3864"), name="wave", paras=P("text in custgeom", 14, ppr='algn="ctr"', inner=sf("FFFFFF")), body='anchor="ctr"')
    S(s, 7.0, 1.0, 6.0, 2.4, geom=cg(wave), fill=sf("ED7D31"), ln=lnx(2, "843C0C"), name="wave flip", flipH=True, flipV=True, rot=10)
    arrow = f'<a:path w="200" h="100"><a:moveTo>{pt(0,30)}</a:moveTo><a:lnTo>{pt(120,30)}</a:lnTo><a:lnTo>{pt(120,0)}</a:lnTo><a:lnTo>{pt(200,50)}</a:lnTo><a:lnTo>{pt(120,100)}</a:lnTo><a:lnTo>{pt(120,70)}</a:lnTo><a:lnTo>{pt(0,70)}</a:lnTo><a:close/></a:path>'
    S(s, 0.5, 4.2, 5.0, 2.0, geom=cg(arrow), fill=sf("70AD47"), ln=lnx(1.5, "375623"), name="arrow stretched")
    S(s, 6.2, 4.2, 2.0, 2.0, geom=cg(arrow), fill=sf("70AD47"), ln=lnx(1.5, "375623"), name="arrow squeezed")
    heart = f'<a:path w="100" h="90"><a:moveTo>{pt(50,90)}</a:moveTo><a:cubicBezTo>{pt(-30,40)}{pt(20,-20)}{pt(50,25)}</a:cubicBezTo><a:cubicBezTo>{pt(80,-20)}{pt(130,40)}{pt(50,90)}</a:cubicBezTo><a:close/></a:path>'
    S(s, 9.0, 4.0, 3.5, 2.8, geom=cg(heart), fill='<a:gradFill><a:gsLst><a:gs pos="0"><a:srgbClr val="FF9999"/></a:gs><a:gs pos="100000"><a:srgbClr val="C00000"/></a:gs></a:gsLst><a:lin ang="5400000"/></a:gradFill>', ln=lnx(2, "7F0000"), name="gradient heart")
    return prs

def cov_pictures():
    prs = new_deck()
    s = blank(prs, "pictures 1: PNG alpha on colour, JPEG, srcRect crop, border, rotation, alphaModFix")
    S(s, 0.3, 0.7, 12.7, 6.7, fill=sf("DDEEFF"), name="bg")
    s.shapes.add_picture(f"{IMG}/alpha.png", Inches(0.5), Inches(0.9), Inches(3.2), Inches(2.4))
    s.shapes.add_picture(f"{IMG}/photo.jpg", Inches(4.0), Inches(0.9), Inches(3.6), Inches(2.4))
    p = s.shapes.add_picture(f"{IMG}/photo.jpg", Inches(7.9), Inches(0.9), Inches(2.4), Inches(2.4))
    p.crop_left = p.crop_right = 0.2; p.crop_top = p.crop_bottom = 0.1
    p = s.shapes.add_picture(f"{IMG}/photo.jpg", Inches(10.6), Inches(0.9), Inches(2.4), Inches(2.4))
    p.crop_left = 0.5
    p = s.shapes.add_picture(f"{IMG}/photo.jpg", Inches(0.5), Inches(3.8), Inches(3.2), Inches(2.4))
    p.line.color.rgb = RGBColor(0xC0, 0, 0); p.line.width = Pt(6)
    p = s.shapes.add_picture(f"{IMG}/photo.jpg", Inches(4.2), Inches(3.8), Inches(3.2), Inches(2.4)); p.rotation = 20
    p = s.shapes.add_picture(f"{IMG}/photo.jpg", Inches(8.0), Inches(3.8), Inches(2.4), Inches(2.4))
    blip = p._element.xpath(".//a:blip")[0]
    etree.SubElement(blip, "{http://schemas.openxmlformats.org/drawingml/2006/main}alphaModFix").set("amt", "50000")
    p = s.shapes.add_picture(f"{IMG}/photo.jpg", Inches(10.7), Inches(3.8), Inches(2.3), Inches(2.4))
    p._element.spPr.find("{http://schemas.openxmlformats.org/drawingml/2006/main}prstGeom").set("prst", "ellipse")
    p = s.shapes.add_picture(f"{IMG}/photo.jpg", Inches(8.0), Inches(6.3), Inches(1.2), Inches(0.9)); p._element.xpath(".//a:xfrm")[0].set("flipH", "1")
    s = blank(prs, "pictures 2: blipFill in shapes - stretch, fillRect offsets, tile, srcRect, alpha")
    _, rid = s.part.get_or_add_image_part(f"{IMG}/photo.jpg")
    _, rid2 = s.part.get_or_add_image_part(f"{IMG}/tile.png")
    def bf(inner, rid=rid, blipin=""):
        return f'<a:blipFill dpi="0" rotWithShape="1"><a:blip r:embed="{rid}">{blipin}</a:blip><a:srcRect/>{inner}</a:blipFill>'
    def fl(n, x, y, fill, prst="rect", w=2.9, h=2.2):
        S(s, x, y, w, h, prst=prst, fill=fill, ln=lnx(1.5, "000000"), paras=P(n, 11, inner=sf("FFFFFF")), body='anchor="b"')
    fl("stretch", 0.4, 0.8, bf('<a:stretch><a:fillRect/></a:stretch>'))
    fl("stretch fillRect l/t/r/b 10%", 3.6, 0.8, bf('<a:stretch><a:fillRect l="10000" t="10000" r="10000" b="10000"/></a:stretch>'))
    fl("stretch in ellipse", 6.8, 0.8, bf('<a:stretch><a:fillRect/></a:stretch>'), prst="ellipse")
    fl("stretch in star5", 10.0, 0.8, bf('<a:stretch><a:fillRect/></a:stretch>'), prst="star5")
    fl("tile 100% (64px)", 0.4, 3.4, bf('<a:tile tx="0" ty="0" sx="100000" sy="100000" flip="none" algn="tl"/>', rid2))
    fl("tile 200% flip xy", 3.6, 3.4, bf('<a:tile tx="0" ty="0" sx="200000" sy="200000" flip="xy" algn="tl"/>', rid2))
    fl("tile offset tx ty algn ctr", 6.8, 3.4, bf(f'<a:tile tx="{E(0.3)}" ty="{E(0.3)}" sx="100000" sy="100000" flip="none" algn="ctr"/>', rid2))
    fl("stretch + alphaModFix 50%", 10.0, 3.4, bf('<a:stretch><a:fillRect/></a:stretch>', blipin='<a:alphaModFix amt="50000"/>'))
    S(s, 0.4, 6.0, 2.9, 1.2, fill=f'<a:blipFill><a:blip r:embed="{rid}"/><a:srcRect l="25000" t="25000" r="25000" b="25000"/><a:stretch><a:fillRect/></a:stretch></a:blipFill>', ln=lnx(1.5, "000000"), paras=P("srcRect 25% in fill", 11, inner=sf("FFFFFF")))
    return prs

def cov_tables():
    prs = new_deck()
    def mk(s, rows, cols, x, y, w, h, style=None, data=None, flags=None):
        gf = s.shapes.add_table(rows, cols, Inches(x), Inches(y), Inches(w), Inches(h))
        t = gf.table
        tblPr = t._tbl.tblPr
        if style:
            sid = tblPr.find("{http://schemas.openxmlformats.org/drawingml/2006/main}tableStyleId")
            if sid is None: sid = etree.SubElement(tblPr, "{http://schemas.openxmlformats.org/drawingml/2006/main}tableStyleId")
            sid.text = style
        for k, v in (flags or {}).items(): tblPr.set(k, v)
        for ri in range(rows):
            for ci in range(cols):
                t.cell(ri, ci).text = (data[ri][ci] if data else f"R{ri+1}C{ci+1}")
                for pp in t.cell(ri, ci).text_frame.paragraphs:
                    for rr in pp.runs: rr.font.size = Pt(14)
        return t
    s = blank(prs, "tables 1: built-in table styles (Medium2 Accent1, Light1, Themed1, No style, Medium4)")
    A = "{http://schemas.openxmlformats.org/drawingml/2006/main}"
    mk(s, 4, 3, 0.4, 0.8, 4.0, 2.4, "{5C22544A-7EE6-4342-B048-85BDC9FD1C3A}")
    mk(s, 4, 3, 4.7, 0.8, 4.0, 2.4, "{9D7B26C5-4107-4FEC-AEDC-1716B250A1EF}")
    mk(s, 4, 3, 9.0, 0.8, 4.0, 2.4, "{3C2FFA5D-87B4-456A-9821-1D502468CF0F}")
    mk(s, 4, 3, 0.4, 3.8, 4.0, 2.4, "{2D5ABB26-0587-4C30-8999-92F81FD0307C}")
    mk(s, 4, 3, 4.7, 3.8, 4.0, 2.4, "{0505E3EF-67EA-436B-97B2-0124C06EBD24}")
    mk(s, 4, 3, 9.0, 3.8, 4.0, 2.4, "{5C22544A-7EE6-4342-B048-85BDC9FD1C3A}", flags={"firstCol": "1", "bandRow": "0", "lastRow": "1"})
    s = blank(prs, "tables 2: merges gridSpan/rowSpan/hMerge/vMerge")
    t = mk(s, 5, 5, 0.6, 0.9, 10.0, 4.5, "{5C22544A-7EE6-4342-B048-85BDC9FD1C3A}")
    t.cell(0, 0).merge(t.cell(0, 2)); t.cell(0, 0).text = "gridSpan 3"
    t.cell(1, 0).merge(t.cell(3, 0)); t.cell(1, 0).text = "rowSpan 3"
    t.cell(1, 2).merge(t.cell(2, 4)); t.cell(1, 2).text = "2x3 block"
    t.cell(4, 0).merge(t.cell(4, 4)); t.cell(4, 0).text = "full-width merged footer row"
    s = blank(prs, "tables 3: cell borders, fills, margins, alignment/anchor")
    t = mk(s, 4, 3, 0.6, 0.9, 11.5, 5, "{2D5ABB26-0587-4C30-8999-92F81FD0307C}")
    def tcpr(c, inner="", **attrs):
        tc = c._tc
        old = tc.find(A + "tcPr")
        if old is not None: tc.remove(old)
        el = etree.fromstring(f'<a:tcPr xmlns:a="{A[1:-1]}" ' + " ".join(f'{k}="{v}"' for k, v in attrs.items()) + f'>{inner}</a:tcPr>')
        tc.append(el)
    b = lambda tag, w, c, d="solid": f'<a:{tag} w="{int(w*12700)}" cap="flat" cmpd="sng" algn="ctr"><a:solidFill><a:srgbClr val="{c}"/></a:solidFill><a:prstDash val="{d}"/></a:{tag}>'
    tcpr(t.cell(0, 0), b("lnL", 3, "FF0000") + b("lnR", 3, "00AA00") + b("lnT", 3, "0000FF") + b("lnB", 3, "000000") + sf("FFF2CC"))
    tcpr(t.cell(0, 1), b("lnL", 1, "000000") + b("lnR", 1, "000000") + b("lnT", 6, "7030A0") + b("lnB", 1, "000000", "dash") + sf("DDEBF7"))
    tcpr(t.cell(0, 2), '<a:lnL w="0"><a:noFill/></a:lnL><a:lnR w="0"><a:noFill/></a:lnR><a:lnT w="0"><a:noFill/></a:lnT><a:lnB w="19050"><a:solidFill><a:srgbClr val="000000"/></a:solidFill></a:lnB>' + '<a:gradFill><a:gsLst><a:gs pos="0"><a:srgbClr val="FFFFFF"/></a:gs><a:gs pos="100000"><a:srgbClr val="FFC000"/></a:gs></a:gsLst><a:lin ang="0"/></a:gradFill>')
    for ci, (al, an) in enumerate([("l", "t"), ("ctr", "ctr"), ("r", "b")]):
        c = t.cell(1, ci); c.text = f"algn={al} anchor={an} margins 0.3in"
        c.text_frame.paragraphs[0].alignment = {"l": 1, "ctr": 2, "r": 3}[al]
        tcpr(c, b("lnL", 1, "999999") + b("lnR", 1, "999999") + b("lnT", 1, "999999") + b("lnB", 1, "999999") + sf("E2EFDA"), marL=E(.3), marR=E(.3), marT=E(.3), marB=E(.3), anchor=an)
        for rr in c.text_frame.paragraphs[0].runs: rr.font.size = Pt(14)
    for ci in range(3):
        c = t.cell(2, ci); c.text = "margins 0"; tcpr(c, b("lnL", 1, "000000") + b("lnR", 1, "000000") + b("lnT", 1, "000000") + b("lnB", 1, "000000"), marL=0, marR=0, marT=0, marB=0)
    c = t.cell(3, 0); c.text = "vert270 text"; tcpr(c, sf("FCE4D6"), vert="vert270")
    c = t.cell(3, 1); c.text = "Long wrapping text inside a cell to test wrap " + LOREM; tcpr(c, sf("EDEDED"))
    c = t.cell(3, 2); c.text = "bold white on dark"; tcpr(c, sf("203864"))
    for rr in c.text_frame.paragraphs[0].runs: rr.font.bold = True; rr.font.color.rgb = RGBColor(255, 255, 255); rr.font.size = Pt(14)
    t.rows[3].height = Inches(1.6)
    return prs

def cov_charts():
    prs = new_deck()
    cats = ["Q1", "Q2", "Q3", "Q4"]
    def cd(n=2):
        d = CategoryChartData(); d.categories = cats
        d.add_series("North", (19.2, 21.4, 16.7, 25.0)); 
        if n > 1: d.add_series("South", (22.3, 28.6, 15.2, 18.3))
        return d
    def chart(s, ty, x, y, w, h, data, title=None, legend=True, labels=False):
        ch = s.shapes.add_chart(ty, Inches(x), Inches(y), Inches(w), Inches(h), data).chart
        if title: ch.has_title = True; ch.chart_title.text_frame.text = title
        else: ch.has_title = False
        ch.has_legend = legend
        if legend: ch.legend.position = XL_LEGEND_POSITION.BOTTOM; ch.legend.include_in_layout = False
        if labels:
            pl = ch.plots[0]; pl.has_data_labels = True; pl.data_labels.font.size = Pt(10)
        return ch
    s = blank(prs, "charts 1: bar clustered (horizontal) + column clustered with data labels")
    chart(s, XL_CHART_TYPE.BAR_CLUSTERED, 0.3, 0.7, 6.3, 6.5, cd(), "Bar clustered", True)
    chart(s, XL_CHART_TYPE.COLUMN_CLUSTERED, 6.8, 0.7, 6.3, 6.5, cd(), "Column clustered + labels", True, True)
    s = blank(prs, "charts 2: line with markers + pie with labels and legend")
    chart(s, XL_CHART_TYPE.LINE_MARKERS, 0.3, 0.7, 6.3, 6.5, cd(), "Line markers", True, True)
    d = CategoryChartData(); d.categories = ["Apples", "Pears", "Plums", "Figs"]; d.add_series("Share", (40, 25, 20, 15))
    ch = chart(s, XL_CHART_TYPE.PIE, 6.8, 0.7, 6.3, 6.5, d, "Pie", True)
    ch.plots[0].has_data_labels = True; dl = ch.plots[0].data_labels; dl.number_format = "0%"; dl.show_percentage = True; dl.show_value = False; dl.position = XL_LABEL_POSITION.OUTSIDE_END
    s = blank(prs, "charts 3: XY scatter + stacked area")
    xd = XyChartData()
    a = xd.add_series("Series A"); [a.add_data_point(x, y) for x, y in [(1, 2), (2, 4.5), (3, 3), (4, 6), (5, 5.5)]]
    b = xd.add_series("Series B"); [b.add_data_point(x, y) for x, y in [(1, 1), (2, 2), (3, 4), (4, 3.5), (5, 7)]]
    chart(s, XL_CHART_TYPE.XY_SCATTER, 0.3, 0.7, 6.3, 6.5, xd, "Scatter", True)
    chart(s, XL_CHART_TYPE.AREA_STACKED, 6.8, 0.7, 6.3, 6.5, cd(), "Stacked area", True)
    return prs

THEME_NS = "http://schemas.openxmlformats.org/drawingml/2006/main"
def customize_theme(prs):
    part = prs.slide_master.part.part_related_by(RT.THEME)
    root = etree.fromstring(part.blob)
    A = "{%s}" % THEME_NS
    cols = dict(dk1="1A1A2E", lt1="FFFFFF", dk2="16213E", lt2="F1EFE7", accent1="E94560", accent2="0F9D8A", accent3="F5A623", accent4="4A90D9", accent5="7B4FBF", accent6="2E8B57", hlink="0563C1", folHlink="954F72")
    cs = root.find(f".//{A}clrScheme"); cs.set("name", "CovColors")
    for ch in cs:
        tag = etree.QName(ch).localname
        for k in list(ch): ch.remove(k)
        etree.SubElement(ch, A + "srgbClr").set("val", cols[tag])
    fs = root.find(f".//{A}fontScheme"); fs.set("name", "CovFonts")
    fs.find(f"{A}majorFont/{A}latin").set("typeface", "Georgia")
    fs.find(f"{A}minorFont/{A}latin").set("typeface", "Verdana")
    part._blob = etree.tostring(root, xml_declaration=True, encoding="UTF-8", standalone=True)

def cov_background_theme():
    prs = new_deck()
    customize_theme(prs)
    m = prs.slide_master._element
    cSld = m.find("{http://schemas.openxmlformats.org/presentationml/2006/main}cSld")
    old = cSld.find("{http://schemas.openxmlformats.org/presentationml/2006/main}bg")
    if old is not None: cSld.remove(old)
    bg = etree.fromstring(f'<p:bg {NS}><p:bgPr><a:solidFill><a:schemeClr val="bg2"/></a:solidFill><a:effectLst/></p:bgPr></p:bg>')
    cSld.insert(0, bg)
    # decorative band on master (accent1)
    add_master = etree.fromstring(shape_xml(0, 0, 13.333, 0.12, fill=scf("accent1"), name="Master band").replace("{ID}", "99"))
    cSld.find("{http://schemas.openxmlformats.org/presentationml/2006/main}spTree").append(add_master)
    # slide 1: layout 1 (Title and Content) inherits master bg, placeholders incl. footer/date/slide number
    s = prs.slides.add_slide(prs.slide_layouts[1])
    s.shapes.title.text = "Title uses theme major font (Georgia)"
    s.placeholders[1].text_frame.text = "Body placeholder inherits minor font (Verdana) and master background (bg2)"
    s.placeholders[1].text_frame.add_paragraph().text = "Second bullet level from master"
    label(s, "background_theme 1: master bg inheritance + title/body/dt/ftr/sldNum placeholders + master band")
    tree = s.shapes._spTree
    for typ, idx, body_ in [("dt", 10, '<a:p><a:r><a:rPr lang="en-US"/><a:t>Date text</a:t></a:r></a:p>'),
                            ("ftr", 11, '<a:p><a:r><a:rPr lang="en-US"/><a:t>Footer text</a:t></a:r></a:p>'),
                            ("sldNum", 12, '<a:p><a:fld id="{B6F15528-21DE-4FAA-801E-634DDDAF4B2B}" type="slidenum"><a:rPr lang="en-US"/><a:t>‹#›</a:t></a:fld><a:endParaRPr lang="en-US"/></a:p>')]:
        add(s, f'<p:sp {NS}><p:nvSpPr><p:cNvPr id="{{ID}}" name="{typ}"/><p:cNvSpPr><a:spLocks noGrp="1"/></p:cNvSpPr><p:nvPr><p:ph type="{typ}" sz="quarter" idx="{idx}"/></p:nvPr></p:nvSpPr><p:spPr/><p:txBody><a:bodyPr/><a:lstStyle/>{body_}</p:txBody></p:sp>')
    # slide 2: solid bg override + schemeClr transforms
    s = blank(prs, "background_theme 2: slide bg solid (accent4 lumMod/lumOff) + schemeClr mods swatches")
    s._element.cSld.insert(0, etree.fromstring(f'<p:bg {NS}><p:bgPr><a:solidFill><a:schemeClr val="accent4"><a:lumMod val="20000"/><a:lumOff val="80000"/></a:schemeClr></a:solidFill><a:effectLst/></p:bgPr></p:bg>'))
    mods = [("accent1", ""), ("accent1 lumMod75", '<a:lumMod val="75000"/>'), ("accent1 lumMod50", '<a:lumMod val="50000"/>'), ("accent1 lumMod60 lumOff40", '<a:lumMod val="60000"/><a:lumOff val="40000"/>'),
            ("accent1 lumMod20 lumOff80", '<a:lumMod val="20000"/><a:lumOff val="80000"/>'), ("accent1 tint40", '<a:tint val="40000"/>'), ("accent1 shade50", '<a:shade val="50000"/>'),
            ("accent2 satMod150", '<a:satMod val="150000"/>'), ("accent2 alpha50", '<a:alpha val="50000"/>'), ("accent3 hueOff", '<a:hueOff val="3600000"/>'), ("tx1 lumMod65 lumOff35", None), ("bg2 lumMod90", None)]
    for i, (n, m_) in enumerate(mods):
        if n.startswith("tx1"): f = scf("tx1", '<a:lumMod val="65000"/><a:lumOff val="35000"/>')
        elif n.startswith("bg2"): f = scf("bg2", '<a:lumMod val="90000"/>')
        else: f = scf(n.split()[0], m_)
        S(s, 0.4 + (i % 4) * 3.15, 0.8 + (i // 4) * 2.1, 2.9, 1.8, fill=f, ln=lnx(1, "333333"), paras=P(n, 12, ppr='algn="ctr"', inner=scf("bg1") if "lumMod50" in n or "lumMod75" in n or n.startswith("tx1") else sf("000000")), body='anchor="ctr"')
    S(s, 0.4, 6.2, 12.4, 0.9, fill=NOFILL, paras=[para([r("+mj-lt major ", 'sz="2400"', '<a:latin typeface="+mj-lt"/>'), r("+mn-lt minor ", 'sz="2400"', '<a:latin typeface="+mn-lt"/>'), r("Explicit Arial", 'sz="2400"', '<a:latin typeface="Arial"/>')])], txbox=True)
    # slide 3: gradient bg
    s = blank(prs, "background_theme 3: slide bg gradient (theme colours)")
    s._element.cSld.insert(0, etree.fromstring(f'<p:bg {NS}><p:bgPr><a:gradFill rotWithShape="1"><a:gsLst><a:gs pos="0"><a:schemeClr val="accent1"/></a:gs><a:gs pos="100000"><a:schemeClr val="dk2"/></a:gs></a:gsLst><a:lin ang="5400000" scaled="0"/></a:gradFill><a:effectLst/></p:bgPr></p:bg>'))
    S(s, 1, 2, 8, 2, paras=P("White text on gradient bg", 36, inner=sf("FFFFFF")), txbox=True)
    # slide 4: picture bg
    s = blank(prs, "background_theme 4: slide bg picture (stretch)")
    _, rid = s.part.get_or_add_image_part(f"{IMG}/photo.jpg")
    s._element.cSld.insert(0, etree.fromstring(f'<p:bg {NS}><p:bgPr><a:blipFill dpi="0" rotWithShape="1"><a:blip r:embed="{rid}"/><a:srcRect/><a:stretch><a:fillRect/></a:stretch></a:blipFill><a:effectLst/></p:bgPr></p:bg>'))
    return prs

def cov_slide_size():
    prs = new_deck(10, 7.5)
    sz = prs.part._element.find("{http://schemas.openxmlformats.org/presentationml/2006/main}sldSz"); sz.set("type", "screen4x3")
    s = blank(prs, "slide_size 1: 4:3 (10x7.5in) - corner markers and centre cross")
    for (x, y, c) in [(0, 0.5, "FF0000"), (8.5, 0.5, "00AA00"), (0, 6.0, "0000FF"), (8.5, 6.0, "FFC000")]:
        S(s, x, y, 1.5, 1.5, fill=sf(c), ln=lnx(1, "000000"), paras=P("corner", 12, ppr='algn="ctr"'), body='anchor="ctr"')
    S(s, 4.0, 3.0, 2.0, 2.0, prst="ellipse", fill=sf("DDDDDD"), ln=lnx(2, "000000"), paras=P("center", 14, ppr='algn="ctr"'), body='anchor="ctr"')
    S(s, 0, 3.74, 10, 0.01, prst="line", ln=lnx(1, "999999")); S(s, 5, 0, 0.01, 7.5, prst="line", ln=lnx(1, "999999"))
    s = blank(prs, "slide_size 2: 4:3 text and a table-ish grid at full width")
    box(s, 0.5, 1.0, 9.0, 1.5, [P("Full-width text box on a 4:3 slide. " + LOREM, 20)])
    for i in range(8): S(s, 0.5 + i * 1.125, 3.0, 1.0, 3.0, fill=sf(["4472C4", "ED7D31"][i % 2]), ln=lnx(1, "000000"))
    return prs

def cov_slide_portrait():
    prs = new_deck(5.625, 10)
    s = blank(prs, "slide_portrait 1: 9:16 portrait (5.625x10in)")
    for (x, y, c) in [(0, 0.5, "FF0000"), (4.125, 0.5, "00AA00"), (0, 8.5, "0000FF"), (4.125, 8.5, "FFC000")]:
        S(s, x, y, 1.5, 1.5, fill=sf(c), ln=lnx(1, "000000"))
    box(s, 0.4, 2.5, 4.8, 2.5, [P("Portrait slide text wraps in a narrow column. " + LOREM, 18)])
    S(s, 1.3, 5.5, 3.0, 3.0, prst="ellipse", fill=sf("DDDDDD"), ln=lnx(2, "000000"), paras=P("center", 14, ppr='algn="ctr"'), body='anchor="ctr"')
    s = blank(prs, "slide_portrait 2: tall chart-like bars")
    for i in range(5): S(s, 0.4 + i * 0.95, 9.2 - (i + 1) * 1.5, 0.8, (i + 1) * 1.5, fill=sf("4472C4"), ln=lnx(1, "000000"))
    return prs

def cov_misc():
    prs = new_deck()
    s = blank(prs, "misc 1: hyperlinks (text run, shape click, internal slide jump)")
    tb = s.shapes.add_textbox(Inches(0.5), Inches(1.0), Inches(8), Inches(1.5))
    p = tb.text_frame.paragraphs[0]
    r1 = p.add_run(); r1.text = "External link run (should be underlined, hlink colour) "; r1.font.size = Pt(20)
    r1.hyperlink.address = "https://example.com/"
    r2 = p.add_run(); r2.text = " and plain text."; r2.font.size = Pt(20)
    sh = s.shapes.add_shape(1, Inches(0.5), Inches(3.0), Inches(3.5), Inches(1.2)); sh.text_frame.text = "Shape click -> URL"
    sh.click_action.hyperlink.address = "https://example.com/shape"
    s2 = blank(prs, "misc 2: connectors straight / bent / curved with arrows, glued to shapes")
    sh2 = s.shapes.add_shape(1, Inches(5), Inches(3.0), Inches(3.5), Inches(1.2)); sh2.text_frame.text = "Slide jump -> slide 2"
    sh2.click_action.target_slide = s2.part.slide if hasattr(s2.part, "slide") else s2
    # connectors on s2
    def A(label, x, y, c):
        el = S(s2, x, y, 1.6, 0.9, fill=sf(c), ln=lnx(1, "000000"), paras=P(label, 12, ppr='algn="ctr"'), body='anchor="ctr"')
        return int(el.xpath('./p:nvSpPr/p:cNvPr/@id', namespaces={'p': 'http://schemas.openxmlformats.org/presentationml/2006/main'})[0])
    a, b = A("A", 0.6, 1.0, "9DC3E6"), A("B", 4.0, 2.2, "A9D18E")
    arrow = lnx(2.5, "C00000", inner='<a:headEnd type="oval"/><a:tailEnd type="triangle"/>')
    add(s2, cxn_xml(2.2, 1.45, 1.8, 1.2, prst="bentConnector3", ln=arrow, st=(a, 3), en=(b, 1), name="bent 3"))
    a, b = A("C", 6.5, 1.0, "9DC3E6"), A("D", 10.0, 3.0, "A9D18E")
    add(s2, cxn_xml(8.1, 1.45, 1.9, 2.0, prst="curvedConnector3", ln=lnx(2.5, "1F4E79", inner='<a:tailEnd type="triangle"/>'), st=(a, 3), en=(b, 1), name="curved 3"))
    a, b = A("E", 0.6, 4.5, "FFD966"), A("F", 4.0, 5.8, "F4B183")
    add(s2, cxn_xml(2.2, 4.95, 1.8, 1.3, prst="straightConnector1", ln=lnx(2.5, "000000", inner='<a:headEnd type="diamond"/><a:tailEnd type="stealth" w="lg" len="lg"/>'), st=(a, 3), en=(b, 1), name="straight"))
    add(s2, cxn_xml(7.0, 5.0, 3.0, 1.6, prst="bentConnector3", ln=lnx(2, "7030A0", inner='<a:tailEnd type="arrow"/>'), flipV=True, name="bent flipV"))
    add(s2, cxn_xml(10.5, 5.0, 1.5, 1.5, prst="bentConnector2", ln=lnx(2, "7030A0", inner='<a:tailEnd type="triangle"/>'), name="bent2"))
    add(s2, cxn_xml(7.0, 6.9, 3.0, 0.01, prst="straightConnector1", ln=lnx(2, "00AA00", inner='<a:tailEnd type="triangle"/>'), name="horiz"))
    s3 = blank(prs, "misc 3: tab stops, slide-number field, text in rotated shapes")
    tabs = '<a:tabLst><a:tab pos="1371600" algn="l"/><a:tab pos="3657600" algn="ctr"/><a:tab pos="5486400" algn="r"/><a:tab pos="6400800" algn="dec"/></a:tabLst>'
    box(s3, 0.5, 0.8, 7.0, 2.5, [para(r("L\tcentre\tright\t12.50", 'sz="1800"'), "", tabs), para(r("Name\tAlpha\tBeta\t3.14159", 'sz="1800"'), "", tabs), para(r("x\ty\tz\t100.5", 'sz="1800"'), "", tabs)])
    box(s3, 8.0, 0.8, 5.0, 1.0, [para([r("Slide ", 'sz="2400"'), f'<a:fld id="{{B6F15528-21DE-4FAA-801E-634DDDAF4B2B}}" type="slidenum"><a:rPr lang="en-US" sz="2400" b="1"/><a:t>3</a:t></a:fld>', r(" of deck", 'sz="2400"')])])
    for i, a in enumerate([-30, 0, 30, 90]):
        S(s3, 0.8 + i * 3.1, 4.0, 2.4, 1.2, prst="roundRect", fill=sf("9DC3E6"), ln=lnx(1.5, "1F4E79"), rot=a, paras=P(f"rot {a} with text wrapping inside", 14, ppr='algn="ctr"'), body='anchor="ctr"')
    return prs

DECKS = [cov_text_basic, cov_text_layout, cov_bullets, cov_cjk, cov_shapes_preset, cov_fill_line, cov_transform, cov_effects,
         cov_custgeom, cov_pictures, cov_tables, cov_charts, cov_background_theme, cov_slide_size, cov_slide_portrait, cov_misc]

def validate_shape_ids(prs):
    for number, slide in enumerate(prs.slides, 1):
        ids = slide.shapes._spTree.xpath('.//p:cNvPr/@id')
        if len(ids) != len(set(ids)):
            raise ValueError(f'slide {number}: duplicate shape IDs')
        refs = slide.shapes._spTree.xpath('.//a:stCxn/@id | .//a:endCxn/@id')
        missing = set(refs) - set(ids)
        if missing:
            raise ValueError(f'slide {number}: connector references missing IDs {sorted(missing)}')

if __name__ == "__main__":
    only = set(sys.argv[1:])
    for fn in DECKS:
        if only and fn.__name__ not in only: continue
        prs = fn()
        validate_shape_ids(prs)
        p = save(prs, fn.__name__)
        chk = Presentation(p)
        print(f"{fn.__name__}: {len(chk.slides)} slides")
