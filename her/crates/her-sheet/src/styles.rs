//! Cell styles as the grid and the agent need them (design §5.2): the number format that turns
//! a stored number into the text Excel shows, and the look of a cell (bold, colours, alignment).
//!
//! Number formats cover what finance models use: General, fixed decimals, thousands separators,
//! percentages, scientific, dates and times, literal text, sections for negative and zero,
//! colours, currency symbols. Anything else shows the number as General and says so.

use std::collections::HashMap;

use quick_xml::events::Event;

use crate::Error;
use crate::package::attrs;

#[derive(Clone, Debug, Default, PartialEq)]
pub struct CellStyle {
    pub num_fmt: String,
    pub bold: bool,
    pub italic: bool,
    /// `RRGGBB`.
    pub color: Option<String>,
    pub fill: Option<String>,
    /// `left`, `center`, `right`…
    pub align: Option<String>,
}

#[derive(Debug, Default)]
pub struct Styles {
    xfs: Vec<CellStyle>,
}

/// Excel's built-in number formats (ECMA-376 §18.8.30), with the Chinese (zh-CN) ones.
fn builtin(id: u32) -> &'static str {
    match id {
        0 => "General",
        1 => "0",
        2 => "0.00",
        3 => "#,##0",
        4 => "#,##0.00",
        5 => "\"¥\"#,##0;\"¥\"-#,##0",
        6 => "\"¥\"#,##0;[Red]\"¥\"-#,##0",
        7 => "\"¥\"#,##0.00;\"¥\"-#,##0.00",
        8 => "\"¥\"#,##0.00;[Red]\"¥\"-#,##0.00",
        9 => "0%",
        10 => "0.00%",
        11 => "0.00E+00",
        12 => "# ?/?",
        13 => "# ??/??",
        14 => "yyyy/m/d",
        15 => "d-mmm-yy",
        16 => "d-mmm",
        17 => "mmm-yy",
        18 => "h:mm AM/PM",
        19 => "h:mm:ss AM/PM",
        20 => "h:mm",
        21 => "h:mm:ss",
        22 => "yyyy/m/d h:mm",
        27 | 36 | 50 | 57 => "yyyy\"年\"m\"月\"",
        28 | 29 | 51 | 52 | 53 | 54 | 58 => "m\"月\"d\"日\"",
        30 => "m-d-yy",
        31 => "yyyy\"年\"m\"月\"d\"日\"",
        32 => "h\"时\"mm\"分\"",
        33 => "h\"时\"mm\"分\"ss\"秒\"",
        34 | 55 => "上午/下午h\"时\"mm\"分\"",
        35 | 56 => "上午/下午h\"时\"mm\"分\"ss\"秒\"",
        37 => "#,##0 ;(#,##0)",
        38 => "#,##0 ;[Red](#,##0)",
        39 => "#,##0.00;(#,##0.00)",
        40 => "#,##0.00;[Red](#,##0.00)",
        45 => "mm:ss",
        46 => "[h]:mm:ss",
        47 => "mm:ss.0",
        48 => "##0.0E+0",
        49 => "@",
        _ => "General",
    }
}

const INDEXED: [&str; 8] = [
    "000000", "FFFFFF", "FF0000", "00FF00", "0000FF", "FFFF00", "FF00FF", "00FFFF",
];

fn color_of(a: &HashMap<String, String>) -> Option<String> {
    if let Some(rgb) = a.get("rgb") {
        let rgb = rgb.trim();
        return Some(rgb[rgb.len().saturating_sub(6)..].to_uppercase());
    }
    let i: usize = a.get("indexed")?.parse().ok()?;
    INDEXED
        .get(i.checked_sub(8).unwrap_or(i))
        .map(|s| s.to_string())
}

impl Styles {
    pub fn parse(xml: &[u8]) -> Result<Self, Error> {
        let (mut rd, _) = crate::xmltext::reader(xml);
        let mut fmts: HashMap<u32, String> = HashMap::new();
        let mut fonts: Vec<(bool, bool, Option<String>)> = Vec::new();
        let mut fills: Vec<Option<String>> = Vec::new();
        let mut xfs = Vec::new();
        let mut section = String::new();
        let mut in_xf: Option<CellStyle> = None;
        loop {
            let ev = rd.read_event()?;
            match &ev {
                Event::Start(e) | Event::Empty(e) => {
                    let empty = matches!(ev, Event::Empty(_));
                    let name = e.local_name().as_ref().to_string();
                    match name.as_str() {
                        "numFmts" | "fonts" | "fills" | "cellXfs" | "cellStyleXfs" | "dxfs" => {
                            section = name.clone()
                        }
                        "numFmt" if section == "numFmts" => {
                            let a = attrs(e)?;
                            if let (Some(id), Some(code)) = (
                                a.get("numFmtId").and_then(|v| v.parse().ok()),
                                a.get("formatCode"),
                            ) {
                                fmts.insert(id, code.clone());
                            }
                        }
                        "font" if section == "fonts" => fonts.push((false, false, None)),
                        "b" | "i" if section == "fonts" => {
                            let on = attrs(e)?
                                .get("val")
                                .is_none_or(|v| v != "0" && v != "false");
                            if let Some(f) = fonts.last_mut() {
                                if name == "b" { f.0 = on } else { f.1 = on }
                            }
                        }
                        "color" if section == "fonts" => {
                            if let Some(f) = fonts.last_mut() {
                                f.2 = color_of(&attrs(e)?);
                            }
                        }
                        "fill" if section == "fills" => fills.push(None),
                        "fgColor" if section == "fills" => {
                            if let Some(f) = fills.last_mut() {
                                *f = color_of(&attrs(e)?);
                            }
                        }
                        "patternFill" if section == "fills" => {
                            if attrs(e)?.get("patternType").is_some_and(|p| p == "none")
                                && let Some(f) = fills.last_mut()
                            {
                                *f = None;
                            }
                        }
                        "xf" if section == "cellXfs" => {
                            let a = attrs(e)?;
                            let id: u32 =
                                a.get("numFmtId").and_then(|v| v.parse().ok()).unwrap_or(0);
                            let font = a
                                .get("fontId")
                                .and_then(|v| v.parse::<usize>().ok())
                                .and_then(|i| fonts.get(i))
                                .cloned()
                                .unwrap_or_default();
                            let fill = a
                                .get("fillId")
                                .and_then(|v| v.parse::<usize>().ok())
                                .and_then(|i| fills.get(i).cloned().flatten());
                            let style = CellStyle {
                                num_fmt: fmts
                                    .get(&id)
                                    .cloned()
                                    .unwrap_or_else(|| builtin(id).to_string()),
                                bold: font.0,
                                italic: font.1,
                                color: font.2.filter(|c| c != "000000"),
                                fill: fill.filter(|c| c != "FFFFFF"),
                                align: None,
                            };
                            if empty {
                                xfs.push(style);
                            } else {
                                in_xf = Some(style);
                            }
                        }
                        "alignment" if in_xf.is_some() => {
                            in_xf.as_mut().unwrap().align = attrs(e)?.get("horizontal").cloned();
                        }
                        _ => {}
                    }
                }
                Event::End(e) => match e.local_name().as_ref() {
                    "xf" if in_xf.is_some() => xfs.push(in_xf.take().unwrap()),
                    "numFmts" | "fonts" | "fills" | "cellXfs" | "cellStyleXfs" | "dxfs" => {
                        section.clear()
                    }
                    _ => {}
                },
                Event::Eof => break,
                _ => {}
            }
        }
        Ok(Self { xfs })
    }

    pub fn get(&self, s: Option<&str>) -> CellStyle {
        let i = s.and_then(|s| s.parse::<usize>().ok()).unwrap_or(0);
        self.xfs.get(i).cloned().unwrap_or_else(|| CellStyle {
            num_fmt: "General".into(),
            ..Default::default()
        })
    }
}

/// The text Excel shows for a number in `fmt`, and the colour a `[Red]` section asks for.
pub fn format_number(v: f64, fmt: &str, date1904: bool) -> (String, Option<&'static str>) {
    let sections = split_sections(fmt);
    let (section, negative_in_own) = match (sections.len(), v) {
        (_, v) if v > 0.0 || sections.len() == 1 => (sections[0].as_str(), false),
        (n, v) if v < 0.0 && n >= 2 => (sections[1].as_str(), true),
        (n, _) if v == 0.0 && n >= 3 => (sections[2].as_str(), false),
        _ => (sections[0].as_str(), false),
    };
    let (section, color) = take_color(section);
    let section = section.as_str();
    let abs = if negative_in_own { v.abs() } else { v };
    let text = if section.eq_ignore_ascii_case("general") || section.is_empty() {
        general(abs)
    } else if is_date(section) {
        format_date(abs, section, date1904).unwrap_or_else(|| general(abs))
    } else {
        format_fixed(abs, section)
    };
    (text, color)
}

/// Whether a number format shows a number as a date or a time (its first section decides).
pub fn is_date_format(fmt: &str) -> bool {
    let sections = split_sections(fmt);
    let (section, _) = take_color(sections[0].as_str());
    !(section.eq_ignore_ascii_case("general") || section.is_empty()) && is_date(&section)
}

pub fn general(v: f64) -> String {
    if v == 0.0 {
        return "0".into();
    }
    let a = v.abs();
    if !(1e-9..1e11).contains(&a) {
        let s = format!("{v:.5E}");
        let (m, e) = s.split_once('E').unwrap();
        let m = m.trim_end_matches('0').trim_end_matches('.');
        let e: i32 = e.parse().unwrap();
        return format!("{m}E{}{:02}", if e < 0 { '-' } else { '+' }, e.abs());
    }
    // Up to 11 significant digits, as Excel's General shows in a normal-width column.
    let digits = (11 - (a.log10().floor() as i32 + 1)).clamp(0, 10) as usize;
    let s = format!("{v:.digits$}");
    if s.contains('.') {
        s.trim_end_matches('0').trim_end_matches('.').to_string()
    } else {
        s
    }
}

fn split_sections(fmt: &str) -> Vec<String> {
    let mut out = vec![String::new()];
    let mut quoted = false;
    let mut chars = fmt.chars();
    while let Some(c) = chars.next() {
        match c {
            '"' => {
                quoted = !quoted;
                out.last_mut().unwrap().push(c);
            }
            '\\' => {
                out.last_mut().unwrap().push(c);
                if let Some(n) = chars.next() {
                    out.last_mut().unwrap().push(n);
                }
            }
            ';' if !quoted => out.push(String::new()),
            _ => out.last_mut().unwrap().push(c),
        }
    }
    out
}

fn take_color(section: &str) -> (String, Option<&'static str>) {
    let mut color = None;
    let mut out = String::new();
    let mut rest = section;
    while let Some(i) = rest.find('[') {
        let Some(j) = rest[i..].find(']') else { break };
        let tag = &rest[i + 1..i + j];
        out.push_str(&rest[..i]);
        match tag.to_ascii_lowercase().as_str() {
            "red" => color = Some("FF0000"),
            "blue" => color = Some("0000FF"),
            "green" => color = Some("00FF00"),
            "black" => color = Some("000000"),
            t if t.starts_with('$') => {
                // [$¥-804]: the currency symbol, then the locale.
                let sym = tag[1..].split('-').next().unwrap_or("");
                out.push('"');
                out.push_str(sym);
                out.push('"');
            }
            t if t.starts_with('h') || t.starts_with('m') || t.starts_with('s') => {
                out.push_str(&rest[i..i + j + 1])
            }
            _ => {}
        }
        rest = &rest[i + j + 1..];
    }
    out.push_str(rest);
    (out, color)
}

/// Format codes outside quotes, for telling dates from numbers.
fn unquoted(section: &str) -> String {
    let mut out = String::new();
    let mut quoted = false;
    let mut chars = section.chars();
    while let Some(c) = chars.next() {
        match c {
            '"' => quoted = !quoted,
            '\\' => {
                chars.next();
            }
            _ if !quoted => out.push(c),
            _ => {}
        }
    }
    out
}

fn is_date(section: &str) -> bool {
    let u = unquoted(section).to_ascii_lowercase();
    u.contains('y')
        || u.contains('d')
        || u.contains('h')
        || u.contains('s')
        || (u.contains('m') && !u.contains('0') && !u.contains('#'))
}

fn format_fixed(v: f64, section: &str) -> String {
    // Literal parts before and after the number pattern.
    let mut prefix = String::new();
    let mut suffix = String::new();
    let mut pattern = String::new();
    let mut chars = section.chars().peekable();
    let mut seen_digit = false;
    while let Some(c) = chars.next() {
        let lit = match c {
            '"' => {
                let mut s = String::new();
                for d in chars.by_ref() {
                    if d == '"' {
                        break;
                    }
                    s.push(d);
                }
                Some(s)
            }
            '\\' => chars.next().map(String::from),
            '_' => {
                chars.next();
                Some(" ".into())
            }
            '*' => {
                chars.next();
                None
            }
            '0' | '#' | '?' | '.' | ',' | 'E' | 'e' | '+' | '-'
                if seen_digit || matches!(c, '0' | '#' | '?' | '.') =>
            {
                if matches!(c, '-') && !pattern.ends_with(['E', 'e']) {
                    Some("-".into())
                } else {
                    seen_digit = true;
                    pattern.push(c);
                    None
                }
            }
            '%' => {
                pattern.push('%');
                None
            }
            '@' => None,
            other => Some(other.to_string()),
        };
        if let Some(l) = lit {
            if seen_digit {
                suffix.push_str(&l)
            } else {
                prefix.push_str(&l)
            }
        }
    }
    let percent = pattern.matches('%').count();
    let pattern: String = pattern.chars().filter(|c| *c != '%').collect();
    let mut v = v * 100f64.powi(percent as i32);
    let pct = "%".repeat(percent);
    if let Some(e) = pattern.find(['E', 'e']) {
        let mant = &pattern[..e];
        let dec = mant.split('.').nth(1).map(|d| d.len()).unwrap_or(0);
        let s = format!("{:.*E}", dec, v);
        let (m, x) = s.split_once('E').unwrap();
        let x: i32 = x.parse().unwrap();
        return format!(
            "{prefix}{m}E{}{:02}{pct}{suffix}",
            if x < 0 { '-' } else { '+' },
            x.abs()
        );
    }
    // Trailing commas scale by thousands.
    let mut pattern = pattern.as_str();
    while let Some(p) = pattern.strip_suffix(',') {
        v /= 1000.0;
        pattern = p;
    }
    let grouping = pattern.contains(',');
    let (int_p, dec_p) = match pattern.split_once('.') {
        Some((i, d)) => (i, d),
        None => (pattern, ""),
    };
    let decimals = dec_p.len();
    let min_dec = dec_p.chars().filter(|c| *c == '0').count();
    let min_int = int_p.chars().filter(|c| *c == '0').count();
    let neg = v < 0.0;
    let s = format!("{:.*}", decimals, round_half_away(v.abs(), decimals));
    let (i, d) = match s.split_once('.') {
        Some((i, d)) => (i.to_string(), d.to_string()),
        None => (s.clone(), String::new()),
    };
    let mut d = d;
    while d.len() > min_dec && d.ends_with('0') {
        d.pop();
    }
    let mut i = if i == "0" && min_int == 0 {
        String::new()
    } else {
        i
    };
    while i.len() < min_int {
        i.insert(0, '0');
    }
    if grouping {
        let bytes: Vec<char> = i.chars().collect();
        let mut g = String::new();
        for (k, c) in bytes.iter().enumerate() {
            if k > 0 && (bytes.len() - k).is_multiple_of(3) {
                g.push(',');
            }
            g.push(*c);
        }
        i = g;
    }
    let num = if d.is_empty() { i } else { format!("{i}.{d}") };
    let zero = num.chars().all(|c| matches!(c, '0' | '.' | ','));
    format!(
        "{}{prefix}{num}{pct}{suffix}",
        if neg && !zero { "-" } else { "" }
    )
}

/// Excel rounds halves away from zero (Rust's formatting rounds them to even); the nudge keeps
/// 1.005 from becoming 1.00 through binary representation.
fn round_half_away(v: f64, decimals: usize) -> f64 {
    let p = 10f64.powi(decimals as i32);
    let scaled = v * p;
    (scaled + scaled * 1e-14).round() / p
}

/// Days since 1899-12-30 (or 1904-01-01) → (y, m, d).
fn civil(serial: i64, date1904: bool) -> (i64, u32, u32) {
    // Excel's 1900 system counts a 29 February 1900 that never was.
    let days = if date1904 {
        serial + 1462
    } else if serial < 60 {
        serial + 1
    } else {
        serial
    };
    let z = days - 25569 + 719468; // to days since 0000-03-01 via the Unix epoch
    let era = z.div_euclid(146097);
    let doe = z - era * 146097;
    let yoe = (doe - doe / 1460 + doe / 36524 - doe / 146096) / 365;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = (doy - (153 * mp + 2) / 5 + 1) as u32;
    let m = if mp < 10 { mp + 3 } else { mp - 9 } as u32;
    (yoe + era * 400 + i64::from(m <= 2), m, d)
}

fn format_date(v: f64, section: &str, date1904: bool) -> Option<String> {
    if !(0.0..2_958_466.0).contains(&v) {
        return None;
    }
    let day = v.floor() as i64;
    let secs = ((v - v.floor()) * 86400.0).round() as i64;
    let (y, mo, d) = civil(day, date1904);
    let (h, mi, s) = (secs / 3600 % 24, secs / 60 % 60, secs % 60);
    let ampm = section.contains("AM/PM") || section.contains("上午/下午");
    let h12 = if ampm {
        if h % 12 == 0 { 12 } else { h % 12 }
    } else {
        h
    };
    let mut out = String::new();
    let chars: Vec<char> = section.chars().collect();
    let mut i = 0;
    let mut last_was_hour = false;
    while i < chars.len() {
        let c = chars[i];
        let run = chars[i..]
            .iter()
            .take_while(|x| x.eq_ignore_ascii_case(&c))
            .count();
        match c.to_ascii_lowercase() {
            '"' => {
                let end = chars[i + 1..]
                    .iter()
                    .position(|x| *x == '"')
                    .map(|p| i + 1 + p)
                    .unwrap_or(chars.len());
                out.extend(&chars[i + 1..end]);
                i = end + 1;
                continue;
            }
            '\\' => {
                if let Some(n) = chars.get(i + 1) {
                    out.push(*n);
                }
                i += 2;
                continue;
            }
            'y' => out.push_str(&if run <= 2 {
                format!("{:02}", y % 100)
            } else {
                y.to_string()
            }),
            'm' => {
                // After an hour or before seconds, m is minutes.
                let next_s = chars[i + run..]
                    .iter()
                    .find(|x| x.is_ascii_alphabetic())
                    .is_some_and(|x| x.eq_ignore_ascii_case(&'s'));
                if last_was_hour || next_s {
                    out.push_str(&format!("{:0w$}", mi, w = run.min(2)));
                } else {
                    const M: [&str; 12] = [
                        "Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct",
                        "Nov", "Dec",
                    ];
                    match run {
                        1 => out.push_str(&mo.to_string()),
                        2 => out.push_str(&format!("{mo:02}")),
                        3 => out.push_str(M[mo as usize - 1]),
                        _ => out.push_str(M[mo as usize - 1]),
                    }
                }
            }
            'd' => match run {
                1 => out.push_str(&d.to_string()),
                _ => out.push_str(&format!("{d:02}")),
            },
            'h' => out.push_str(&format!("{:0w$}", h12, w = run.min(2))),
            's' => out.push_str(&format!("{:0w$}", s, w = run.min(2))),
            'a' if chars[i..].iter().collect::<String>().starts_with("AM/PM") => {
                out.push_str(if h < 12 { "AM" } else { "PM" });
                i += 5;
                continue;
            }
            '上' if chars[i..]
                .iter()
                .collect::<String>()
                .starts_with("上午/下午") =>
            {
                out.push_str(if h < 12 { "上午" } else { "下午" });
                i += 5;
                continue;
            }
            '[' => {
                // [h]: elapsed hours.
                let end = chars[i..]
                    .iter()
                    .position(|x| *x == ']')
                    .map(|p| i + p)
                    .unwrap_or(chars.len());
                if chars[i + 1..end]
                    .iter()
                    .all(|x| x.eq_ignore_ascii_case(&'h'))
                {
                    out.push_str(&((v * 24.0).floor() as i64).to_string());
                }
                i = end + 1;
                last_was_hour = true;
                continue;
            }
            _ => out.push(c),
        }
        if c.is_ascii_alphabetic() {
            last_was_hour = c.eq_ignore_ascii_case(&'h');
        }
        i += run.max(1);
    }
    Some(out)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn f(v: f64, fmt: &str) -> String {
        format_number(v, fmt, false).0
    }

    #[test]
    fn numbers() {
        assert_eq!(f(1234.5678, "General"), "1234.5678");
        assert_eq!(f(0.1 + 0.2, "General"), "0.3");
        assert_eq!(f(1234.5678, "0.00"), "1234.57");
        assert_eq!(f(1234567.891, "#,##0.00"), "1,234,567.89");
        assert_eq!(f(-1234.5, "#,##0"), "-1,235");
        assert_eq!(f(0.085, "0.0%"), "8.5%");
        assert_eq!(f(0.12345, "0.00%"), "12.35%");
        assert_eq!(f(12345.0, "0.00E+00"), "1.23E+04");
        assert_eq!(f(1234567.0, "#,##0,"), "1,235");
        assert_eq!(f(-1234.5, "#,##0.00;(#,##0.00)"), "(1,234.50)");
        assert_eq!(
            format_number(-5.0, "0;[Red]-0", false),
            ("-5".into(), Some("FF0000"))
        );
        assert_eq!(f(0.0, "#,##0;-#,##0;\"-\""), "-");
        assert_eq!(f(1344.0, "\"¥\"#,##0.00"), "¥1,344.00");
        assert_eq!(f(1344.0, "[$¥-804]#,##0"), "¥1,344");
        assert_eq!(f(0.5, "#.##"), ".5");
        assert_eq!(f(3.0, "0.0\"x\""), "3.0x");
        assert_eq!(f(1e20, "General"), "1E+20");
    }

    #[test]
    fn dates() {
        assert_eq!(f(45352.0, "yyyy-mm-dd"), "2024-03-01");
        assert_eq!(f(45352.0, "yyyy/m/d"), "2024/3/1");
        assert_eq!(f(45352.75, "yyyy/m/d h:mm"), "2024/3/1 18:00");
        assert_eq!(f(45352.0, "yyyy\"年\"m\"月\"d\"日\""), "2024年3月1日");
        assert_eq!(f(45352.0, "d-mmm-yy"), "1-Mar-24");
        assert_eq!(f(0.5, "h:mm AM/PM"), "12:00 PM");
        assert_eq!(f(1.0, "yyyy-mm-dd"), "1900-01-01");
        assert_eq!(f(61.0, "yyyy-mm-dd"), "1900-03-01");
        assert_eq!(format_number(0.0, "yyyy-mm-dd", true).0, "1904-01-01");
    }
}
