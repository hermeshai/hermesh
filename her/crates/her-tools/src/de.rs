//! Lenient deserializers: models sometimes send numbers as strings.

use serde::Deserialize;
use serde::Deserializer;
use serde::de::Error;
use serde_json::Value;

pub fn opt_usize<'de, D: Deserializer<'de>>(deserializer: D) -> Result<Option<usize>, D::Error> {
    opt_u64(deserializer).map(|v| v.map(|n| n as usize))
}

pub fn opt_u64<'de, D: Deserializer<'de>>(deserializer: D) -> Result<Option<u64>, D::Error> {
    let value: Option<Value> = Option::deserialize(deserializer)?;
    match value {
        None | Some(Value::Null) => Ok(None),
        Some(Value::Number(n)) => n
            .as_u64()
            .or_else(|| n.as_f64().filter(|f| *f >= 0.0 && f.fract() == 0.0).map(|f| f as u64))
            .map(Some)
            .ok_or_else(|| D::Error::custom(format!("expected a non-negative integer, got {n}"))),
        Some(Value::String(s)) => s
            .trim()
            .parse::<u64>()
            .map(Some)
            .map_err(|_| D::Error::custom(format!("expected a non-negative integer, got \"{s}\""))),
        Some(other) => Err(D::Error::custom(format!("expected a non-negative integer, got {other}"))),
    }
}

#[cfg(test)]
mod tests {
    use serde::Deserialize;

    #[derive(Deserialize)]
    struct Args {
        #[serde(default, deserialize_with = "super::opt_usize")]
        n: Option<usize>,
    }

    #[test]
    fn accepts_numbers_and_numeric_strings() {
        let parse = |s: &str| serde_json::from_str::<Args>(s).map(|a| a.n);
        assert_eq!(parse(r#"{"n": 5}"#).unwrap(), Some(5));
        assert_eq!(parse(r#"{"n": "12"}"#).unwrap(), Some(12));
        assert_eq!(parse(r#"{"n": 3.0}"#).unwrap(), Some(3));
        assert_eq!(parse(r#"{}"#).unwrap(), None);
        assert_eq!(parse(r#"{"n": null}"#).unwrap(), None);
        assert!(parse(r#"{"n": -1}"#).is_err());
        assert!(parse(r#"{"n": "ten"}"#).is_err());
    }
}
