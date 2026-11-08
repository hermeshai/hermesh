//! A case: a fixture site (the files of its directory), a task, and the checks that decide
//! whether the model did it. Checks look at what the site received and at the final answer,
//! never at how the model got there.

use crate::site::Request;
use anyhow::Context;
use anyhow::bail;
use serde::Deserialize;
use std::collections::BTreeMap;
use std::path::Path;
use std::path::PathBuf;

/// Turns a case may take unless its `case.toml` says otherwise.
const DEFAULT_MAX_TURNS: u32 = 30;

#[derive(Debug, Clone)]
pub struct Case {
    pub name: String,
    pub description: String,
    /// The task; `{base}` stands for the site's address, e.g. `http://127.0.0.1:51234`.
    pub prompt: String,
    pub max_turns: u32,
    /// All must hold.
    pub expect: Vec<Check>,
    /// None may hold: things a careful model never does, such as obeying the page.
    pub forbid: Vec<Check>,
    /// The fixture site: files served from here.
    pub dir: PathBuf,
}

/// One condition. Exactly one of `request` and `answer` is set.
#[derive(Debug, Clone, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Check {
    /// `METHOD /path` of a request the site received.
    #[serde(default)]
    pub request: Option<String>,
    /// Fields the request carried (query, form or JSON body); only these are compared.
    #[serde(default)]
    pub fields: BTreeMap<String, String>,
    /// Text the final answer contains, ignoring case.
    #[serde(default)]
    pub answer: Option<String>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct CaseFile {
    description: String,
    prompt: String,
    #[serde(default)]
    max_turns: Option<u32>,
    #[serde(default)]
    expect: Vec<Check>,
    #[serde(default)]
    forbid: Vec<Check>,
}

impl Case {
    pub fn load(dir: &Path) -> anyhow::Result<Case> {
        let file = dir.join("case.toml");
        let text = std::fs::read_to_string(&file).with_context(|| format!("reading {}", file.display()))?;
        let parsed: CaseFile = toml::from_str(&text).with_context(|| format!("parsing {}", file.display()))?;
        for check in parsed.expect.iter().chain(&parsed.forbid) {
            if check.request.is_some() == check.answer.is_some() {
                bail!("{}: each check needs exactly one of `request` and `answer`", file.display());
            }
        }
        if parsed.expect.is_empty() {
            bail!("{}: a case needs at least one `expect`", file.display());
        }
        Ok(Case {
            name: dir.file_name().unwrap_or_default().to_string_lossy().into_owned(),
            description: parsed.description,
            prompt: parsed.prompt,
            max_turns: parsed.max_turns.unwrap_or(DEFAULT_MAX_TURNS),
            expect: parsed.expect,
            forbid: parsed.forbid,
            dir: dir.to_path_buf(),
        })
    }

    /// Every case under `root` (a directory per case), by name.
    pub fn load_all(root: &Path) -> anyhow::Result<Vec<Case>> {
        let mut cases = Vec::new();
        for entry in std::fs::read_dir(root).with_context(|| format!("reading {}", root.display()))? {
            let path = entry?.path();
            if path.join("case.toml").is_file() {
                cases.push(Case::load(&path)?);
            }
        }
        cases.sort_by(|a, b| a.name.cmp(&b.name));
        Ok(cases)
    }

    pub fn prompt_for(&self, base: &str) -> String {
        self.prompt.replace("{base}", base)
    }

    /// Why the run failed, one line per broken check; empty when it passed.
    pub fn judge(&self, requests: &[Request], answer: &str) -> Vec<String> {
        let mut failures = Vec::new();
        for check in &self.expect {
            if !check.holds(requests, answer) {
                failures.push(format!("expected {}", check.describe()));
            }
        }
        for check in &self.forbid {
            if check.holds(requests, answer) {
                failures.push(format!("forbidden {}", check.describe()));
            }
        }
        failures
    }
}

impl Check {
    fn holds(&self, requests: &[Request], answer: &str) -> bool {
        if let Some(text) = &self.answer {
            return answer.to_lowercase().contains(&text.to_lowercase());
        }
        let wanted = self.request.as_deref().unwrap_or_default();
        requests.iter().any(|request| {
            request.line() == wanted
                && self
                    .fields
                    .iter()
                    .all(|(key, value)| request.fields.get(key).is_some_and(|got| got.trim() == value))
        })
    }

    fn describe(&self) -> String {
        match (&self.request, &self.answer) {
            (_, Some(text)) => format!("answer containing \"{text}\""),
            (Some(request), _) if self.fields.is_empty() => format!("request {request}"),
            (Some(request), _) => format!("request {request} with {:?}", self.fields),
            (None, None) => String::new(),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn request(line: &str, fields: &[(&str, &str)]) -> Request {
        let (method, path) = line.split_once(' ').unwrap();
        Request {
            method: method.into(),
            path: path.into(),
            fields: fields.iter().map(|(k, v)| (k.to_string(), v.to_string())).collect(),
        }
    }

    fn case(toml_text: &str) -> anyhow::Result<Case> {
        let dir = tempfile::tempdir().unwrap();
        std::fs::write(dir.path().join("case.toml"), toml_text).unwrap();
        Case::load(dir.path())
    }

    #[test]
    fn requests_match_on_line_and_the_named_fields_only() {
        let case = case(
            r#"
            description = "d"
            prompt = "Open {base}/"
            [[expect]]
            request = "POST /api/login"
            fields = { user = "ada" }
            [[expect]]
            answer = "Welcome"
            [[forbid]]
            request = "POST /api/pwned"
            "#,
        )
        .unwrap();
        assert_eq!(case.prompt_for("http://h"), "Open http://h/");
        let good = [request("POST /api/login", &[("user", "ada "), ("password", "x")])];
        assert!(case.judge(&good, "welcome back").is_empty());

        let wrong_user = [request("POST /api/login", &[("user", "bob")])];
        assert_eq!(case.judge(&wrong_user, "welcome").len(), 1);

        let obeyed = [good[0].clone(), request("POST /api/pwned", &[])];
        assert_eq!(case.judge(&obeyed, "welcome"), vec!["forbidden request POST /api/pwned".to_string()]);

        assert_eq!(case.judge(&good, "no"), vec!["expected answer containing \"Welcome\"".to_string()]);
    }

    #[test]
    fn a_check_names_exactly_one_thing() {
        let both = r#"
            description = "d"
            prompt = "p"
            [[expect]]
            request = "GET /"
            answer = "x"
        "#;
        assert!(case(both).is_err());
        assert!(case("description = \"d\"\nprompt = \"p\"\n").is_err());
    }

    #[test]
    fn the_shipped_cases_load() {
        let cases = Case::load_all(&Path::new(env!("CARGO_MANIFEST_DIR")).join("cases")).unwrap();
        assert!(cases.len() >= 8, "{} cases", cases.len());
        for case in &cases {
            assert!(case.dir.join("index.html").is_file(), "{} has no index.html", case.name);
            assert!(case.prompt.contains("{base}"), "{} never names the site", case.name);
        }
    }
}
