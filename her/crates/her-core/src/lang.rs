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
    fn the_first_set_value_decides() {
        let some = |text: &str| Some(text.to_string());
        assert_eq!(from_values([None, None]), Lang::En);
        assert_eq!(from_values([some("zh_CN.UTF-8")]), Lang::Zh);
        assert_eq!(from_values([some("en"), some("zh_CN.UTF-8")]), Lang::En, "HER_LANG comes first");
        assert_eq!(from_values([some(""), some("zh")]), Lang::Zh, "an empty value counts as unset");
        assert_eq!(from_values([some("C")]), Lang::En);
    }
}
