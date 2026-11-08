//! The models clients see: providers with where their keys and models come from, each model's
//! details and whether it is in the model menu (docs/kernel/model-catalog-design.md §6, §8).
//! Shared by `her models` and the protocol service.

use her_llm::AuthConfig;
use her_llm::AuthStatus;
use her_llm::Model;
use her_llm::ModelRegistry;
use her_llm::ProviderConfig;
use her_llm::directory::DirectoryModel;
use her_llm::directory::IdSource;
use her_llm::directory::ModelMeta;
use her_llm::discovery::ListSource;
use her_llm::discovery::ModelList;
use her_protocol::api::AuthKind;
use her_protocol::api::CredentialState;
use her_protocol::api::ListingError;
use her_protocol::api::ListingSource;
use her_protocol::api::ModelPrice;
use her_protocol::api::ModelSummary;
use her_protocol::api::ModelsListResult;
use her_protocol::api::ProviderListing;
use her_protocol::api::ProviderSummary;
use her_protocol::api::SessionDefaults;
use her_llm::ThinkingLevel;

use crate::kernel::KernelError;

/// The public catalog's address; tests point it at a local server.
pub fn catalog_url() -> String {
    std::env::var("HER_MODEL_CATALOG_URL").unwrap_or_else(|_| her_llm::public_catalog::SOURCE_URL.to_string())
}

/// Providers whose list can be asked now: they have one, own it, and have credentials.
pub fn refreshable(llm: &ModelRegistry) -> Vec<String> {
    llm.providers()
        .filter(|p| {
            p.listing.path.is_some()
                && p.listing.from.is_none()
                && !matches!(p.auth.status(&p.id), AuthStatus::Missing(_) | AuthStatus::Managed)
        })
        .map(|p| p.id)
        .collect()
}

fn credential_of(status: &AuthStatus) -> (CredentialState, Option<String>) {
    match status {
        AuthStatus::NotRequired => (CredentialState::NotRequired, None),
        AuthStatus::Managed => (CredentialState::Connection, None),
        AuthStatus::Configured(source) if source == "config" => (CredentialState::Config, None),
        AuthStatus::Configured(source) => (CredentialState::Env, Some(source.clone())),
        AuthStatus::Stored => (CredentialState::Stored, None),
        AuthStatus::Missing(_) => (CredentialState::Missing, None),
        AuthStatus::Command => (CredentialState::Command, None),
    }
}

/// A local server that did not answer cannot be used.
fn unreachable(provider: &ProviderConfig, list: Option<&ModelList>) -> bool {
    matches!(provider.auth, AuthConfig::None)
        && list.and_then(|l| l.error.as_ref()).is_some_and(|e| e.kind == "network" || e.kind == "timeout")
}

/// The level a session starts with on `model` (§18): the one last chosen on it, else its
/// configured default, else `Default`.
pub fn start_thinking(llm: &ModelRegistry, model: &Model) -> ThinkingLevel {
    llm.directory()
        .remembered_thinking(&model.reference())
        .filter(|l| model.offers(*l))
        .unwrap_or(model.default_thinking)
}

/// The level a session keeps when it moves to `model` holding `current` (§18): `current` when the
/// model offers it, else the level the model starts with.
pub fn thinking_for(llm: &ModelRegistry, model: &Model, current: ThinkingLevel) -> ThinkingLevel {
    if current != ThinkingLevel::Default && model.offers(current) {
        current
    } else {
        start_thinking(llm, model)
    }
}

fn summary_of(llm: &ModelRegistry, model: &Model, meta: &ModelMeta, available: bool, shown: bool) -> ModelSummary {
    ModelSummary {
        id: model.reference(),
        name: model.name.clone(),
        names: meta.names.clone(),
        provider: model.provider.clone(),
        context_window: u64::from(model.context_window),
        reasoning: model.reasoning,
        thinking_levels: model.thinking.clone(),
        default_thinking: start_thinking(llm, model),
        images: model.accepts_images(),
        available,
        shown,
        known: meta.known,
        deprecated: meta.deprecated,
        released: meta.released.clone(),
        cost: model.cost.is_configured().then(|| ModelPrice {
            input: model.cost.input,
            output: model.cost.output,
            cache_read: (model.cost.cache_read > 0.0).then_some(model.cost.cache_read),
            cache_write: (model.cost.cache_write > 0.0).then_some(model.cost.cache_write),
            currency: model.currency,
        }),
    }
}


/// The model a provider starts on among those shown: the first one it declares, else its newest.
fn starting_model<'a>(provider: &ProviderConfig, shown: &[&'a DirectoryModel]) -> Option<&'a DirectoryModel> {
    let declared = provider.listing.entries.iter().find_map(|entry| shown.iter().find(|m| m.model.id == entry.id));
    declared
        .or_else(|| shown.iter().max_by_key(|m| (!m.meta.deprecated, m.meta.released.clone())))
        .copied()
}

/// The model a provider starts on, as a first choice to offer.
pub fn provider_start(llm: &ModelRegistry, provider: &ProviderConfig) -> Option<String> {
    let directory = llm.directory();
    let selection = directory.selection();
    let merged = directory.models(provider);
    let by_default = merged.default_shown(provider.listing.from.is_some());
    let shown: Vec<&DirectoryModel> = merged
        .models
        .iter()
        .filter(|m| m.meta.chat && selection.is_shown(&m.model.reference(), by_default.contains(&m.model.id)))
        .collect();
    starting_model(provider, &shown).map(|m| m.model.reference())
}

/// What a new session runs on when nobody names a model: the configured `model`, else the model
/// the first usable provider starts on, in the order of `models/list`. `None` when nothing is set
/// up: no model is assumed for the person, and clients tell them to connect one. May read saved
/// keys (the keychain), so it blocks.
pub fn default_model(llm: &ModelRegistry, configured: Option<&str>) -> Option<String> {
    if let Some(model) = configured {
        return Some(model.to_string());
    }
    let directory = llm.directory();
    let selection = directory.selection();
    // Every model hidden by hand: one of them still runs.
    let mut hidden = None;
    for provider in llm.providers() {
        if matches!(provider.auth.status(&provider.id), AuthStatus::Missing(_)) {
            continue;
        }
        let merged = directory.models(&provider);
        if unreachable(&provider, merged.list.as_ref()) {
            continue;
        }
        let by_default = merged.default_shown(provider.listing.from.is_some());
        let chat = || merged.models.iter().filter(|m| m.meta.chat);
        let shown: Vec<&DirectoryModel> =
            chat().filter(|m| selection.is_shown(&m.model.reference(), by_default.contains(&m.model.id))).collect();
        if let Some(model) = starting_model(&provider, &shown) {
            return Some(model.model.reference());
        }
        hidden = hidden.or_else(|| chat().next().map(|m| m.model.reference()));
    }
    hidden
}

/// The model of a new session: the one asked for, else [`default_model`]. Blocks as that does.
pub fn session_model(llm: &ModelRegistry, asked: Option<String>, configured: Option<&str>) -> Result<String, KernelError> {
    asked.or_else(|| default_model(llm, configured)).ok_or(KernelError::NoModel)
}

/// Models, providers with where their keys and models come from, and the defaults of new
/// sessions. `defaults` is what the configuration says, `model` only when it names one; the
/// result's says what a new session gets. `refreshing` says which providers' lists are being
/// refreshed. May read saved keys (the keychain), so it blocks.
pub fn list(llm: &ModelRegistry, defaults: &SessionDefaults, refreshing: &dyn Fn(&str) -> bool) -> ModelsListResult {
    let directory = llm.directory();
    let selection = directory.selection();
    let configured = defaults.model.as_deref();
    let mut providers = Vec::new();
    let mut models = Vec::new();
    for provider in llm.providers() {
        let (credential, credential_source) = credential_of(&provider.auth.status(&provider.id));
        let mut summary = ProviderSummary {
            plan: provider.plan.as_ref().map(|plan| her_protocol::api::TokenPlanSummary {
                key_url: plan.key_url.clone(),
                base_url: provider.base_url.clone(),
                api: provider.api.clone(),
                dynamic_models: provider.listing.path.is_some(),
            }),
            id: provider.id.clone(),
            name: provider.name.clone(),
            names: provider.names.clone(),
            auth: match provider.auth {
                AuthConfig::None => AuthKind::None,
                AuthConfig::Managed => AuthKind::Connection,
                AuthConfig::ApiKey { .. } => AuthKind::ApiKey,
                AuthConfig::Command { .. } => AuthKind::Command,
            },
            credential,
            credential_source,
            key_env: provider.auth.env_vars().to_vec(),
            listing: None,
        };
        if credential == CredentialState::Missing {
            providers.push(summary);
            continue;
        }
        let merged = directory.models(&provider);
        let list = merged.list.as_ref();
        if !unreachable(&provider, list) {
            let shown_by_default = merged.default_shown(provider.listing.from.is_some());
            let (mut count, mut shown) = (0, 0);
            for m in merged.models.iter().filter(|m| m.meta.chat) {
                let reference = m.model.reference();
                let by_default = shown_by_default.contains(&m.model.id) || configured == Some(reference.as_str());
                let is_shown = selection.is_shown(&reference, by_default);
                count += 1;
                shown += u32::from(is_shown);
                models.push(summary_of(llm, &m.model, &m.meta, true, is_shown));
            }
            summary.listing = provider.listing.is_dynamic().then(|| ProviderListing {
                source: match merged.source {
                    IdSource::Provider => ListingSource::Provider,
                    IdSource::Catalog => ListingSource::Catalog,
                    IdSource::Config if list.is_some_and(|l| l.source == ListSource::Unsupported) => {
                        ListingSource::Unsupported
                    }
                    IdSource::Config => ListingSource::Config,
                },
                fetched_at: list.and_then(|l| l.fetched_at.clone()),
                refreshing: refreshing(&provider.id),
                error: list
                    .and_then(|l| l.error.as_ref())
                    .map(|e| ListingError { kind: e.kind.clone(), message: e.message.clone() }),
                models: count,
                shown,
            });
        } else {
            summary.listing = Some(ProviderListing {
                source: ListingSource::Provider,
                fetched_at: list.and_then(|l| l.fetched_at.clone()),
                refreshing: false,
                error: list
                    .and_then(|l| l.error.as_ref())
                    .map(|e| ListingError { kind: e.kind.clone(), message: e.message.clone() }),
                models: 0,
                shown: 0,
            });
        }
        providers.push(summary);
    }
    // The configured model shows even without its key, so a new session can ask for it.
    if let Some(configured) = configured
        && !models.iter().any(|m| m.id == configured)
        && let Ok(resolved) = llm.resolve(configured)
    {
        let meta = ModelMeta { known: !resolved.ad_hoc, ..Default::default() };
        models.push(summary_of(llm, &resolved.model, &meta, false, true));
    }
    let defaults = SessionDefaults { model: default_model(llm, configured), ..defaults.clone() };
    ModelsListResult { models, providers, defaults: Some(defaults) }
}

/// Puts a model in the menu or takes it out (`models/show`). Blocking I/O.
pub fn set_shown(llm: &ModelRegistry, configured: Option<&str>, reference: &str, shown: bool) -> Result<(), String> {
    let (provider_id, model_id) = reference
        .split_once('/')
        .ok_or_else(|| format!("expected provider/model-id, got '{reference}'"))?;
    let provider =
        llm.provider(provider_id).ok_or_else(|| format!("unknown provider '{provider_id}'"))?;
    let by_default = configured == Some(reference)
        || llm.directory().models(&provider).default_shown(provider.listing.from.is_some()).contains(model_id);
    llm.directory().set_shown(reference, shown, by_default).map_err(|e| e.message)
}

#[cfg(test)]
mod tests {
    use super::*;
    use her_llm::Catalog;
    use her_llm::RegistryOptions;
    use her_protocol::PermissionMode;
    use her_llm::ThinkingLevel;

    #[test]
    fn an_account_connections_models_can_be_hidden() {
        let dir = tempfile::tempdir().unwrap();
        let llm = ModelRegistry::new(
            Catalog::default(),
            RegistryOptions { models_dir: Some(dir.path().to_path_buf()), ..Default::default() },
        );
        let mut provider = Catalog::from_toml("[providers.x]\napi = \"openai-chat\"\nbase_url = \"https://x.invalid/v1\"\n")
            .unwrap()
            .providers
            .remove("x")
            .unwrap();
        provider.id = "qwenwork-a".into();
        provider.auth = AuthConfig::Managed;
        provider.listing.entries = ["flash", "pro"].map(her_llm::catalog::ModelToml::new).to_vec();
        provider.listing.entries[0].names = [("en".to_string(), "Standard".to_string()), ("zh".to_string(), "标准".to_string())].into();
        provider.names = [("en".to_string(), "QwenWork".to_string())].into();
        llm.replace_source("model-connections", vec![provider]).unwrap();
        let defaults = SessionDefaults { model: Some("deepseek/deepseek-flash".into()), thinking: ThinkingLevel::Medium, permission_mode: PermissionMode::Manual };
        let listed = list(&llm, &defaults, &|_| false);
        let names = |id: &str| listed.models.iter().find(|m| m.id == id).unwrap().names.clone();
        assert_eq!(names("qwenwork-a/flash")["en"], "Standard", "a channel's names reach the list");
        assert!(names("qwenwork-a/pro").is_empty());
        let own = listed.providers.iter().find(|p| p.id == "qwenwork-a").unwrap();
        assert_eq!(own.names["en"], "QwenWork", "so does the name of its product");
        let shown = |llm: &ModelRegistry| -> Vec<(String, bool)> {
            list(llm, &defaults, &|_| false).models.into_iter().filter(|m| m.provider == "qwenwork-a").map(|m| (m.id, m.shown)).collect()
        };
        assert_eq!(shown(&llm), [("qwenwork-a/flash".to_string(), true), ("qwenwork-a/pro".to_string(), true)]);
        set_shown(&llm, defaults.model.as_deref(), "qwenwork-a/pro", false).unwrap();
        assert_eq!(shown(&llm), [("qwenwork-a/flash".to_string(), true), ("qwenwork-a/pro".to_string(), false)]);
        set_shown(&llm, defaults.model.as_deref(), "qwenwork-a/pro", true).unwrap();
        assert!(llm.directory().selection().hide.is_empty(), "showing it again forgets the choice");
    }

    /// A registry of these providers: `(id, key, declared models)`. Models are the channel's own
    /// entries, with no public catalog behind them.
    fn registry(dir: &std::path::Path, providers: &[(&str, Option<&str>, &[&str])]) -> ModelRegistry {
        let mut toml = String::new();
        for (id, key, declared) in providers {
            toml.push_str(&format!("[providers.{id}]\napi = \"openai-chat\"\nbase_url = \"https://{id}.invalid/v1\"\nlist_models = false\n"));
            match key {
                Some(key) => toml.push_str(&format!("api_key = \"{key}\"\n")),
                None => toml.push_str(&format!("api_key_env = \"HER_TEST_NO_SUCH_KEY_{}\"\n", id.to_uppercase())),
            }
            for model in *declared {
                toml.push_str(&format!("[[providers.{id}.models]]\nid = \"{model}\"\n"));
            }
        }
        ModelRegistry::new(
            Catalog::from_toml(&toml).unwrap(),
            RegistryOptions { models_dir: Some(dir.to_path_buf()), ..Default::default() },
        )
    }

    #[test]
    fn nothing_set_up_means_no_model() {
        let dir = tempfile::tempdir().unwrap();
        let llm = registry(dir.path(), &[("a", None, &["one"]), ("b", None, &["two"])]);
        assert_eq!(default_model(&llm, None), None);
        assert!(matches!(session_model(&llm, None, None), Err(KernelError::NoModel)));
        let defaults = SessionDefaults { model: None, thinking: ThinkingLevel::Default, permission_mode: PermissionMode::Manual };
        let listing = list(&llm, &defaults, &|_| false);
        assert_eq!(listing.defaults.unwrap().model, None);
        assert!(listing.models.is_empty(), "no provider's model is offered before it is connected");
    }

    #[test]
    fn the_configured_model_stands_with_or_without_a_key() {
        let dir = tempfile::tempdir().unwrap();
        let llm = registry(dir.path(), &[("a", Some("k"), &["one"]), ("b", None, &["two"])]);
        assert_eq!(default_model(&llm, Some("b/two")).as_deref(), Some("b/two"));
        let defaults = SessionDefaults { model: Some("b/two".into()), thinking: ThinkingLevel::Default, permission_mode: PermissionMode::Manual };
        let listing = list(&llm, &defaults, &|_| false);
        assert_eq!(listing.defaults.unwrap().model.as_deref(), Some("b/two"));
        let keyless = listing.models.iter().find(|m| m.id == "b/two").expect("listed, to ask for its key");
        assert!(!keyless.available);
    }

    #[test]
    fn the_first_usable_provider_starts_on_the_model_it_declares_first() {
        let dir = tempfile::tempdir().unwrap();
        let llm = registry(dir.path(), &[("a", None, &["one"]), ("b", Some("k"), &["zebra", "apple"]), ("c", Some("k"), &["other"])]);
        assert_eq!(default_model(&llm, None).as_deref(), Some("b/zebra"));
        assert_eq!(session_model(&llm, Some("c/other".into()), None).unwrap(), "c/other", "one asked for wins");
        let defaults = SessionDefaults { model: None, thinking: ThinkingLevel::Default, permission_mode: PermissionMode::Manual };
        let listing = list(&llm, &defaults, &|_| false);
        assert_eq!(listing.defaults.unwrap().model.as_deref(), Some("b/zebra"));
        assert!(listing.models.iter().all(|m| m.available), "the keyless provider offers nothing");
    }

    #[test]
    fn a_hidden_model_gives_way_and_all_hidden_still_runs() {
        let dir = tempfile::tempdir().unwrap();
        let llm = registry(dir.path(), &[("b", Some("k"), &["zebra", "apple"])]);
        set_shown(&llm, None, "b/zebra", false).unwrap();
        assert_eq!(default_model(&llm, None).as_deref(), Some("b/apple"));
        set_shown(&llm, None, "b/apple", false).unwrap();
        assert_eq!(default_model(&llm, None).as_deref(), Some("b/zebra"), "better a hidden model than none");
    }

    #[test]
    fn a_provider_that_declares_nothing_starts_on_its_newest_model() {
        let dir = tempfile::tempdir().unwrap();
        let toml = "[providers.deepseek]\napi = \"openai-chat\"\nbase_url = \"https://x.invalid/v1\"\napi_key = \"k\"\nlist_models = false\ncatalog_id = \"deepseek\"\n";
        let llm = ModelRegistry::new(
            Catalog::from_toml(toml).unwrap(),
            RegistryOptions { models_dir: Some(dir.path().to_path_buf()), ..Default::default() },
        );
        let provider = llm.provider("deepseek").unwrap();
        let merged = llm.directory().models(&provider);
        let newest = merged.models.iter().filter(|m| m.meta.chat && !m.meta.deprecated).max_by_key(|m| m.meta.released.clone()).unwrap();
        assert_eq!(default_model(&llm, None), Some(newest.model.reference()));
    }

    /// The models the built-in providers start on: the catalog must know them, and not as retired.
    /// The weekly catalog refresh fails here when one goes.
    #[test]
    fn every_built_in_provider_starts_on_a_model_it_still_offers() {
        let dir = tempfile::tempdir().unwrap();
        let llm = ModelRegistry::new(
            Catalog::builtin().unwrap(),
            RegistryOptions { models_dir: Some(dir.path().to_path_buf()), ..Default::default() },
        );
        let mut checked = 0;
        for provider in llm.providers() {
            let merged = llm.directory().models(&provider);
            let Some(first) = provider.listing.entries.first() else {
                assert!(matches!(provider.auth, AuthConfig::None), "{} declares no model to start on", provider.id);
                continue;
            };
            let model = merged.get(&first.id).unwrap_or_else(|| panic!("{}/{} is not offered", provider.id, first.id));
            assert!(model.meta.chat && !model.meta.deprecated, "{}/{} cannot be started on", provider.id, first.id);
            // A plan's routing names are in no catalog.
            assert!(provider.plan.is_some() || model.meta.known, "the catalog no longer knows {}/{}", provider.id, first.id);
            let by_default = merged.default_shown(provider.listing.from.is_some());
            assert!(by_default.contains(&first.id), "{}/{} is not shown", provider.id, first.id);
            checked += 1;
        }
        assert!(checked >= 14, "{checked}");
    }
}
