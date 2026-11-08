"""Generate finance-shaped workbooks for the write-path validation.

The calamine corpus (corpus/excel) is saved by real Excel and covers odd
structures; these files cover what a finance model looks like: several
linked sheets, named inputs, formats, a table, a chart, Chinese text,
a data-vendor function whose value only Excel with the add-in can compute,
and one large sheet.

Usage: .venv/bin/python tools/make_corpus.py corpus/gen
"""

import sys
from pathlib import Path

import xlsxwriter

YEARS = list(range(2024, 2034))


def dcf(path: Path) -> None:
    wb = xlsxwriter.Workbook(str(path))
    bold = wb.add_format({"bold": True})
    pct = wb.add_format({"num_format": "0.0%"})
    money = wb.add_format({"num_format": "#,##0.00"})
    head = wb.add_format({"bold": True, "align": "center", "bg_color": "#DDEBF7"})
    inp = wb.add_format({"num_format": "0.0%", "font_color": "#0000FF"})

    a = wb.add_worksheet("假设")
    a.write("A1", "关键假设", bold)
    rows = [
        ("收入增速", 0.12, "growth"),
        ("毛利率", 0.42, "gross_margin"),
        ("费用率", 0.18, "opex_ratio"),
        ("税率", 0.25, "tax_rate"),
        ("WACC", 0.085, "wacc"),
        ("永续增长率", 0.025, "tg"),
    ]
    for i, (label, value, name) in enumerate(rows, start=2):
        a.write(f"A{i}", label)
        a.write_number(f"B{i}", value, inp)
        wb.define_name(name, f"='假设'!$B${i}")
    a.write("A9", "基期收入（百万）")
    a.write_number("B9", 1200.0, money)
    wb.define_name("base_revenue", "='假设'!$B$9")
    a.data_validation("B2:B7", {"validate": "decimal", "criteria": "between", "minimum": -1, "maximum": 1})
    a.write_comment("B6", "加权平均资本成本，来自 Wind")
    a.set_column("A:A", 18)

    p = wb.add_worksheet("利润表")
    p.merge_range("A1:K1", "利润表（百万元）", head)
    p.write("A2", "年度", bold)
    for j, y in enumerate(YEARS):
        p.write_number(1, j + 1, y, bold)
    labels = ["营业收入", "营业成本", "毛利", "期间费用", "EBIT", "所得税", "净利润"]
    for i, l in enumerate(labels):
        p.write(2 + i, 0, l)
    p.set_column("B:K", 12, money)
    for j in range(len(YEARS)):
        col = xlsxwriter.utility.xl_col_to_name(j + 1)
        prev = xlsxwriter.utility.xl_col_to_name(j)
        if j == 0:
            p.write_formula(f"{col}3", "=base_revenue*(1+growth)", money, 1200 * 1.12)
        else:
            p.write_formula(f"{col}3", f"={prev}3*(1+growth)", money)
        p.write_formula(f"{col}4", f"={col}3*(1-gross_margin)", money)
        p.write_formula(f"{col}5", f"={col}3-{col}4", money)
        p.write_formula(f"{col}6", f"={col}3*opex_ratio", money)
        p.write_formula(f"{col}7", f"={col}5-{col}6", money)
        p.write_formula(f"{col}8", f"=MAX(0,{col}7)*tax_rate", money)
        p.write_formula(f"{col}9", f"={col}7-{col}8", money)
    p.conditional_format("B9:K9", {"type": "cell", "criteria": "<", "value": 0, "format": wb.add_format({"font_color": "red"})})
    p.freeze_panes(2, 1)

    v = wb.add_worksheet("估值")
    v.write("A1", "现金流折现", bold)
    v.write_row("A2", ["年度", "自由现金流", "折现因子", "现值"], bold)
    for i in range(len(YEARS)):
        r = 3 + i
        src = xlsxwriter.utility.xl_col_to_name(i + 1)
        v.write_formula(f"A{r}", f"='利润表'!{src}2")
        v.write_formula(f"B{r}", f"='利润表'!{src}9*0.8", money)
        v.write_formula(f"C{r}", f"=1/(1+wacc)^(ROW()-2)", wb.add_format({"num_format": "0.0000"}))
        v.write_formula(f"D{r}", f"=B{r}*C{r}", money)
    last = 2 + len(YEARS)
    v.write(f"A{last + 2}", "终值现值")
    v.write_formula(f"D{last + 2}", f"=B{last}*(1+tg)/(wacc-tg)*C{last}", money)
    v.write(f"A{last + 3}", "企业价值")
    v.write_formula(f"D{last + 3}", f"=SUM(D3:D{last})+D{last + 2}", money)
    v.write(f"A{last + 4}", "收盘价（Wind）")
    # A data-vendor function: only Excel with the Wind add-in can compute it,
    # so the cached value must survive our writes.
    v.write_formula(f"D{last + 4}", '=WSS("600519.SH","close","tradeDate=20251231")', money, 1688.0)
    v.write(f"A{last + 5}", "XLOOKUP 示例")
    v.write_formula(f"D{last + 5}", f'=_xlfn.XLOOKUP(2030,A3:A{last},D3:D{last})', money)
    chart = wb.add_chart({"type": "column"})
    chart.add_series({"name": "现值", "categories": f"='估值'!$A$3:$A${last}", "values": f"='估值'!$D$3:$D${last}"})
    v.insert_chart("F2", chart)

    t = wb.add_worksheet("可比公司")
    data = [
        ["贵州茅台", "600519.SH", 28.5, 9.1, 0.31],
        ["五粮液", "000858.SZ", 18.2, 5.3, 0.25],
        ["泸州老窖", "000568.SZ", 16.9, 6.0, 0.28],
        ["山西汾酒", "600809.SH", 21.4, 7.7, 0.33],
    ]
    t.add_table(f"A1:F{len(data) + 1}", {
        "name": "Comps",
        "data": [r + [None] for r in data],
        "columns": [
            {"header": "公司"},
            {"header": "代码"},
            {"header": "PE"},
            {"header": "PB"},
            {"header": "ROE", "format": pct},
            {"header": "PE/ROE", "formula": "=[@PE]/[@ROE]"},
        ],
    })
    t.write_url("H1", "https://www.wind.com.cn", string="数据来源")
    wb.close()


def big(path: Path, rows: int = 100_000) -> None:
    wb = xlsxwriter.Workbook(str(path), {"constant_memory": True})
    s = wb.add_worksheet("明细")
    s.write_row(0, 0, ["日期", "代码", "名称", "收盘", "成交量", "成交额", "涨跌幅", "备注"])
    num = wb.add_format({"num_format": "0.00"})
    for r in range(1, rows + 1):
        s.write_number(r, 0, 45000 + r % 3000)
        s.write_string(r, 1, f"{600000 + r % 900:06d}.SH")
        s.write_string(r, 2, f"证券{r % 900}")
        s.write_number(r, 3, 10 + (r % 97) / 7, num)
        s.write_number(r, 4, r * 13 % 100000)
        s.write_formula(r, 5, f"=D{r + 1}*E{r + 1}", num, (10 + (r % 97) / 7) * (r * 13 % 100000))
        s.write_formula(r, 6, f"=IF(D{r}=0,0,D{r + 1}/D{r}-1)" if r > 1 else "=0", num)
        if r % 50 == 0:
            s.write_string(r, 7, "复核")
    wb.close()


if __name__ == "__main__":
    out = Path(sys.argv[1])
    out.mkdir(parents=True, exist_ok=True)
    dcf(out / "dcf_model.xlsx")
    big(out / "big_100k.xlsx")
    print("ok")
