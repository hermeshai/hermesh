//! The language of what her says to people: `HER_LANG`, then the locale. What goes to a model
//! is always English (standalone-cli-design D-520).

use std::sync::OnceLock;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Lang {
    En,
    Zh,
}

fn from_values(values: impl IntoIterator<Item = Option<String>>) -> Lang {
    let first = values.into_iter().flatten().find(|value| !value.is_empty());
    match first {
        Some(value) if value.to_ascii_lowercase().starts_with("zh") => Lang::Zh,
        _ => Lang::En,
    }
}

pub fn current() -> Lang {
    static LANG: OnceLock<Lang> = OnceLock::new();
    *LANG.get_or_init(|| {
        from_values(["HER_LANG", "LC_ALL", "LC_MESSAGES", "LANG"].map(|name| std::env::var(name).ok()))
    })
}

/// Large English counts use billions and trillions across all human-readable output.
pub fn format_tokens(n: u64, english: impl FnOnce(u64) -> String) -> String {
    tokens_in(current(), n, english)
}

fn tokens_in(language: Lang, n: u64, english: impl FnOnce(u64) -> String) -> String {
    match language {
        Lang::En if n < 999_950_000 => english(n),
        Lang::En => compact_tokens(n, &[(1_000_000_000, "B"), (1_000_000_000_000, "T")]),
        Lang::Zh => compact_tokens(n, &[(1000, "千"), (10_000, "万"), (100_000_000, "亿"), (1_000_000_000_000, "万亿")]),
    }
}

fn compact_tokens(n: u64, units: &[(u64, &str)]) -> String {
    if n < 1000 {
        return n.to_string();
    }
    let (mut scale, mut suffix) = units[0];
    for &(next, unit) in &units[1..] {
        if n < next - scale / 20 {
            break;
        }
        (scale, suffix) = (next, unit);
    }
    let step = scale / 10;
    let tenths = n / step + u64::from(n % step >= step / 2);
    if tenths.is_multiple_of(10) {
        format!("{}{suffix}", tenths / 10)
    } else {
        format!("{}.{}{suffix}", tenths / 10, tenths % 10)
    }
}

pub fn token_count(n: u64) -> String {
    format_tokens(n, |n| n.to_string())
}

/// The words in the current language.
pub fn say(en: &'static str, zh: &'static str) -> &'static str {
    match current() {
        Lang::En => en,
        Lang::Zh => zh,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn chinese_token_units_round_and_promote() {
        for (n, expected) in [(999, "999"), (1500, "1.5千"), (9949, "9.9千"), (9950, "1万"), (412_000, "41.2万"),
                              (99_999_499, "9999.9万"), (99_999_500, "1亿"), (1_000_000_000, "10亿"), (u64::MAX, "18446744.1万亿")] {
            assert_eq!(tokens_in(Lang::Zh, n, |_| unreachable!()), expected);
        }
    }

    #[test]
    fn english_token_counts_promote_to_billions_and_trillions() {
        for (n, expected) in [(999_950_000, "1B"), (1_500_000_000, "1.5B"), (999_949_999_999, "999.9B"),
                              (999_950_000_000, "1T"), (1_200_000_000_000, "1.2T"), (u64::MAX, "18446744.1T")] {
            assert_eq!(tokens_in(Lang::En, n, |_| unreachable!()), expected);
        }
    }

    #[test]
    fn the_first_set_value_decides() {
        let some = |text: &str| Some(text.to_string());
        assert_eq!(from_values([None, None]), Lang::En);
        assert_eq!(from_values([some("zh_CN.UTF-8")]), Lang::Zh);
        assert_eq!(from_values([some("en"), some("zh_CN.UTF-8")]), Lang::En, "HER_LANG comes first");
        assert_eq!(from_values([some(""), some("zh")]), Lang::Zh, "an empty value counts as unset");
        assert_eq!(from_values([some("C")]), Lang::En);
    }
}
