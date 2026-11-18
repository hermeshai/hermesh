//! The person's own services in `models.toml` (docs/kernel/custom-endpoint-design.md §8): adding,
//! changing and removing them, and saying what a model is like (§7.3). One implementation for
//! the protocol service and the command line. Writes keep the person's comments and order.

use her_llm::model::AuthHeader;
use her_llm::probe::ProbeReport;

use crate::paths::HerHome;

/// A service as models.toml holds it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ServiceEntry {
    pub id: String,
    pub name: String,
    /// The default protocol and its prefix.
    pub api: String,
    pub base_url: String,
    pub auth_header: Option<AuthHeader>,
    /// The other protocols it speaks: api, prefix, header.
    pub endpoints: Vec<(String, String, Option<AuthHeader>)>,
    pub no_key: bool,
    /// It lists its models.
    pub list: bool,
    /// A local server's details endpoint: `ollama`, `lmstudio`, `llamacpp`.
    pub list_details: Option<String>,
    /// Models named by hand, for a service without a list.
    pub models: Vec<String>,
}

/// The header a protocol takes a key in when nothing says otherwise.
pub fn usual_header(api: &str) -> AuthHeader {
    if api == "anthropic-messages" { AuthHeader::XApiKey } else { AuthHeader::Bearer }
}

pub fn header_name(header: AuthHeader) -> &'static str {
    match header {
        AuthHeader::Bearer => "bearer",
        AuthHeader::XApiKey => "x_api_key",
        AuthHeader::ApiKey => "api_key",
    }
}

impl ServiceEntry {
    /// The entry a probe's findings make: Chat Completions as the default protocol when the
    /// service speaks it (§5). `None` when nothing was found to talk to.
    pub fn from_probe(id: &str, name: &str, report: &ProbeReport, no_key: bool, models: &[String]) -> Option<Self> {
        let default = report.protocols.first()?;
        Some(Self {
            id: id.to_string(),
            name: name.to_string(),
            api: default.api.clone(),
            base_url: default.base_url.clone(),
            auth_header: default.auth_header.filter(|h| *h != usual_header(&default.api)),
            endpoints: report.protocols[1..]
                .iter()
                .map(|p| (p.api.clone(), p.base_url.clone(), p.auth_header.filter(|h| *h != usual_header(&p.api))))
                .collect(),
            no_key,
            list: report.list.is_some(),
            list_details: report.details.clone(),
            models: models.to_vec(),
        })
    }
}

fn models_document(home: &HerHome) -> Result<(std::path::PathBuf, toml_edit::DocumentMut), String> {
    let path = home.models_file();
    let text = match std::fs::read_to_string(&path) {
        Ok(text) => text,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => String::new(),
        Err(e) => return Err(format!("cannot read {}: {e}", path.display())),
    };
    let document = text.parse::<toml_edit::DocumentMut>().map_err(|e| format!("cannot read {}: {e}", path.display()))?;
    Ok((path, document))
}

fn save(path: &std::path::Path, document: &toml_edit::DocumentMut) -> Result<(), String> {
    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent).map_err(|e| format!("cannot create {}: {e}", parent.display()))?;
    }
    crate::config_edit::replace_file(path, document.to_string().as_bytes()).map_err(|e| format!("cannot write {}: {e}", path.display()))
}

fn providers_table(document: &mut toml_edit::DocumentMut) -> Result<&mut toml_edit::Table, String> {
    let providers = document.entry("providers").or_insert(toml_edit::table());
    let providers = providers.as_table_mut().ok_or("`providers` in models.toml is not a table")?;
    providers.set_implicit(true);
    Ok(providers)
}

/// Writes a service, replacing its settings and keeping the models already written for it.
pub fn write_service(home: &HerHome, entry: &ServiceEntry) -> Result<(), String> {
    let path = home.models_file();
    let _lock = crate::config_edit::lock(&path, home.root()).map_err(|e| e.to_string())?;
    let (path, mut document) = models_document(home)?;
    let providers = providers_table(&mut document)?;
    let kept = providers.get(&entry.id).and_then(|p| p.get("models")).and_then(|m| m.as_array_of_tables()).cloned();
    let mut table = toml_edit::Table::new();
    table["name"] = toml_edit::value(entry.name.as_str());
    table["api"] = toml_edit::value(entry.api.as_str());
    table["base_url"] = toml_edit::value(entry.base_url.as_str());
    if entry.no_key {
        table["auth"] = toml_edit::value("none");
    }
    if !entry.endpoints.is_empty() {
        let mut endpoints = toml_edit::InlineTable::new();
        for (api, base_url, header) in &entry.endpoints {
            let value: toml_edit::Value = match header {
                None => base_url.as_str().into(),
                Some(header) => {
                    let mut full = toml_edit::InlineTable::new();
                    full.insert("base_url", base_url.as_str().into());
                    full.insert("auth_header", header_name(*header).into());
                    full.into()
                }
            };
            endpoints.insert(api, value);
        }
        table["endpoints"] = toml_edit::value(endpoints);
    }
    if let Some(header) = entry.auth_header {
        let mut compat = toml_edit::InlineTable::new();
        compat.insert("auth_header", header_name(header).into());
        table["compat"] = toml_edit::value(compat);
    }
    if !entry.list {
        table["list_models"] = toml_edit::value(false);
    }
    if let Some(details) = &entry.list_details {
        table["list_details"] = toml_edit::value(details.as_str());
    }
    let mut models = kept.unwrap_or_default();
    for id in &entry.models {
        if !models.iter().any(|m| m.get("id").and_then(|v| v.as_str()) == Some(id.as_str())) {
            let mut model = toml_edit::Table::new();
            model["id"] = toml_edit::value(id.as_str());
            models.push(model);
        }
    }
    if !models.is_empty() {
        table["models"] = toml_edit::Item::ArrayOfTables(models);
    }
    providers[&entry.id] = toml_edit::Item::Table(table);
    save(&path, &document)
}

/// Renames a service without touching anything else.
pub fn rename_service(home: &HerHome, id: &str, name: &str) -> Result<(), String> {
    let path = home.models_file();
    let _lock = crate::config_edit::lock(&path, home.root()).map_err(|e| e.to_string())?;
    let (path, mut document) = models_document(home)?;
    let providers = providers_table(&mut document)?;
    let table = providers.get_mut(id).and_then(|p| p.as_table_mut()).ok_or_else(|| format!("{id} is not in models.toml"))?;
    table["name"] = toml_edit::value(name);
    save(&path, &document)
}

/// Removes a service's settings. Returns whether there were any.
pub fn remove_service(home: &HerHome, id: &str) -> Result<bool, String> {
    let path = home.models_file();
    let _lock = crate::config_edit::lock(&path, home.root()).map_err(|e| e.to_string())?;
    let (path, mut document) = models_document(home)?;
    let removed = providers_table(&mut document)?.remove(id).is_some();
    if removed {
        save(&path, &document)?;
    }
    Ok(removed)
}

/// What a model is like (§7.3). `None`: leave as it is; `Some(None)`: remove.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct ModelPatch {
    pub canonical: Option<Option<String>>,
    pub context_window: Option<Option<u64>>,
    pub images: Option<Option<bool>>,
    pub reasoning: Option<Option<bool>>,
    /// Remove the model's entry: it is found by its name again.
    pub clear: bool,
}

/// Writes what a model of a service is like. An entry left with only its id goes.
pub fn configure_model(home: &HerHome, provider: &str, model: &str, patch: &ModelPatch) -> Result<(), String> {
    let path = home.models_file();
    let _lock = crate::config_edit::lock(&path, home.root()).map_err(|e| e.to_string())?;
    let (path, mut document) = models_document(home)?;
    let providers = providers_table(&mut document)?;
    let table = providers.get_mut(provider).and_then(|p| p.as_table_mut()).ok_or_else(|| format!("{provider} is not in models.toml"))?;
    let models = table.entry("models").or_insert(toml_edit::Item::ArrayOfTables(toml_edit::ArrayOfTables::new()));
    let models = models.as_array_of_tables_mut().ok_or("`models` is not a list of tables")?;
    let found = models.iter().position(|m| m.get("id").and_then(|v| v.as_str()) == Some(model));
    let index = match found {
        Some(index) => index,
        None => {
            let mut entry = toml_edit::Table::new();
            entry["id"] = toml_edit::value(model);
            models.push(entry);
            models.len() - 1
        }
    };
    let entry = models.get_mut(index).expect("the entry just found");
    if patch.clear {
        entry.clear();
        entry["id"] = toml_edit::value(model);
    }
    let set = |entry: &mut toml_edit::Table, key: &str, value: Option<toml_edit::Item>| match value {
        Some(item) => {
            entry[key] = item;
        }
        None => {
            entry.remove(key);
        }
    };
    if let Some(canonical) = &patch.canonical {
        set(entry, "canonical", canonical.as_deref().map(toml_edit::value));
    }
    if let Some(window) = patch.context_window {
        let window = window.and_then(|w| i64::try_from(w).ok()).filter(|w| *w > 0);
        set(entry, "context_window", window.map(toml_edit::value));
    }
    if let Some(images) = patch.images {
        set(
            entry,
            "input",
            images.map(|images| {
                let mut input = toml_edit::Array::new();
                input.push("text");
                if images {
                    input.push("image");
                }
                toml_edit::value(input)
            }),
        );
    }
    if let Some(reasoning) = patch.reasoning {
        set(entry, "reasoning", reasoning.map(toml_edit::value));
    }
    if entry.iter().count() <= 1 {
        models.remove(index);
    }
    if models.is_empty() {
        table.remove("models");
    }
    save(&path, &document)
}

/// An id for a new service: from its name when that makes one, else from its host
/// (`llm.acme.com` → `acme`), never `local-…` (account connections), unique by `-2`, `-3`.
pub fn new_id(name: Option<&str>, host: &str, taken: impl Fn(&str) -> bool) -> String {
    let slug = |s: &str| -> String {
        let mut out = String::new();
        for c in s.chars() {
            if c.is_ascii_alphanumeric() {
                out.push(c.to_ascii_lowercase());
            } else if !out.ends_with('-') && !out.is_empty() {
                out.push('-');
            }
        }
        out.trim_matches('-').chars().take(32).collect::<String>().trim_matches('-').to_string()
    };
    let from_host = || {
        let host = host.trim_start_matches('[').trim_end_matches(']');
        let labels: Vec<&str> = host.split('.').filter(|l| !matches!(*l, "www" | "api" | "llm" | "ai" | "gateway" | "gw")).collect();
        let picked = if host.parse::<std::net::IpAddr>().is_ok() {
            host.to_string()
        } else {
            match labels.len() {
                0 => host.to_string(),
                1 => labels[0].to_string(),
                n => labels[n - 2].to_string(),
            }
        };
        slug(&picked)
    };
    let mut base = name.map(slug).filter(|s| !s.is_empty()).unwrap_or_else(from_host);
    if base.is_empty() {
        base = "custom".into();
    }
    if base.starts_with("local-") {
        base = format!("custom-{base}");
    }
    let mut id = base.clone();
    let mut n = 2;
    while taken(&id) {
        id = format!("{base}-{n}");
        n += 1;
    }
    id
}

#[cfg(test)]
mod tests {
    use super::*;
    use her_llm::probe::FoundProtocol;

    fn home() -> (tempfile::TempDir, HerHome) {
        let dir = tempfile::tempdir().unwrap();
        let home = HerHome::new(dir.path().to_path_buf());
        (dir, home)
    }

    #[test]
    fn a_probe_becomes_an_entry_that_reads_back() {
        let (_dir, home) = home();
        std::fs::write(home.models_file(), "# my own notes\n[providers.deepseek]\nbase_url = \"http://proxy.local/v1\"\n").unwrap();
        let report = ProbeReport {
            base_url: "https://llm.acme.example".into(),
            protocols: vec![
                FoundProtocol { api: "openai-chat".into(), base_url: "https://llm.acme.example/v1".into(), auth_header: Some(AuthHeader::Bearer) },
                FoundProtocol { api: "anthropic-messages".into(), base_url: "https://llm.acme.example".into(), auth_header: Some(AuthHeader::Bearer) },
            ],
            ..Default::default()
        };
        let entry = ServiceEntry::from_probe("acme", "Acme 网关", &report, false, &["prod-gpt".to_string()]).unwrap();
        assert_eq!(entry.auth_header, None, "the usual header is not written");
        assert_eq!(entry.endpoints[0].2, Some(AuthHeader::Bearer), "an unusual one is");
        write_service(&home, &entry).unwrap();
        let text = std::fs::read_to_string(home.models_file()).unwrap();
        assert!(text.starts_with("# my own notes"), "comments stay: {text}");
        assert!(text.contains("list_models = false"));
        let catalog = her_llm::Catalog::load(Some(&home.models_file())).unwrap();
        let acme = &catalog.providers["acme"];
        assert!(acme.custom);
        assert_eq!(acme.endpoints["anthropic-messages"].auth_header, Some(AuthHeader::Bearer));
        assert!(acme.entry("prod-gpt").is_some());
        assert_eq!(catalog.providers["deepseek"].base_url, "http://proxy.local/v1");

        configure_model(&home, "acme", "prod-gpt", &ModelPatch { canonical: Some(Some("openai/gpt-4o".into())), context_window: Some(Some(64_000)), ..Default::default() }).unwrap();
        // Rewriting the service keeps what was said about its models.
        write_service(&home, &ServiceEntry { list: true, models: vec![], ..entry.clone() }).unwrap();
        let catalog = her_llm::Catalog::load(Some(&home.models_file())).unwrap();
        let model = catalog.providers["acme"].entry("prod-gpt").unwrap().clone();
        assert_eq!((model.canonical.as_deref(), model.context_window), (Some("openai/gpt-4o"), Some(64_000)));
        configure_model(&home, "acme", "prod-gpt", &ModelPatch { images: Some(Some(true)), ..Default::default() }).unwrap();
        configure_model(&home, "acme", "prod-gpt", &ModelPatch { clear: true, ..Default::default() }).unwrap();
        let catalog = her_llm::Catalog::load(Some(&home.models_file())).unwrap();
        assert!(catalog.providers["acme"].entry("prod-gpt").is_none(), "an entry with only its id goes");
        rename_service(&home, "acme", "Acme").unwrap();
        assert!(remove_service(&home, "acme").unwrap());
        assert!(!remove_service(&home, "acme").unwrap());
        assert!(her_llm::Catalog::load(Some(&home.models_file())).unwrap().providers.contains_key("deepseek"));
    }

    #[test]
    fn ids_for_new_services() {
        let none = |_: &str| false;
        assert_eq!(new_id(None, "llm.acme.com", none), "acme");
        assert_eq!(new_id(None, "localhost", none), "localhost");
        assert_eq!(new_id(None, "192.168.1.20", none), "192-168-1-20");
        assert_eq!(new_id(Some("Company Gateway!"), "x.example", none), "company-gateway");
        assert_eq!(new_id(Some("公司网关"), "gw.corp.example", none), "corp");
        assert_eq!(new_id(Some("local-thing"), "x", none), "custom-local-thing");
        assert_eq!(new_id(None, "openrouter.ai", |id| id == "openrouter"), "openrouter-2");
    }
}
