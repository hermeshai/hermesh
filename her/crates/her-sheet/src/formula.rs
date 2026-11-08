//! Just enough of the A1 formula grammar to (1) move a formula to another
//! cell, as Excel does when it expands a shared formula, and (2) turn the
//! formula a person or an agent typed into what the file stores.
//!
//! This is a tokenizer, not a parser: it finds references, function names,
//! strings and brackets, and copies everything else through unchanged.

use crate::cellref::col_name;

use crate::cellref::parse_col;

use crate::cellref::MAX_COL;

use crate::Error;
use crate::cellref::MAX_ROW;

#[derive(Clone, Debug, PartialEq)]
pub enum Tok {
    /// `"text"` with its quotes.
    Str(String),
    /// `Sheet1!`, `'My sheet'!`, `[1]Sheet1!`: copied as is.
    SheetPrefix(String),
    /// `[...]`: structured reference or external workbook index.
    Bracket(String),
    /// `{1,2;3,4}`.
    Array(String),
    /// `#REF!`, `#N/A`.
    Error(String),
    Number(String),
    /// `$A$1`, `B2`.
    Cell(String),
    /// One side of a whole-column range `A:C`.
    Col(String),
    /// One side of a whole-row range `1:3`.
    Row(String),
    /// Name followed by `(`.
    Func(String),
    /// A defined name, a table name, TRUE/FALSE.
    Name(String),
    /// Anything else, one char at a time: operators, separators, spaces.
    Other(String),
}

impl Tok {
    pub fn text(&self) -> &str {
        match self {
            Tok::Str(s)
            | Tok::SheetPrefix(s)
            | Tok::Bracket(s)
            | Tok::Array(s)
            | Tok::Error(s)
            | Tok::Number(s)
            | Tok::Cell(s)
            | Tok::Col(s)
            | Tok::Row(s)
            | Tok::Func(s)
            | Tok::Name(s)
            | Tok::Other(s) => s,
        }
    }
}

const ERRORS: &[&str] = &[
    "#NULL!",
    "#DIV/0!",
    "#VALUE!",
    "#REF!",
    "#NAME?",
    "#NUM!",
    "#N/A",
    "#GETTING_DATA",
    "#SPILL!",
    "#CALC!",
    "#FIELD!",
    "#BLOCKED!",
    "#CONNECT!",
    "#BUSY!",
    "#UNKNOWN!",
];

/// One of Excel's error values (IronCalc has others of its own, like `#ERROR!` and `#CIRC!`).
pub fn is_excel_error(s: &str) -> bool {
    ERRORS.contains(&s)
}

fn bad(msg: impl Into<String>) -> Error {
    Error::Formula(msg.into())
}

pub fn tokenize(f: &str) -> Result<Vec<Tok>, Error> {
    let chars: Vec<char> = f.chars().collect();
    let mut toks = Vec::new();
    let mut i = 0;
    let take = |a: usize, b: usize| -> String { chars[a..b].iter().collect() };
    while i < chars.len() {
        let c = chars[i];
        match c {
            '"' => {
                let start = i;
                i += 1;
                loop {
                    match chars.get(i) {
                        None => return Err(bad("unterminated string")),
                        Some('"') if chars.get(i + 1) == Some(&'"') => i += 2,
                        Some('"') => {
                            i += 1;
                            break;
                        }
                        Some(_) => i += 1,
                    }
                }
                toks.push(Tok::Str(take(start, i)));
            }
            '\'' => {
                let start = i;
                i += 1;
                loop {
                    match chars.get(i) {
                        None => return Err(bad("unterminated quoted sheet name")),
                        Some('\'') if chars.get(i + 1) == Some(&'\'') => i += 2,
                        Some('\'') => {
                            i += 1;
                            break;
                        }
                        Some(_) => i += 1,
                    }
                }
                if chars.get(i) != Some(&'!') {
                    return Err(bad("quoted name not followed by '!'"));
                }
                i += 1;
                toks.push(Tok::SheetPrefix(take(start, i)));
            }
            '[' => {
                let start = i;
                let mut depth = 0;
                loop {
                    match chars.get(i) {
                        None => return Err(bad("unterminated '['")),
                        Some('\'') => i += 2, // escapes the next char inside structured references
                        Some('[') => {
                            depth += 1;
                            i += 1;
                        }
                        Some(']') => {
                            depth -= 1;
                            i += 1;
                            if depth == 0 {
                                break;
                            }
                        }
                        Some(_) => i += 1,
                    }
                }
                let text = take(start, i.min(chars.len()));
                // `[1]Sheet1!A1`: an external workbook index glued to a sheet name.
                if text[1..text.len() - 1].bytes().all(|b| b.is_ascii_digit()) {
                    let s2 = i;
                    while i < chars.len() && is_name_char(chars[i]) {
                        i += 1;
                    }
                    if chars.get(i) == Some(&'!') {
                        i += 1;
                        toks.push(Tok::SheetPrefix(format!("{}{}", text, take(s2, i))));
                        continue;
                    }
                    i = s2;
                }
                toks.push(Tok::Bracket(text));
            }
            '{' => {
                let start = i;
                let mut in_str = false;
                loop {
                    match chars.get(i) {
                        None => return Err(bad("unterminated '{'")),
                        Some('"') => {
                            in_str = !in_str;
                            i += 1
                        }
                        Some('}') if !in_str => {
                            i += 1;
                            break;
                        }
                        Some(_) => i += 1,
                    }
                }
                toks.push(Tok::Array(take(start, i)));
            }
            '#' => {
                let rest: String = chars[i..]
                    .iter()
                    .take(16)
                    .collect::<String>()
                    .to_uppercase();
                match ERRORS.iter().find(|e| rest.starts_with(*e)) {
                    Some(e) => {
                        let n = e.chars().count();
                        toks.push(Tok::Error(take(i, i + n)));
                        i += n;
                    }
                    // `A1#`: the spill-range operator.
                    None => {
                        toks.push(Tok::Other("#".into()));
                        i += 1;
                    }
                }
            }
            _ if c.is_ascii_digit()
                || (c == '.' && chars.get(i + 1).is_some_and(|d| d.is_ascii_digit())) =>
            {
                let start = i;
                while i < chars.len() && (chars[i].is_ascii_digit() || chars[i] == '.') {
                    i += 1;
                }
                if i < chars.len()
                    && (chars[i] == 'E' || chars[i] == 'e')
                    && (chars.get(i + 1).is_some_and(|d| d.is_ascii_digit())
                        || (matches!(chars.get(i + 1), Some('+') | Some('-'))
                            && chars.get(i + 2).is_some_and(|d| d.is_ascii_digit())))
                {
                    i += 2;
                    while i < chars.len() && chars[i].is_ascii_digit() {
                        i += 1;
                    }
                }
                toks.push(Tok::Number(take(start, i)));
            }
            _ if c == '$' || c == '_' || c == '\\' || c.is_alphabetic() => {
                let start = i;
                i += 1;
                while i < chars.len() && (is_name_char(chars[i]) || chars[i] == '$') {
                    i += 1;
                }
                let word = take(start, i);
                let tok = match chars.get(i) {
                    Some('(') => Tok::Func(word),
                    Some('!') => {
                        i += 1;
                        Tok::SheetPrefix(format!("{word}!"))
                    }
                    _ if parse_cell(&word).is_some() => Tok::Cell(word),
                    _ => Tok::Name(word),
                };
                toks.push(tok);
            }
            _ => {
                toks.push(Tok::Other(c.to_string()));
                i += 1;
            }
        }
    }
    mark_whole_ranges(&mut toks);
    Ok(toks)
}

fn is_name_char(c: char) -> bool {
    c.is_alphanumeric() || c == '_' || c == '.' || c == '\\' || c == '?'
}

/// `A:C` and `1:3` are column and row references only next to a colon;
/// alone, `A` is a name and `1` a number.
fn mark_whole_ranges(toks: &mut [Tok]) {
    for i in 1..toks.len().saturating_sub(1) {
        if toks[i] != Tok::Other(":".into()) {
            continue;
        }
        let (a, b) = (
            toks[i - 1].text().to_string(),
            toks[i + 1].text().to_string(),
        );
        let is_col = |s: &str| {
            parse_col_part(s).is_some() && s.trim_start_matches('$').parse::<u32>().is_err()
        };
        let is_row = |s: &str| parse_row_part(s).is_some();
        if matches!(toks[i - 1], Tok::Name(_))
            && matches!(toks[i + 1], Tok::Name(_))
            && is_col(&a)
            && is_col(&b)
        {
            toks[i - 1] = Tok::Col(a);
            toks[i + 1] = Tok::Col(b);
        } else if matches!(toks[i - 1], Tok::Number(_) | Tok::Name(_))
            && matches!(toks[i + 1], Tok::Number(_) | Tok::Name(_))
            && is_row(&a)
            && is_row(&b)
        {
            toks[i - 1] = Tok::Row(a);
            toks[i + 1] = Tok::Row(b);
        }
    }
}

/// `$A$1` → (col, col_abs, row, row_abs).
pub(crate) fn parse_cell(s: &str) -> Option<(u32, bool, u32, bool)> {
    let (col_abs, rest) = match s.strip_prefix('$') {
        Some(r) => (true, r),
        None => (false, s),
    };
    let split = rest.find(|c: char| !c.is_ascii_alphabetic())?;
    let col = parse_col(&rest[..split])?;
    let (row_abs, digits) = match rest[split..].strip_prefix('$') {
        Some(r) => (true, r),
        None => (false, &rest[split..]),
    };
    if digits.is_empty() || !digits.bytes().all(|b| b.is_ascii_digit()) || digits.starts_with('0') {
        return None;
    }
    let row: u32 = digits.parse().ok()?;
    (row <= MAX_ROW).then_some((col, col_abs, row, row_abs))
}

pub(crate) fn parse_col_part(s: &str) -> Option<(u32, bool)> {
    let (abs, rest) = match s.strip_prefix('$') {
        Some(r) => (true, r),
        None => (false, s),
    };
    parse_col(rest).map(|c| (c, abs))
}

pub(crate) fn parse_row_part(s: &str) -> Option<(u32, bool)> {
    let (abs, rest) = match s.strip_prefix('$') {
        Some(r) => (true, r),
        None => (false, s),
    };
    if rest.is_empty() || !rest.bytes().all(|b| b.is_ascii_digit()) || rest.starts_with('0') {
        return None;
    }
    let r: u32 = rest.parse().ok()?;
    (r <= MAX_ROW).then_some((r, abs))
}

fn shift(v: u32, d: i64, max: u32) -> Result<u32, Error> {
    let n = v as i64 + d;
    if n < 1 || n > max as i64 {
        return Err(bad("reference moves off the sheet"));
    }
    Ok(n as u32)
}

/// The formula as it reads when moved by (`drow`, `dcol`): relative parts
/// of references move, absolute parts and everything else stay.
pub fn translate(f: &str, drow: i64, dcol: i64) -> Result<String, Error> {
    if drow == 0 && dcol == 0 {
        return Ok(f.to_string());
    }
    let mut out = String::with_capacity(f.len() + 8);
    for t in tokenize(f)? {
        match &t {
            Tok::Cell(s) => {
                let (c, ca, r, ra) = parse_cell(s).unwrap();
                let c = if ca { c } else { shift(c, dcol, MAX_COL)? };
                let r = if ra { r } else { shift(r, drow, MAX_ROW)? };
                out.push_str(&format!(
                    "{}{}{}{}",
                    if ca { "$" } else { "" },
                    col_name(c),
                    if ra { "$" } else { "" },
                    r
                ));
            }
            Tok::Col(s) => {
                let (c, abs) = parse_col_part(s).unwrap();
                let c = if abs { c } else { shift(c, dcol, MAX_COL)? };
                out.push_str(&format!("{}{}", if abs { "$" } else { "" }, col_name(c)));
            }
            Tok::Row(s) => {
                let (r, abs) = parse_row_part(s).unwrap();
                let r = if abs { r } else { shift(r, drow, MAX_ROW)? };
                out.push_str(&format!("{}{}", if abs { "$" } else { "" }, r));
            }
            other => out.push_str(other.text()),
        }
    }
    Ok(out)
}

/// Functions newer than Excel 2007 are stored with a `_xlfn.` prefix; without
/// it Excel shows `#NAME?`.
const XLFN: &[&str] = &[
    "ACOT",
    "ACOTH",
    "AGGREGATE",
    "ARABIC",
    "BASE",
    "BETA.DIST",
    "BETA.INV",
    "BINOM.DIST",
    "BINOM.DIST.RANGE",
    "BINOM.INV",
    "BITAND",
    "BITLSHIFT",
    "BITOR",
    "BITRSHIFT",
    "BITXOR",
    "CEILING.MATH",
    "CEILING.PRECISE",
    "CHISQ.DIST",
    "CHISQ.DIST.RT",
    "CHISQ.INV",
    "CHISQ.INV.RT",
    "CHISQ.TEST",
    "COMBINA",
    "CONCAT",
    "CONFIDENCE.NORM",
    "CONFIDENCE.T",
    "COT",
    "COTH",
    "COVARIANCE.P",
    "COVARIANCE.S",
    "CSC",
    "CSCH",
    "DAYS",
    "DECIMAL",
    "ERF.PRECISE",
    "ERFC.PRECISE",
    "EXPON.DIST",
    "F.DIST",
    "F.DIST.RT",
    "F.INV",
    "F.INV.RT",
    "F.TEST",
    "FLOOR.MATH",
    "FLOOR.PRECISE",
    "FORECAST.ETS",
    "FORECAST.ETS.CONFINT",
    "FORECAST.ETS.SEASONALITY",
    "FORECAST.ETS.STAT",
    "FORECAST.LINEAR",
    "FORMULATEXT",
    "GAMMA",
    "GAMMA.DIST",
    "GAMMA.INV",
    "GAMMALN.PRECISE",
    "GAUSS",
    "HYPGEOM.DIST",
    "IFNA",
    "IFS",
    "IMCOSH",
    "IMCOT",
    "IMCSC",
    "IMCSCH",
    "IMSEC",
    "IMSECH",
    "IMSINH",
    "IMTAN",
    "ISFORMULA",
    "ISOWEEKNUM",
    "LOGNORM.DIST",
    "LOGNORM.INV",
    "MAXIFS",
    "MINIFS",
    "MODE.MULT",
    "MODE.SNGL",
    "MUNIT",
    "NEGBINOM.DIST",
    "NETWORKDAYS.INTL",
    "NORM.DIST",
    "NORM.INV",
    "NORM.S.DIST",
    "NORM.S.INV",
    "NUMBERVALUE",
    "PDURATION",
    "PERCENTILE.EXC",
    "PERCENTILE.INC",
    "PERCENTRANK.EXC",
    "PERCENTRANK.INC",
    "PERMUTATIONA",
    "PHI",
    "POISSON.DIST",
    "QUARTILE.EXC",
    "QUARTILE.INC",
    "RANK.AVG",
    "RANK.EQ",
    "RRI",
    "SEC",
    "SECH",
    "SHEET",
    "SHEETS",
    "SKEW.P",
    "STDEV.P",
    "STDEV.S",
    "SWITCH",
    "T.DIST",
    "T.DIST.2T",
    "T.DIST.RT",
    "T.INV",
    "T.INV.2T",
    "T.TEST",
    "TEXTJOIN",
    "UNICHAR",
    "UNICODE",
    "VAR.P",
    "VAR.S",
    "WEBSERVICE",
    "WEIBULL.DIST",
    "WORKDAY.INTL",
    "XLOOKUP",
    "XOR",
    "Z.TEST",
    "ENCODEURL",
    "FILTERXML",
    "TEXTBEFORE",
    "TEXTAFTER",
    "VALUETOTEXT",
    "ISOMITTED",
    "REGEXTEST",
    "REGEXEXTRACT",
    "REGEXREPLACE",
    "PERCENTOF",
];

/// Functions whose result can spill. Written without the dynamic-array
/// metadata they would show as `@FILTER(...)` in Excel, so the first version
/// refuses them.
const SPILL: &[&str] = &[
    "FILTER",
    "SORT",
    "SORTBY",
    "UNIQUE",
    "SEQUENCE",
    "RANDARRAY",
    "XMATCH",
    "TEXTSPLIT",
    "VSTACK",
    "HSTACK",
    "TAKE",
    "DROP",
    "CHOOSECOLS",
    "CHOOSEROWS",
    "TOCOL",
    "TOROW",
    "WRAPROWS",
    "WRAPCOLS",
    "EXPAND",
    "GROUPBY",
    "PIVOTBY",
    "TRIMRANGE",
    "ARRAYTOTEXT",
    "IMAGE",
];

/// Functions with parameters, which need `_xlpm.` names.
const LAMBDA_FAMILY: &[&str] = &[
    "LET",
    "LAMBDA",
    "MAP",
    "REDUCE",
    "SCAN",
    "BYROW",
    "BYCOL",
    "MAKEARRAY",
];

/// What a person or an agent typed (`=xlookup(...)`) → the stored text
/// (`_xlfn.XLOOKUP(...)`). Refuses what the first version cannot write
/// faithfully.
pub fn normalize_for_write(input: &str) -> Result<String, Error> {
    let f = input.trim();
    let f = f.strip_prefix('=').unwrap_or(f).trim_start();
    if f.is_empty() {
        return Err(bad("empty formula"));
    }
    let toks = tokenize(f)?;
    let mut depth: i32 = 0;
    let mut out = String::with_capacity(f.len() + 8);
    for t in &toks {
        match t {
            Tok::Func(name) => {
                let upper = name.to_uppercase();
                let bare = upper
                    .trim_start_matches("_XLFN.")
                    .trim_start_matches("_XLWS.");
                if LAMBDA_FAMILY.contains(&bare) {
                    return Err(Error::Unsupported(format!(
                        "{bare} is not written in this version"
                    )));
                }
                if SPILL.contains(&bare) {
                    return Err(Error::Unsupported(format!(
                        "{bare} returns a spilled array; not written in this version"
                    )));
                }
                if let Some(rest) = upper.strip_prefix("_XLFN.") {
                    out.push_str(&format!("_xlfn.{}", rest.replace("_XLWS.", "_xlws.")));
                } else if XLFN.contains(&upper.as_str()) {
                    out.push_str(&format!("_xlfn.{upper}"));
                } else if name.is_ascii() {
                    out.push_str(&upper);
                } else {
                    out.push_str(name);
                }
            }
            Tok::Other(s) if s == "(" => {
                depth += 1;
                out.push('(');
            }
            Tok::Other(s) if s == ")" => {
                depth -= 1;
                if depth < 0 {
                    return Err(bad("unbalanced ')'"));
                }
                out.push(')');
            }
            Tok::Other(s) if s == "#" => {
                return Err(Error::Unsupported(
                    "spill-range references (A1#) are not written in this version".into(),
                ));
            }
            other => out.push_str(other.text()),
        }
    }
    if depth != 0 {
        return Err(bad("unbalanced '('"));
    }
    Ok(out)
}

/// The stored text as a person reads it: no `_xlfn.` prefixes.
pub fn display(stored: &str) -> String {
    stored
        .replace("_xlfn._xlws.", "")
        .replace("_xlfn.", "")
        .replace("_xlpm.", "")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn moves_relative_parts_only() {
        assert_eq!(
            translate("A1+$B2+C$3+$D$4", 1, 1).unwrap(),
            "B2+$B3+D$3+$D$4"
        );
        assert_eq!(
            translate("SUM(A1:B2)*Sheet2!C3", 2, 0).unwrap(),
            "SUM(A3:B4)*Sheet2!C5"
        );
        assert_eq!(
            translate("SUM('My sheet'!A1:A10)", 1, 0).unwrap(),
            "SUM('My sheet'!A2:A11)"
        );
        assert_eq!(
            translate("SUM(A:A)+SUM($B:C)", 5, 1).unwrap(),
            "SUM(B:B)+SUM($B:D)"
        );
        assert_eq!(
            translate("SUM(1:1)+SUM($2:3)", 1, 9).unwrap(),
            "SUM(2:2)+SUM($2:4)"
        );
    }

    #[test]
    fn leaves_non_references_alone() {
        assert_eq!(
            translate("LOG10(A1)+ATAN2(1,2)", 1, 0).unwrap(),
            "LOG10(A2)+ATAN2(1,2)"
        );
        assert_eq!(translate("\"A1\"&A1", 1, 0).unwrap(), "\"A1\"&A2");
        assert_eq!(translate("1E+10+A1+1.5e3", 1, 0).unwrap(), "1E+10+A2+1.5e3");
        assert_eq!(
            translate("Comps[@PE]/[@ROE]+growth", 3, 3).unwrap(),
            "Comps[@PE]/[@ROE]+growth"
        );
        assert_eq!(
            translate("[1]Sheet1!A1+#REF!", 1, 0).unwrap(),
            "[1]Sheet1!A2+#REF!"
        );
        assert_eq!(
            translate("SUM({1,2;3,4})+TRUE", 1, 0).unwrap(),
            "SUM({1,2;3,4})+TRUE"
        );
        assert_eq!(
            translate("IF(A1=\"\",\"\",A1)", 1, 0).unwrap(),
            "IF(A2=\"\",\"\",A2)"
        );
        assert_eq!(
            translate("'利润表'!B9*0.8", 0, 1).unwrap(),
            "'利润表'!C9*0.8"
        );
        assert!(translate("A1", -1, 0).is_err());
    }

    #[test]
    fn normalizes_typed_formulas() {
        assert_eq!(
            normalize_for_write("=xlookup(1,A:A,B:B)").unwrap(),
            "_xlfn.XLOOKUP(1,A:A,B:B)"
        );
        assert_eq!(normalize_for_write("=sum(A1:A3)").unwrap(), "SUM(A1:A3)");
        assert_eq!(
            normalize_for_write("=_xlfn.STDEV.S(A1:A3)").unwrap(),
            "_xlfn.STDEV.S(A1:A3)"
        );
        assert_eq!(
            normalize_for_write("=WSS(\"600519.SH\",\"close\")").unwrap(),
            "WSS(\"600519.SH\",\"close\")"
        );
        assert!(matches!(
            normalize_for_write("=FILTER(A:A,B:B>1)"),
            Err(Error::Unsupported(_))
        ));
        assert!(matches!(
            normalize_for_write("=LET(x,1,x)"),
            Err(Error::Unsupported(_))
        ));
        assert!(normalize_for_write("=SUM(A1").is_err());
        assert!(normalize_for_write("=\"abc").is_err());
    }
}
