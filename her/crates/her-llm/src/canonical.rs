//! Canonical models (docs/kernel/custom-endpoint-design.md §6.2). The public catalog ties each
//! provider's name for a model to the model's id at its lab (`canonical_model_id`). From that come
//! an alias table, every name → its canonical model, and one row of facts per canonical model, so
//! a gateway's name for a model finds what the model can do. Both are compiled in and refreshed
//! with the catalog, like the providers' snapshots.

use serde::Deserialize;
use serde::Serialize;
use serde_json::Value;
use std::collections::BTreeMap;
use std::collections::BTreeSet;
use std::collections::HashMap;

use crate::public_catalog::PublicModel;
use crate::public_catalog::PublicProvider;
use crate::public_catalog::ReasoningOption;

/// File stem of the canonical models' rows, in the format of a provider's snapshot.
pub const CANONICAL: &str = "_canonical";
/// File stem of the alias table.
pub const ALIASES: &str = "_aliases";

pub(crate) const EMBEDDED_CANONICAL: &str = include_str!("../catalog/public/_canonical.json");
pub(crate) const EMBEDDED_ALIASES: &str = include_str!("../catalog/public/_aliases.json");

include!(concat!(env!("OUT_DIR"), "/alias-names.rs"));

/// The lab-level file next to the catalog: `…/api.json` → `…/models.json`.
pub fn lab_url(catalog_url: &str) -> Option<String> {
    catalog_url.strip_suffix("api.json").map(|base| format!("{base}models.json"))
}

/// The labs' own providers in the catalog, by the lab part of a canonical id: their rows describe
/// the model best, thinking controls included.
const LAB_PROVIDERS: &[(&str, &[&str])] = &[
    ("anthropic", &["anthropic"]),
    ("openai", &["openai"]),
    ("google", &["google"]),
    ("xai", &["xai"]),
    ("deepseek", &["deepseek"]),
    ("zhipuai", &["zhipuai", "zai"]),
    ("moonshotai", &["moonshotai", "moonshotai-cn"]),
    ("minimax", &["minimax", "minimax-cn"]),
    ("alibaba", &["alibaba", "alibaba-cn"]),
    ("xiaomi", &["xiaomi"]),
    ("mistral", &["mistral"]),
    ("tencent", &["tencent-tokenhub"]),
    ("bytedance-seed", &["volcengine"]),
    ("nvidia", &["nvidia"]),
    ("cohere", &["cohere"]),
    ("amazon", &["amazon-bedrock"]),
    ("stepfun", &["stepfun", "stepfun-ai"]),
    ("perplexity", &["perplexity"]),
    ("meta", &["meta"]),
    ("upstage", &["upstage"]),
    ("meituan", &["longcat"]),
];

/// The alias table as stored: names in lower case → canonical ids; `None` for a name that stands
/// for models that differ, which is not guessed.
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
pub struct AliasFile {
    pub provider: String,
    pub fetched_at: String,
    pub names: Vec<(String, Option<String>)>,
}

/// What projecting the catalog gives.
#[derive(Debug)]
pub struct Projected {
    pub canonical: PublicProvider,
    pub aliases: AliasFile,
}

fn valid(id: &str) -> bool {
    crate::discovery::valid_id(id)
}

/// A model that talks: not an embedding, image or speech model. Ones that refuse tools stay, so
/// that their rows keep them out of the menu (model-catalog-design §6.1) instead of leaving them
/// unknown.
fn talks(model: &PublicModel) -> bool {
    PublicModel { tool_call: None, ..model.clone() }.is_chat()
}

/// `owner/repo` of the Hugging Face links of a lab entry: self-hosted servers name a model so.
fn hugging_face_repos(raw: &Value) -> Vec<String> {
    let Some(weights) = raw.get("weights").and_then(Value::as_array) else {
        return Vec::new();
    };
    weights
        .iter()
        .filter_map(|w| w.get("url").and_then(Value::as_str).or_else(|| w.as_str()))
        .filter_map(|url| {
            let rest = url.strip_prefix("https://huggingface.co/").or_else(|| url.strip_prefix("http://huggingface.co/"))?;
            let mut parts = rest.split(['/', '?', '#']);
            let (owner, repo) = (parts.next()?, parts.next()?);
            let id = format!("{owner}/{repo}");
            (!owner.is_empty() && !repo.is_empty() && valid(&id)).then_some(id)
        })
        .collect()
}

fn close(a: Option<u32>, b: Option<u32>) -> bool {
    match (a, b) {
        (Some(a), Some(b)) => a.abs_diff(b) as f64 <= 0.06 * f64::from(a.max(b)),
        _ => true,
    }
}

/// Of several canonical models one name stands for: the shortest id when they are one model for
/// what it can do (dated snapshots, usually), else none.
fn agreeing(ids: &[&String], facts: &BTreeMap<String, PublicModel>) -> Option<String> {
    let rows: Vec<&PublicModel> = ids.iter().filter_map(|id| facts.get(*id)).collect();
    let first = rows.first()?;
    let same = rows.iter().all(|r| close(r.context_window, first.context_window) && r.reasoning == first.reasoning);
    same.then(|| ids.iter().min_by_key(|id| (id.len(), id.as_str())).map(|id| (*id).clone())).flatten()
}

/// The option list more than half of the rows that name one agree on; empty otherwise. Hosts
/// translate thinking for their own APIs, so a plurality is no evidence of how the model is told
/// (model-catalog-design D-334: a row that names no control offers no levels).
fn agreed_options<'a>(lists: impl Iterator<Item = &'a Vec<ReasoningOption>>) -> Vec<ReasoningOption> {
    let mut counts: Vec<(&Vec<ReasoningOption>, usize)> = Vec::new();
    let mut total = 0;
    for list in lists.filter(|l| !l.is_empty()) {
        total += 1;
        match counts.iter_mut().find(|(l, _)| *l == list) {
            Some((_, n)) => *n += 1,
            None => counts.push((list, 1)),
        }
    }
    counts.into_iter().find(|(_, n)| *n * 2 > total).map(|(l, _)| l.clone()).unwrap_or_default()
}

fn median_low(mut values: Vec<u32>) -> Option<u32> {
    values.sort_unstable();
    (!values.is_empty()).then(|| values[(values.len() - 1) / 2])
}

fn majority(values: impl Iterator<Item = Option<bool>>) -> Option<bool> {
    let known: Vec<bool> = values.flatten().collect();
    (!known.is_empty()).then(|| known.iter().filter(|v| **v).count() * 2 > known.len())
}

/// What hosts of an open model agree on: yes/no by majority, limits by the lower median.
fn consensus(rows: &[(String, PublicModel)]) -> PublicModel {
    let models: Vec<&PublicModel> = rows.iter().map(|(_, m)| m).collect();
    let mut out = models[0].clone();
    out.reasoning = majority(models.iter().map(|m| m.reasoning));
    out.tool_call = majority(models.iter().map(|m| m.tool_call));
    let images = majority(models.iter().map(|m| (!m.input.is_empty()).then(|| m.input.iter().any(|i| i == "image"))));
    if let Some(images) = images {
        out.input = if images { vec!["text".into(), "image".into()] } else { vec!["text".into()] };
    }
    out.context_window = median_low(models.iter().filter_map(|m| m.context_window).collect());
    out.max_output_tokens = median_low(models.iter().filter_map(|m| m.max_output_tokens).collect());
    out.reasoning_options = agreed_options(models.iter().map(|m| &m.reasoning_options));
    out
}

/// The facts of a canonical model: its lab's own row, else the lab-level entry (with the thinking
/// controls most hosts name), else what the hosts agree on.
fn facts_of(id: &str, rows: &[(String, PublicModel)], lab: Option<&PublicModel>) -> Option<PublicModel> {
    let (lab_name, bare) = id.split_once('/').unwrap_or((id, id));
    let owners = LAB_PROVIDERS.iter().find(|(l, _)| *l == lab_name).map(|(_, p)| *p).unwrap_or(&[]);
    let own: Vec<&PublicModel> = rows.iter().filter(|(p, _)| owners.contains(&p.as_str())).map(|(_, m)| m).collect();
    // The row named like the model, then one still offered, then the newest: a lab's older names
    // may point at the model it became (DeepSeek's vision preview names its 1017 release).
    let best = own.iter().min_by_key(|m| (!m.id.eq_ignore_ascii_case(bare), m.deprecated, std::cmp::Reverse(m.released.clone())));
    let mut row = if let Some(m) = best {
        (*m).clone()
    } else if let Some(lab) = lab {
        let mut m = lab.clone();
        if m.reasoning == Some(true) {
            m.reasoning_options = agreed_options(rows.iter().map(|(_, r)| &r.reasoning_options));
        }
        m
    } else if !rows.is_empty() {
        consensus(rows)
    } else {
        return None;
    };
    row.id = id.to_string();
    row.canonical = None;
    // What a provider charges and retires is its own, never the model's.
    row.cost = None;
    row.cost_tiers = Vec::new();
    row.deprecated = false;
    Some(row)
}

/// Projects the catalog (and, when given, its lab-level file) to the canonical rows and the alias
/// table. Rows that do not project are left out; a catalog without canonical models fails.
pub fn project(api: &Value, lab: Option<&Value>, fetched_at: &str) -> Result<Projected, String> {
    let root = api.as_object().ok_or("the catalog is not a JSON object")?;
    let mut rows: BTreeMap<String, Vec<(String, PublicModel)>> = BTreeMap::new();
    let mut names: BTreeMap<String, BTreeSet<String>> = BTreeMap::new();
    for (provider, entry) in root {
        let Some(models) = entry.get("models").and_then(Value::as_object) else { continue };
        for (key, raw) in models {
            let Some(canonical) = raw.get("canonical_model_id").and_then(Value::as_str).filter(|c| valid(c) && c.contains('/'))
            else {
                continue;
            };
            let Ok(model) = crate::public_catalog::project_model(key, raw) else { continue };
            if !talks(&model) {
                continue;
            }
            names.entry(model.id.to_ascii_lowercase()).or_default().insert(canonical.to_string());
            rows.entry(canonical.to_string()).or_default().push((provider.clone(), model));
        }
    }
    if rows.is_empty() {
        return Err("the catalog names no canonical models".into());
    }
    // The same rows in the same order however serde_json keeps an object's keys.
    for list in rows.values_mut() {
        list.sort_by(|(pa, a), (pb, b)| (pa, &a.id).cmp(&(pb, &b.id)));
    }
    let mut labs: BTreeMap<String, PublicModel> = BTreeMap::new();
    if let Some(lab) = lab.and_then(Value::as_object) {
        for (id, raw) in lab {
            if !valid(id) || !id.contains('/') {
                continue;
            }
            let Ok(model) = crate::public_catalog::project_model(id, raw) else { continue };
            if !talks(&model) {
                continue;
            }
            for repo in hugging_face_repos(raw) {
                names.entry(repo.to_ascii_lowercase()).or_default().insert(id.clone());
            }
            labs.insert(id.clone(), model);
        }
    }
    let ids: BTreeSet<&String> = rows.keys().chain(labs.keys()).collect();
    let facts: BTreeMap<String, PublicModel> = ids
        .into_iter()
        .filter_map(|id| facts_of(id, rows.get(id).map(Vec::as_slice).unwrap_or(&[]), labs.get(id)).map(|f| (id.clone(), f)))
        .collect();
    let mut table: BTreeMap<String, Option<String>> = BTreeMap::new();
    for (name, canons) in &names {
        let canons: Vec<&String> = canons.iter().filter(|c| facts.contains_key(*c)).collect();
        let pick = match canons.as_slice() {
            [] => continue,
            [one] => Some((*one).clone()),
            many => agreeing(many, &facts),
        };
        table.insert(name.clone(), pick);
    }
    // A canonical id names its model, and so does its bare name when no other lab uses it.
    let mut bare: BTreeMap<String, BTreeSet<&String>> = BTreeMap::new();
    for id in facts.keys() {
        table.entry(id.to_ascii_lowercase()).or_insert_with(|| Some(id.clone()));
        if let Some((_, b)) = id.split_once('/') {
            bare.entry(b.to_ascii_lowercase()).or_default().insert(id);
        }
    }
    for (b, ids) in bare {
        if let [only] = ids.iter().collect::<Vec<_>>().as_slice() {
            table.entry(b).or_insert_with(|| Some((**only).clone()));
        }
    }
    Ok(Projected {
        canonical: PublicProvider {
            provider: CANONICAL.to_string(),
            fetched_at: fetched_at.to_string(),
            models: facts.into_values().collect(),
        },
        aliases: AliasFile { provider: ALIASES.to_string(), fetched_at: fetched_at.to_string(), names: table.into_iter().collect() },
    })
}

/// The alias file: a header, then one name per line, so diffs stay readable.
pub fn to_alias_file(file: &AliasFile) -> String {
    let mut out = format!(
        "{{\"provider\":{},\"fetched_at\":{},\"names\":[\n",
        serde_json::to_string(&file.provider).unwrap_or_default(),
        serde_json::to_string(&file.fetched_at).unwrap_or_default()
    );
    for (i, entry) in file.names.iter().enumerate() {
        out.push_str(&serde_json::to_string(entry).unwrap_or_default());
        out.push_str(if i + 1 < file.names.len() { ",\n" } else { "\n" });
    }
    out.push_str("]}\n");
    out
}

/// A name's canonical model.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Match {
    pub canonical: String,
    /// Only after dropping a date or a decoration (step 12 of §6.3): it lends less.
    pub guessed: bool,
}

/// The alias table, ready to look names up in.
#[derive(Debug, Default)]
pub struct AliasTable {
    names: HashMap<String, Option<String>>,
    /// Squashed names (step 11) that lead to one model only.
    squash: HashMap<String, String>,
}

impl AliasTable {
    pub fn parse(text: &str) -> Option<Self> {
        let file: AliasFile = serde_json::from_str(text).ok()?;
        Some(Self::from_file(file))
    }

    pub fn from_file(file: AliasFile) -> Self {
        let mut squash: HashMap<String, Option<String>> = HashMap::new();
        for (name, canonical) in &file.names {
            let Some(canonical) = canonical else { continue };
            let key = crate::names::squash(name);
            if key.is_empty() {
                continue;
            }
            match squash.get_mut(&key) {
                None => {
                    squash.insert(key, Some(canonical.clone()));
                }
                Some(found) if found.as_deref() != Some(canonical.as_str()) => *found = None,
                Some(_) => {}
            }
        }
        Self {
            names: file.names.into_iter().collect(),
            squash: squash.into_iter().filter_map(|(k, v)| v.map(|v| (k, v))).collect(),
        }
    }

    /// The canonical model `names` stand for, the first name first. A name the table marks as
    /// standing for different models stops the search: it is not guessed.
    pub fn find(&self, names: &[&str]) -> Option<Match> {
        for name in names {
            for v in crate::names::variants(name) {
                match self.names.get(&v) {
                    Some(Some(c)) => return Some(Match { canonical: c.clone(), guessed: false }),
                    Some(None) => return None,
                    None => {}
                }
            }
        }
        for name in names {
            let key = crate::names::squash(name);
            if let Some(c) = self.squash.get(&key).filter(|_| !key.is_empty()) {
                return Some(Match { canonical: c.clone(), guessed: false });
            }
        }
        for name in names {
            for v in crate::names::loose(name) {
                if let Some(Some(c)) = self.names.get(&v) {
                    return Some(Match { canonical: c.clone(), guessed: true });
                }
            }
        }
        None
    }
}

/// Whether the compiled-in table may know any of `names`: most names it does not are told without
/// reading it. A chance match only means the table is read for nothing.
pub fn compiled_may_know(names: &[&str]) -> bool {
    let known = |n: &str| ALIAS_NAME_HASHES.binary_search(&crate::public_catalog::model_name_hash(n)).is_ok();
    names.iter().any(|name| {
        crate::names::variants(name).iter().any(|v| known(v))
            || known(&crate::names::squash(name))
            || crate::names::loose(name).iter().any(|v| known(v))
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn catalog() -> Value {
        json!({
            "anthropic": {"models": {
                "claude-sonnet-4-5": {"canonical_model_id": "anthropic/claude-sonnet-4-5", "name": "Claude Sonnet 4.5",
                    "reasoning": true, "tool_call": true, "reasoning_options": [{"type": "budget_tokens", "min": 1024}],
                    "modalities": {"input": ["text", "image"], "output": ["text"]}, "limit": {"context": 200000, "output": 64000},
                    "cost": {"input": 3, "output": 15}}
            }},
            "amazon-bedrock": {"models": {
                "us.anthropic.claude-sonnet-4-5-20250929-v1:0": {"canonical_model_id": "anthropic/claude-sonnet-4-5",
                    "limit": {"context": 200000, "output": 64000}}
            }},
            "groq": {"models": {
                "llama-3.3-70b-versatile": {"canonical_model_id": "meta/llama-3.3-70b-instruct", "reasoning": false,
                    "limit": {"context": 131072, "output": 32768}}
            }},
            "together": {"models": {
                "meta-llama/Llama-3.3-70B-Instruct-Turbo": {"canonical_model_id": "meta/llama-3.3-70b-instruct",
                    "reasoning": false, "limit": {"context": 131072, "output": 8192}},
                "auto": {"limit": {"context": 1000000}},
                "embed": {"canonical_model_id": "x/embed", "type": "embedding", "limit": {"context": 8192}}
            }},
            "a": {"models": {"shared": {"canonical_model_id": "lab1/one", "reasoning": true, "limit": {"context": 1000000}}}},
            "b": {"models": {"shared": {"canonical_model_id": "lab2/two", "reasoning": false, "limit": {"context": 32000}}}}
        })
    }

    fn lab() -> Value {
        json!({
            "meta/llama-3.3-70b-instruct": {"name": "Llama 3.3 70B Instruct", "reasoning": false, "tool_call": true,
                "modalities": {"input": ["text"], "output": ["text"]}, "limit": {"context": 128000, "output": 4096},
                "weights": [{"label": "Hugging Face", "url": "https://huggingface.co/meta-llama/Llama-3.3-70B-Instruct"}]}
        })
    }

    #[test]
    fn names_meet_their_canonical_models() {
        let p = project(&catalog(), Some(&lab()), "2026-11-10T00:00:00Z").unwrap();
        let ids: Vec<&str> = p.canonical.models.iter().map(|m| m.id.as_str()).collect();
        assert_eq!(ids, ["anthropic/claude-sonnet-4-5", "lab1/one", "lab2/two", "meta/llama-3.3-70b-instruct"]);
        let sonnet = p.canonical.model("anthropic/claude-sonnet-4-5").unwrap();
        assert_eq!(sonnet.name.as_deref(), Some("Claude Sonnet 4.5"), "the lab's own row");
        assert!(sonnet.cost.is_none(), "a provider's price is not the model's");
        let llama = p.canonical.model("meta/llama-3.3-70b-instruct").unwrap();
        assert_eq!(llama.context_window, Some(128_000), "the lab-level entry, not the hosts'");
        let table = AliasTable::from_file(p.aliases);
        let find = |n: &str| table.find(&[n]).map(|m| (m.canonical, m.guessed));
        assert_eq!(find("us.anthropic.claude-sonnet-4-5-20250929-v1:0"), Some(("anthropic/claude-sonnet-4-5".into(), false)));
        assert_eq!(find("anthropic/claude-sonnet-4.5"), Some(("anthropic/claude-sonnet-4-5".into(), false)));
        assert_eq!(find("claude-sonnet-4-5-thinking"), Some(("anthropic/claude-sonnet-4-5".into(), true)));
        assert_eq!(find("meta-llama/Llama-3.3-70B-Instruct"), Some(("meta/llama-3.3-70b-instruct".into(), false)), "a repo name");
        assert_eq!(find("claude-sonnet4-5"), Some(("anthropic/claude-sonnet-4-5".into(), false)), "squashed");
        assert_eq!(find("auto"), None, "no canonical model: not borrowed across providers");
        assert_eq!(find("embed"), None, "not a chat model");
        assert_eq!(find("shared"), None, "two models that differ: not guessed");
    }

    #[test]
    fn a_labs_renamed_model_takes_the_row_still_offered() {
        let catalog = json!({
            "deepseek": {"models": {
                "deepseek-a-vision-exp": {"canonical_model_id": "deepseek/deepseek-v9-flash-1017", "name": "DeepSeek Vision Exp", "status": "deprecated",
                    "release_date": "2026-09-01", "tool_call": true, "limit": {"context": 1000000}},
                "deepseek-flash": {"canonical_model_id": "deepseek/deepseek-v9-flash-1017", "name": "DeepSeek V9 Flash 1017",
                    "release_date": "2026-10-17", "tool_call": true, "limit": {"context": 1000000}}
            }}
        });
        let p = project(&catalog, None, "t").unwrap();
        let row = p.canonical.model("deepseek/deepseek-v9-flash-1017").unwrap();
        assert_eq!(row.name.as_deref(), Some("DeepSeek V9 Flash 1017"));
    }

    #[test]
    fn the_alias_file_reads_back() {
        let p = project(&catalog(), None, "t").unwrap();
        let text = to_alias_file(&p.aliases);
        assert!(text.lines().count() > 3, "one name per line");
        let table = AliasTable::parse(&text).unwrap();
        assert!(table.find(&["llama-3.3-70b-versatile"]).is_some());
        assert!(project(&json!({"x": {"models": {}}}), None, "t").is_err(), "no canonical models");
        assert_eq!(lab_url("https://models.dev/api.json").as_deref(), Some("https://models.dev/models.json"));
    }
}
