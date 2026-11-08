"""A workbook of functions finance models use, each with an expected value
computed here from the function's Excel definition (None: coverage only).

Usage: .venv/bin/python -I tools/make_funcs.py corpus/gen/funcs.xlsx
"""

import math
import statistics
import sys
from datetime import date, timedelta

import xlsxwriter

CF = [-70000, 12000, 15000, 18000, 21000, 26000]
DATES = [date(2024, 1, 1), date(2024, 3, 1), date(2024, 10, 30), date(2025, 2, 15), date(2025, 4, 1), date(2025, 12, 31)]
Y = [3.2, 4.1, 5.0, 6.3, 6.9, 8.4]
X = [1, 2, 3, 4, 5, 6]


def serial(d):
    return (d - date(1899, 12, 30)).days


def npv(r, cfs):
    return sum(c / (1 + r) ** (i + 1) for i, c in enumerate(cfs))


def solve(f, lo=-0.99, hi=10.0):
    a, b = lo, hi
    for _ in range(300):
        m = (a + b) / 2
        if f(a) * f(m) <= 0:
            b = m
        else:
            a = m
    return (a + b) / 2


def xnpv(r, cfs, ds):
    return sum(c / (1 + r) ** ((d - ds[0]).days / 365) for c, d in zip(cfs, ds))


def pmt(r, n, pv, fv=0, typ=0):
    return -(pv * (1 + r) ** n + fv) * r / (((1 + r) ** n - 1) * (1 + r * typ))


def fv(r, n, p, pv=0, typ=0):
    return -(pv * (1 + r) ** n + p * (1 + r * typ) * ((1 + r) ** n - 1) / r)


def pv(r, n, p, fv_=0, typ=0):
    return -(fv_ + p * (1 + r * typ) * ((1 + r) ** n - 1) / r) / (1 + r) ** n


def ipmt(r, per, n, pv_):
    p = pmt(r, n, pv_)
    return -(pv_ * (1 + r) ** (per - 1) + p * ((1 + r) ** (per - 1) - 1) / r) * r


def edate(d, months):
    y, m = divmod(d.month - 1 + months, 12)
    y += d.year
    m += 1
    import calendar

    return date(y, m, min(d.day, calendar.monthrange(y, m)[1]))


def eomonth(d, months):
    import calendar

    e = edate(date(d.year, d.month, 1), months)
    return date(e.year, e.month, calendar.monthrange(e.year, e.month)[1])


def days360(a, b):
    d1, d2 = min(a.day, 30), b.day
    if d2 == 31 and d1 >= 30:
        d2 = 30
    return (b.year - a.year) * 360 + (b.month - a.month) * 30 + (d2 - d1)


def pct_inc(xs, p):
    s = sorted(xs)
    k = (len(s) - 1) * p
    f = math.floor(k)
    return s[f] + (s[min(f + 1, len(s) - 1)] - s[f]) * (k - f)


mean_x, mean_y = statistics.mean(X), statistics.mean(Y)
slope = sum((x - mean_x) * (y - mean_y) for x, y in zip(X, Y)) / sum((x - mean_x) ** 2 for x in X)
irr = solve(lambda r: CF[0] + npv(r, CF[1:]))
xirr = solve(lambda r: xnpv(r, CF, DATES))
fin_r = 0.10
reinv = 0.12
mirr = (
    (-sum(c * (1 + reinv) ** (len(CF) - 1 - i) for i, c in enumerate(CF) if c > 0) / sum(c / (1 + fin_r) ** i for i, c in enumerate(CF) if c < 0))
    ** (1 / (len(CF) - 1))
    - 1
)
rate = solve(lambda r: pv(r, 48, -200) - 8000, 1e-9, 1)
d0 = date(2024, 1, 31)

CASES = [
    # financial
    ("NPV", "NPV(0.1,Data!A2:A6)+Data!A1", CF[0] + npv(0.1, CF[1:])),
    ("XNPV", "XNPV(0.09,Data!A1:A6,Data!B1:B6)", xnpv(0.09, CF, DATES)),
    ("IRR", "IRR(Data!A1:A6)", irr),
    ("XIRR", "XIRR(Data!A1:A6,Data!B1:B6)", xirr),
    ("MIRR", "MIRR(Data!A1:A6,0.1,0.12)", mirr),
    ("PMT", "PMT(0.08/12,120,100000)", pmt(0.08 / 12, 120, 100000)),
    ("IPMT", "IPMT(0.08/12,3,120,100000)", ipmt(0.08 / 12, 3, 120, 100000)),
    ("PPMT", "PPMT(0.08/12,3,120,100000)", pmt(0.08 / 12, 120, 100000) - ipmt(0.08 / 12, 3, 120, 100000)),
    ("PV", "PV(0.05/12,240,-1500)", pv(0.05 / 12, 240, -1500)),
    ("FV", "FV(0.06/12,120,-200,-500,1)", fv(0.06 / 12, 120, -200, -500, 1)),
    ("RATE", "RATE(48,-200,8000)", rate),
    ("NPER", "NPER(0.01,-100,-1000,10000,1)", None),
    ("CUMIPMT", "CUMIPMT(0.09/12,360,125000,13,24,0)", None),
    ("CUMPRINC", "CUMPRINC(0.09/12,360,125000,13,24,0)", None),
    ("SLN", "SLN(30000,7500,10)", (30000 - 7500) / 10),
    ("DDB", "DDB(2400,300,10,1)", 2400 * 2 / 10),
    ("DB", "DB(1000000,100000,6,1,7)", None),
    ("EFFECT", "EFFECT(0.0525,4)", (1 + 0.0525 / 4) ** 4 - 1),
    ("NOMINAL", "NOMINAL(0.053543,4)", 4 * ((1 + 0.053543) ** 0.25 - 1)),
    ("PRICE", "PRICE(DATE(2024,2,15),DATE(2032,11,15),0.0575,0.065,100,2,0)", None),
    ("YIELD", "YIELD(DATE(2024,2,15),DATE(2032,11,15),0.0575,95.04287,100,2,0)", None),
    ("DURATION", "DURATION(DATE(2024,1,1),DATE(2032,1,1),0.08,0.09,2,1)", None),
    ("MDURATION", "MDURATION(DATE(2024,1,1),DATE(2032,1,1),0.08,0.09,2,1)", None),
    ("ACCRINT", "ACCRINT(DATE(2024,3,1),DATE(2024,8,31),DATE(2024,5,1),0.1,1000,2,0)", None),
    # dates
    ("EDATE", "EDATE(DATE(2024,1,31),1)", serial(edate(d0, 1))),
    ("EOMONTH", "EOMONTH(DATE(2024,1,31),13)", serial(eomonth(d0, 13))),
    ("YEARFRAC", "YEARFRAC(DATE(2024,1,1),DATE(2025,7,1),3)", (date(2025, 7, 1) - date(2024, 1, 1)).days / 365),
    ("DAYS360", "DAYS360(DATE(2024,1,31),DATE(2024,3,31))", days360(date(2024, 1, 31), date(2024, 3, 31))),
    ("NETWORKDAYS", "NETWORKDAYS(DATE(2024,1,1),DATE(2024,12,31))", sum(1 for i in range(366) if (date(2024, 1, 1) + timedelta(i)).weekday() < 5)),
    ("WORKDAY", "WORKDAY(DATE(2024,1,5),10)", None),
    ("DATEDIF", "DATEDIF(DATE(2020,5,17),DATE(2024,3,1),\"m\")", 45),
    ("WEEKDAY", "WEEKDAY(DATE(2024,3,1),2)", 5),
    # lookup
    ("VLOOKUP", "VLOOKUP(4,Data!D1:E6,2,FALSE)", Y[3]),
    ("HLOOKUP", "HLOOKUP(\"b\",Data!G1:I2,2,FALSE)", 20),
    ("INDEX/MATCH", "INDEX(Data!C1:C6,MATCH(5,Data!D1:D6,0))", Y[4]),
    ("XLOOKUP", "_xlfn.XLOOKUP(6,Data!D1:D6,Data!C1:C6)", Y[5]),
    ("XLOOKUP not found", "_xlfn.XLOOKUP(99,Data!D1:D6,Data!C1:C6,\"无\")", "无"),
    ("OFFSET", "SUM(OFFSET(Data!C1,1,0,3,1))", sum(Y[1:4])),
    ("INDIRECT", "INDIRECT(\"Data!C3\")", Y[2]),
    ("CHOOSE", "CHOOSE(2,10,20,30)", 20),
    ("LOOKUP", "LOOKUP(3.5,Data!D1:D6,Data!C1:C6)", Y[2]),
    # math & stats
    ("SUMIFS", "SUMIFS(Data!C1:C6,Data!D1:D6,\">2\",Data!D1:D6,\"<6\")", sum(Y[2:5])),
    ("COUNTIFS", "COUNTIFS(Data!D1:D6,\">=3\")", 4),
    ("AVERAGEIFS", "AVERAGEIFS(Data!C1:C6,Data!D1:D6,\"<=3\")", statistics.mean(Y[:3])),
    ("SUMPRODUCT", "SUMPRODUCT(Data!C1:C6,Data!D1:D6)", sum(x * y for x, y in zip(X, Y))),
    ("ROUND", "ROUND(2.675,2)", 2.68),
    ("ROUNDDOWN", "ROUNDDOWN(-3.14159,3)", -3.141),
    ("MROUND", "MROUND(10,3)", 9),
    ("CEILING.MATH", "_xlfn.CEILING.MATH(-5.5,2,-1)", -6),
    ("STDEV.S", "_xlfn.STDEV.S(Data!C1:C6)", statistics.stdev(Y)),
    ("VAR.S", "_xlfn.VAR.S(Data!C1:C6)", statistics.variance(Y)),
    ("CORREL", "CORREL(Data!C1:C6,Data!D1:D6)", statistics.correlation(Y, X)),
    ("SLOPE", "SLOPE(Data!C1:C6,Data!D1:D6)", slope),
    ("INTERCEPT", "INTERCEPT(Data!C1:C6,Data!D1:D6)", mean_y - slope * mean_x),
    ("RSQ", "RSQ(Data!C1:C6,Data!D1:D6)", statistics.correlation(Y, X) ** 2),
    ("MEDIAN", "MEDIAN(Data!C1:C6)", statistics.median(Y)),
    ("PERCENTILE.INC", "_xlfn.PERCENTILE.INC(Data!C1:C6,0.9)", pct_inc(Y, 0.9)),
    ("LARGE", "LARGE(Data!C1:C6,2)", sorted(Y)[-2]),
    ("RANK.EQ", "_xlfn.RANK.EQ(6.3,Data!C1:C6)", 3),
    ("GEOMEAN", "GEOMEAN(Data!C1:C6)", statistics.geometric_mean(Y)),
    ("NORM.S.INV", "_xlfn.NORM.S.INV(0.975)", 1.959963984540054),
    ("NORM.DIST", "_xlfn.NORM.DIST(1,0,1,TRUE)", 0.8413447460685429),
    ("SUBTOTAL", "SUBTOTAL(9,Data!C1:C6)", sum(Y)),
    ("AGGREGATE", "_xlfn.AGGREGATE(4,6,Data!C1:C6)", max(Y)),
    ("MAXIFS", "_xlfn.MAXIFS(Data!C1:C6,Data!D1:D6,\"<4\")", Y[2]),
    ("MMULT", "SUM(MMULT(Data!D1:D2,TRANSPOSE(Data!D1:D2)))", 9),
    ("LINEST", "INDEX(LINEST(Data!C1:C6,Data!D1:D6),1)", slope),
    # text & logic
    ("IFERROR", "IFERROR(1/0,\"n/a\")", "n/a"),
    ("IFS", "_xlfn.IFS(Data!D1>5,\"a\",TRUE,\"b\")", "b"),
    ("SWITCH", "_xlfn.SWITCH(2,1,\"一\",2,\"二\")", "二"),
    ("TEXT", "TEXT(0.256,\"0.0%\")", "25.6%"),
    ("TEXT date", "TEXT(DATE(2024,3,1),\"yyyy-mm-dd\")", "2024-03-01"),
    ("TEXTJOIN", "_xlfn.TEXTJOIN(\",\",TRUE,\"a\",\"\",\"b\")", "a,b"),
    ("CONCAT", "_xlfn.CONCAT(\"利润\",\"表\")", "利润表"),
    ("SUBSTITUTE", "SUBSTITUTE(\"600519.SH\",\".SH\",\"\")", "600519"),
    ("VALUE", "VALUE(\"1,234.5\")", 1234.5),
    ("LET", "_xlfn.LET(_xlpm.x,2,_xlpm.x*3)", 6),
    ("UNIQUE count", "COUNTA(_xlfn.UNIQUE(Data!D1:D6))", 6),
    ("SEQUENCE sum", "SUM(_xlfn.SEQUENCE(4))", 10),
    ("FILTER sum", "SUM(_xlfn._xlws.FILTER(Data!C1:C6,Data!D1:D6>4))", Y[4] + Y[5]),
]


def main(path):
    wb = xlsxwriter.Workbook(path)
    d = wb.add_worksheet("Data")
    for i in range(6):
        d.write_number(i, 0, CF[i])
        d.write_number(i, 1, serial(DATES[i]))
        d.write_number(i, 2, Y[i])
        d.write_number(i, 3, X[i])
        d.write_number(i, 4, Y[i])
    d.write_row(0, 6, ["a", "b", "c"])
    d.write_row(1, 6, [10, 20, 30])
    s = wb.add_worksheet("Funcs")
    for r, (name, f, want) in enumerate(CASES):
        s.write_string(r, 0, name)
        s.write_formula(r, 1, "=" + f)
        if isinstance(want, str):
            s.write_string(r, 2, want)
        elif want is not None:
            s.write_number(r, 2, float(want))
    wb.close()
    print(f"{len(CASES)} cases")


if __name__ == "__main__":
    main(sys.argv[1])
