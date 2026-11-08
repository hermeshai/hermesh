//! Excel's fill handle, one line at a time: what the cells a drag adds hold, from the cells it
//! started from. A line is a column dragged up or down, a row dragged left or right, its source
//! cells given in the order the drag moves away from them.
//!
//! As Excel has it: numbers carry on their linear trend (a single number is copied unless the
//! switch key is held), dates go a day at a time (or by the months between them), text ending in
//! a number counts on (`Q1` → `Q2`, `第1期` → `第2期`), and the built-in lists (周一, 一月, Jan…)
//! go round. Anything else, formulas among it, repeats; the caller moves formulas.

/// A source cell, as the fill reads it.
#[derive(Clone, Debug, PartialEq)]
pub enum Source {
    Empty,
    Number { value: f64, shown: Shown },
    Text(String),
    /// A formula, a boolean or an error: only ever repeated.
    Other,
}

/// How a number is shown, which decides how a single one carries on.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Shown {
    Plain,
    /// A date (with or without a time): a day at a time.
    Date,
    /// A time of day only: an hour at a time.
    Time,
}

/// What a cell the drag adds holds.
#[derive(Clone, Debug, PartialEq)]
pub enum Filled {
    /// The source cell at this index, repeated (formulas moved by the caller).
    Copy(usize),
    Number(f64),
    Text(String),
}

/// The `count` cells after `source` along the drag. `switch` is the key Excel holds to swap
/// copying and counting; `date1904` is the workbook's date system.
pub fn extend(source: &[Source], count: usize, switch: bool, date1904: bool) -> Vec<Filled> {
    let copy = || (0..count).map(|k| Filled::Copy(k % source.len())).collect();
    if source.is_empty() {
        return Vec::new();
    }
    let single = source.len() == 1;
    // A single number is copied unless switched; a series counts unless switched.
    let counts = if single { switch } else { !switch };
    if let Some(numbers) = all_numbers(source) {
        let shown = numbers[0].1;
        if numbers.iter().any(|n| n.1 != shown) {
            return copy();
        }
        let values: Vec<f64> = numbers.iter().map(|n| n.0).collect();
        // A single date counts by itself; held, it is copied.
        let counts = if single && shown != Shown::Plain { !switch } else { counts };
        if !counts {
            return copy();
        }
        if single {
            let step = if shown == Shown::Time { 1.0 / 24.0 } else { 1.0 };
            return (1..=count).map(|k| Filled::Number(tidy(values[0] + step * k as f64))).collect();
        }
        if shown == Shown::Date
            && let Some(months) = by_months(&values, count, date1904)
        {
            return months.into_iter().map(Filled::Number).collect();
        }
        return trend(&values, count).into_iter().map(|v| Filled::Number(tidy(v))).collect();
    }
    if switch {
        return copy();
    }
    if let Some(series) = numbered(source) {
        let values: Vec<f64> = series.iter().map(|n| n.number as f64).collect();
        let next = if single { (1..=count).map(|k| values[0] + k as f64).collect() } else { trend(&values, count) };
        let first = &series[0];
        return next.into_iter().map(|v| Filled::Text(first.with((v.round() as i64).unsigned_abs()))).collect();
    }
    if let Some((list, at, case)) = listed(source) {
        let len = list.len() as i64;
        let step = if single {
            1
        } else {
            let step = (at[1] - at[0]).rem_euclid(len);
            if at.windows(2).any(|w| (w[1] - w[0]).rem_euclid(len) != step) {
                return copy();
            }
            step
        };
        let last = *at.last().unwrap();
        return (1..=count as i64).map(|k| Filled::Text(case.apply(list[(last + step * k).rem_euclid(len) as usize]))).collect();
    }
    copy()
}

fn all_numbers(source: &[Source]) -> Option<Vec<(f64, Shown)>> {
    source
        .iter()
        .map(|s| match s {
            Source::Number { value, shown } => Some((*value, *shown)),
            _ => None,
        })
        .collect()
}

/// Excel's linear trend: the least-squares line through the source, carried on.
fn trend(values: &[f64], count: usize) -> Vec<f64> {
    let n = values.len() as f64;
    let mean_x = (n - 1.0) / 2.0;
    let mean_y = values.iter().sum::<f64>() / n;
    let (mut sxy, mut sxx) = (0.0, 0.0);
    for (i, y) in values.iter().enumerate() {
        let dx = i as f64 - mean_x;
        sxy += dx * (y - mean_y);
        sxx += dx * dx;
    }
    let slope = if sxx == 0.0 { 0.0 } else { sxy / sxx };
    (0..count).map(|k| mean_y + slope * ((values.len() + k) as f64 - mean_x)).collect()
}

/// Rounded to 15 significant digits, as Excel keeps them, so 0.1 + 0.2 is 0.3.
fn tidy(v: f64) -> f64 {
    if v == 0.0 || !v.is_finite() {
        return v;
    }
    format!("{v:.14e}").parse().unwrap_or(v)
}

/// Dates on the same day of the month, the same number of months apart: carried on by months,
/// the day kept where the month has it (31 January, 28 February, 31 March).
fn by_months(values: &[f64], count: usize, date1904: bool) -> Option<Vec<f64>> {
    let dates: Vec<(i64, i64, i64, f64)> = values
        .iter()
        .map(|v| {
            let (y, m, d) = to_civil(v.floor() as i64, date1904)?;
            Some((y, m, d, v - v.floor()))
        })
        .collect::<Option<_>>()?;
    let day = dates[0].2;
    let month_of = |d: &(i64, i64, i64, f64)| d.0 * 12 + d.1 - 1;
    let step = month_of(&dates[1]) - month_of(&dates[0]);
    let same_day = dates.iter().all(|d| d.2 == day || d.2 == days_in_month(d.0, d.1).min(day));
    if step == 0 || !same_day || dates.windows(2).any(|w| month_of(&w[1]) - month_of(&w[0]) != step) {
        return None;
    }
    let (last, time) = (month_of(dates.last().unwrap()), dates[0].3);
    (1..=count as i64)
        .map(|k| {
            let m = last + step * k;
            let (y, m) = (m.div_euclid(12), m.rem_euclid(12) + 1);
            Some(from_civil(y, m, day.min(days_in_month(y, m)), date1904)? as f64 + time)
        })
        .collect()
}

fn days_in_month(y: i64, m: i64) -> i64 {
    match m {
        2 if (y % 4 == 0 && y % 100 != 0) || y % 400 == 0 => 29,
        2 => 28,
        4 | 6 | 9 | 11 => 30,
        _ => 31,
    }
}

/// Days since 1970-01-01 of a civil date, and back (Howard Hinnant's algorithms).
fn days_from_civil(y: i64, m: i64, d: i64) -> i64 {
    let y = if m <= 2 { y - 1 } else { y };
    let era = y.div_euclid(400);
    let yoe = y - era * 400;
    let doy = (153 * (if m > 2 { m - 3 } else { m + 9 }) + 2) / 5 + d - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    era * 146097 + doe - 719468
}

fn civil_from_days(z: i64) -> (i64, i64, i64) {
    let z = z + 719468;
    let era = z.div_euclid(146097);
    let doe = z - era * 146097;
    let yoe = (doe - doe / 1460 + doe / 36524 - doe / 146096) / 365;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = doy - (153 * mp + 2) / 5 + 1;
    let m = if mp < 10 { mp + 3 } else { mp - 9 };
    (yoe + era * 400 + i64::from(m <= 2), m, d)
}

/// Excel's serial of 1970-01-01 in each date system. The 1900 system's serials are only
/// continuous from 1 March 1900 (serial 61), past the 29 February 1900 it counts.
const EPOCH_1900: i64 = 25569;
const EPOCH_1904: i64 = 24107;

fn to_civil(serial: i64, date1904: bool) -> Option<(i64, i64, i64)> {
    if date1904 {
        (serial >= 0).then(|| civil_from_days(serial - EPOCH_1904))
    } else {
        (serial >= 61).then(|| civil_from_days(serial - EPOCH_1900))
    }
}

fn from_civil(y: i64, m: i64, d: i64, date1904: bool) -> Option<i64> {
    let serial = days_from_civil(y, m, d) + if date1904 { EPOCH_1904 } else { EPOCH_1900 };
    (serial >= if date1904 { 0 } else { 61 }).then_some(serial)
}

/// Text with a number in it: what is before and after its last run of digits.
#[derive(Clone, Debug, PartialEq)]
struct Numbered<'a> {
    before: &'a str,
    number: u64,
    /// Digits as written, so `007` carries on as `008`.
    width: usize,
    after: &'a str,
}

impl Numbered<'_> {
    fn parse(text: &str) -> Option<Numbered<'_>> {
        let end = text.char_indices().rev().find(|(_, c)| c.is_ascii_digit()).map(|(i, _)| i + 1)?;
        let start = text[..end].char_indices().rev().take_while(|(_, c)| c.is_ascii_digit()).last().map(|(i, _)| i)?;
        let digits = &text[start..end];
        if digits.len() > 15 {
            return None;
        }
        Some(Numbered { before: &text[..start], number: digits.parse().ok()?, width: if digits.starts_with('0') { digits.len() } else { 1 }, after: &text[end..] })
    }

    fn with(&self, number: u64) -> String {
        format!("{}{number:0width$}{}", self.before, self.after, width = self.width)
    }
}

/// Every cell text with a number, the same text around it.
fn numbered(source: &[Source]) -> Option<Vec<Numbered<'_>>> {
    let series: Vec<Numbered> = source
        .iter()
        .map(|s| match s {
            Source::Text(t) => Numbered::parse(t),
            _ => None,
        })
        .collect::<Option<_>>()?;
    let first = &series[0];
    series.iter().all(|n| n.before == first.before && n.after == first.after).then_some(series)
}

/// Excel's built-in lists, and the Chinese ones it has in a Chinese locale.
const LISTS: &[&[&str]] = &[
    &["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"],
    &["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"],
    &["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"],
    &["January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"],
    &["星期日", "星期一", "星期二", "星期三", "星期四", "星期五", "星期六"],
    &["周日", "周一", "周二", "周三", "周四", "周五", "周六"],
    &["一月", "二月", "三月", "四月", "五月", "六月", "七月", "八月", "九月", "十月", "十一月", "十二月"],
    &["第一季度", "第二季度", "第三季度", "第四季度"],
    &["第一季", "第二季", "第三季", "第四季"],
    &["甲", "乙", "丙", "丁", "戊", "己", "庚", "辛", "壬", "癸"],
    &["子", "丑", "寅", "卯", "辰", "巳", "午", "未", "申", "酉", "戌", "亥"],
];

/// How an English list item was written, kept as the list carries on.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Case {
    AsListed,
    Upper,
    Lower,
}

impl Case {
    fn of(text: &str) -> Self {
        if text.chars().any(|c| c.is_ascii_alphabetic()) && !text.chars().any(|c| c.is_ascii_lowercase()) && text.chars().count() > 1 {
            Case::Upper
        } else if !text.chars().any(|c| c.is_ascii_uppercase()) && text.chars().any(|c| c.is_ascii_lowercase()) {
            Case::Lower
        } else {
            Case::AsListed
        }
    }

    fn apply(self, item: &str) -> String {
        match self {
            Case::AsListed => item.to_string(),
            Case::Upper => item.to_uppercase(),
            Case::Lower => item.to_lowercase(),
        }
    }
}

/// Every cell an item of the same list: the list, each one's place in it, how the first is written.
fn listed(source: &[Source]) -> Option<(&'static [&'static str], Vec<i64>, Case)> {
    let texts: Vec<&str> = source
        .iter()
        .map(|s| match s {
            Source::Text(t) => Some(t.trim()),
            _ => None,
        })
        .collect::<Option<_>>()?;
    LISTS.iter().find_map(|list| {
        let at: Vec<i64> = texts.iter().map(|t| list.iter().position(|item| item.eq_ignore_ascii_case(t)).map(|i| i as i64)).collect::<Option<_>>()?;
        Some((*list, at, Case::of(texts[0])))
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    fn n(v: f64) -> Source {
        Source::Number { value: v, shown: Shown::Plain }
    }
    fn t(s: &str) -> Source {
        Source::Text(s.to_string())
    }
    fn numbers(f: Vec<Filled>) -> Vec<f64> {
        f.into_iter()
            .map(|x| match x {
                Filled::Number(v) => v,
                other => panic!("not a number: {other:?}"),
            })
            .collect()
    }
    fn texts(f: Vec<Filled>) -> Vec<String> {
        f.into_iter()
            .map(|x| match x {
                Filled::Text(v) => v,
                other => panic!("not text: {other:?}"),
            })
            .collect()
    }
    fn date(y: i64, m: i64, d: i64) -> f64 {
        from_civil(y, m, d, false).unwrap() as f64
    }

    #[test]
    fn numbers_carry_on_their_trend() {
        assert_eq!(numbers(extend(&[n(1.0), n(2.0), n(3.0), n(4.0)], 4, false, false)), vec![5.0, 6.0, 7.0, 8.0]);
        assert_eq!(numbers(extend(&[n(10.0), n(8.0)], 3, false, false)), vec![6.0, 4.0, 2.0]);
        assert_eq!(numbers(extend(&[n(0.1), n(0.2)], 2, false, false)), vec![0.3, 0.4]);
        // Not a straight line: Excel's least-squares trend.
        assert_eq!(numbers(extend(&[n(1.0), n(2.0), n(4.0)], 1, false, false)), vec![tidy(5.0 + 1.0 / 3.0)]);
    }

    #[test]
    fn a_single_number_is_copied_unless_switched() {
        assert_eq!(extend(&[n(7.0)], 3, false, false), vec![Filled::Copy(0); 3]);
        assert_eq!(numbers(extend(&[n(7.0)], 3, true, false)), vec![8.0, 9.0, 10.0]);
        // Switched, a series is repeated instead.
        assert_eq!(extend(&[n(1.0), n(2.0)], 3, true, false), vec![Filled::Copy(0), Filled::Copy(1), Filled::Copy(0)]);
    }

    #[test]
    fn dates_go_by_days_or_months() {
        let day = |y, m, d| Source::Number { value: date(y, m, d), shown: Shown::Date };
        assert_eq!(numbers(extend(&[day(2026, 2, 27)], 3, false, false)), vec![date(2026, 2, 28), date(2026, 3, 1), date(2026, 3, 2)]);
        assert_eq!(extend(&[day(2026, 2, 27)], 1, true, false), vec![Filled::Copy(0)]);
        assert_eq!(numbers(extend(&[day(2026, 1, 31), day(2026, 2, 28)], 2, false, false)), vec![date(2026, 3, 31), date(2026, 4, 30)]);
        assert_eq!(numbers(extend(&[day(2024, 12, 31), day(2025, 12, 31)], 1, false, false)), vec![date(2026, 12, 31)]);
        // A week apart: by days.
        assert_eq!(numbers(extend(&[day(2026, 1, 1), day(2026, 1, 8)], 1, false, false)), vec![date(2026, 1, 15)]);
        let time = Source::Number { value: 0.375, shown: Shown::Time };
        assert_eq!(numbers(extend(&[time], 1, false, false)), vec![tidy(0.375 + 1.0 / 24.0)]);
    }

    #[test]
    fn the_date_systems_agree_with_excel() {
        // 2026-01-01 is serial 46023 (1900 system), 44561 (1904).
        assert_eq!(from_civil(2026, 1, 1, false), Some(46023));
        assert_eq!(from_civil(2026, 1, 1, true), Some(44561));
        assert_eq!(to_civil(46023, false), Some((2026, 1, 1)));
        assert_eq!(to_civil(61, false), Some((1900, 3, 1)));
    }

    #[test]
    fn text_with_a_number_counts_on() {
        assert_eq!(texts(extend(&[t("Q1")], 3, false, false)), vec!["Q2", "Q3", "Q4"]);
        assert_eq!(texts(extend(&[t("第1期")], 2, false, false)), vec!["第2期", "第3期"]);
        assert_eq!(texts(extend(&[t("007")], 2, false, false)), vec!["008", "009"]);
        assert_eq!(texts(extend(&[t("FY2024"), t("FY2026")], 2, false, false)), vec!["FY2028", "FY2030"]);
        assert_eq!(extend(&[t("Q1")], 2, true, false), vec![Filled::Copy(0); 2]);
        // Different text around the number: repeated.
        assert_eq!(extend(&[t("Q1"), t("H1")], 2, false, false), vec![Filled::Copy(0), Filled::Copy(1)]);
    }

    #[test]
    fn lists_go_round() {
        assert_eq!(texts(extend(&[t("周五")], 3, false, false)), vec!["周六", "周日", "周一"]);
        assert_eq!(texts(extend(&[t("十一月")], 2, false, false)), vec!["十二月", "一月"]);
        assert_eq!(texts(extend(&[t("Jan"), t("Mar")], 2, false, false)), vec!["May", "Jul"]);
        assert_eq!(texts(extend(&[t("MON")], 2, false, false)), vec!["TUE", "WED"]);
        assert_eq!(texts(extend(&[t("第三季度")], 2, false, false)), vec!["第四季度", "第一季度"]);
        assert_eq!(extend(&[t("周一"), t("周三"), t("周四")], 1, false, false), vec![Filled::Copy(0)]);
    }

    #[test]
    fn anything_else_repeats() {
        let source = [t("收入"), Source::Empty, Source::Other];
        assert_eq!(extend(&source, 4, false, false), vec![Filled::Copy(0), Filled::Copy(1), Filled::Copy(2), Filled::Copy(0)]);
        assert_eq!(extend(&[n(1.0), t("a")], 2, false, false), vec![Filled::Copy(0), Filled::Copy(1)]);
    }
}
