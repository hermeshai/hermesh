//! Guards over every built-in channel and every model it can list, so that adding a channel or
//! a catalog sync cannot bring back a menu that lies: a level that changes nothing, a model that
//! thinks but offers no level, a plan model on default limits (docs/kernel/model-catalog-design.md
//! §18.7).

use her_llm::Catalog;
use her_llm::Context;
use her_llm::ProviderConfig;
use her_llm::RequestOptions;
use her_llm::ThinkingLevel;
use her_llm::api::anthropic;
use her_llm::api::openai_chat;
use her_llm::directory::DirectoryModel;
use her_llm::directory::ModelDirectory;
use her_llm::discovery::ListSource;
use her_llm::discovery::ListedModel;
use her_llm::discovery::ModelList;
use her_llm::model::Model;
use her_llm::model::ThinkingFormat;
use her_llm::public_catalog;
use her_llm::public_catalog::PublicModel;
use serde_json::Value;

/// A directory where each channel with a row in the catalog lists all the models of that row, as
/// if every plan offered everything the catalog knows of it.
fn directory(catalog: &Catalog, home: &std::path::Path) -> ModelDirectory {
    for provider in catalog.providers.values() {
        let Some(row) = provider.listing.catalog_id.as_deref().and_then(|id| public_catalog::load(None, id)) else {
            continue;
        };
        let list = ModelList {
            version: 1,
            fetched_at: Some(her_llm::discovery::now()),
            checked_at: her_llm::discovery::now(),
            source: ListSource::Provider,
            credential: None,
            error: None,
            models: row.models.iter().map(|m| ListedModel { id: m.id.clone(), ..Default::default() }).collect(),
        };
        her_llm::discovery::write_json(&home.join(format!("lists/{}.json", provider.id)), &list).unwrap();
    }
    ModelDirectory::new(Some(home.to_path_buf()))
}

fn body(model: &Model, level: ThinkingLevel) -> Value {
    let options = RequestOptions { thinking: level, ..Default::default() };
    let bytes = match model.api.as_str() {
        "anthropic-messages" => anthropic::build_body(model, &Context::default(), &options),
        _ => openai_chat::build_body(model, &Context::default(), &options),
    };
    serde_json::from_slice(&bytes).unwrap()
}

/// Every chat model of every built-in channel, its seeds and listed models alike.
fn each_model(check: impl Fn(&ProviderConfig, &DirectoryModel)) {
    let catalog = Catalog::builtin().unwrap();
    let home = tempfile::tempdir().unwrap();
    let directory = directory(&catalog, home.path());
    for provider in catalog.providers.values() {
        for model in directory.models(provider).models.iter().filter(|m| m.meta.chat) {
            check(provider, model);
        }
    }
    let seeds = ModelDirectory::new(None);
    for provider in catalog.providers.values().filter(|p| p.plan.is_some()) {
        for model in seeds.models(provider).models.iter() {
            check(provider, model);
        }
    }
}

#[test]
fn every_offered_level_reaches_the_request() {
    each_model(|_, m| {
        let model = &m.model;
        let default = body(model, ThinkingLevel::Default);
        for level in &model.thinking {
            let sent = body(model, *level);
            assert_ne!(sent, default, "{} offers {level:?} and it changes nothing", model.reference());
            // A channel takes one way of asking how hard to think.
            assert!(
                sent.get("reasoning_effort").is_none() || sent.get("thinking_budget").is_none(),
                "{} {level:?}: {sent}",
                model.reference(),
            );
            if model.api == "anthropic-messages" && sent["thinking"]["type"] == "enabled" {
                assert_ne!(model.thinking_control, her_llm::model::ThinkingControl::Effort, "{}: newer models refuse a budget", model.reference());
            }
        }
    });
}

#[test]
fn a_model_that_names_its_efforts_offers_them() {
    each_model(|provider, m| {
        let model = &m.model;
        let Some(row) = row_of(provider, &model.id) else { return };
        let (thinking, _) = her_llm::directory::row_thinking(&row);
        if !model.reasoning || provider.entry(&model.id).is_some_and(|e| e.thinking.is_some()) {
            return;
        }
        for effort in &thinking.efforts {
            assert!(model.offers(*effort), "{} takes {effort:?}, its menu has {:?}", model.reference(), model.thinking);
        }
        // "Off" is real: it is in the row, as a switch this channel can send or an effort. An
        // aggregator translates for each model; a budget model of Anthropic's thinks only if asked.
        if model.offers(ThinkingLevel::Off) && provider.compat.thinking_format != Some(ThinkingFormat::Openrouter) {
            assert!(thinking.switch || thinking.off_effort || (thinking.budget && model.api == "anthropic-messages"), "{}", model.reference());
        }
    });
}

/// The row a channel's model is described by.
fn row_of(provider: &ProviderConfig, id: &str) -> Option<PublicModel> {
    provider
        .listing
        .catalog_id
        .as_deref()
        .and_then(|c| public_catalog::load_one(None, c, id))
        .or_else(|| public_catalog::MAKERS.iter().find_map(|maker| public_catalog::load_named(None, maker, id)))
}

/// Routing names, which no catalog can describe, and whose facts nobody documents.
const ROUTING_NAMES: &[&str] = &["tc-code-latest"];

#[test]
fn a_plans_models_are_described() {
    let catalog = Catalog::builtin().unwrap();
    let seeds = ModelDirectory::new(None);
    for provider in catalog.providers.values().filter(|p| p.plan.is_some()) {
        for m in seeds.models(provider).models.iter() {
            assert_eq!(
                m.meta.known,
                !ROUTING_NAMES.contains(&m.model.id.as_str()),
                "{}: find its row or write what it can do in builtin.toml",
                m.model.reference(),
            );
            assert!(!m.model.cost.is_configured(), "{}: a subscription is not billed per token", m.model.reference());
        }
    }
}

#[test]
fn each_channel_says_how_it_sends_thinking() {
    let catalog = Catalog::builtin().unwrap();
    let unset: Vec<&str> = catalog
        .providers
        .values()
        .filter(|p| p.api == "openai-chat" && p.compat.thinking_format.is_none())
        .map(|p| p.id.as_str())
        .collect();
    // Their switch is not documented or does not exist: efforts still go out, and only those.
    assert_eq!(unset, ["ollama", "tencent-token-plan"], "a new channel chooses a `thinking_format`, or is listed here on purpose");
    assert!(catalog.providers.values().all(|p| p.compat.thinking_format != Some(ThinkingFormat::None)));
}

/// What the issue this suite guards against looked like, on the channel it was found on.
#[test]
fn the_bailian_plan_offers_what_its_documentation_says() {
    use ThinkingLevel::*;
    let catalog = Catalog::builtin().unwrap();
    let home = tempfile::tempdir().unwrap();
    let directory = directory(&catalog, home.path());
    let plan = &catalog.providers["bailian-token-plan"];
    let levels = |id: &str| directory.model(plan, id).unwrap().thinking.clone();
    assert_eq!(levels("qwen3.8-max"), [Off, Low, Medium, Xhigh]);
    assert_eq!(levels("qwen3.8-flash"), [Off, Low, Medium, Xhigh]);
    assert_eq!(levels("qwen3.7-max"), [Off, Low, Medium, High, Max]);
    assert_eq!(levels("glm-5.3"), [Low, High, Max]);
    assert_eq!(levels("glm-5.2"), [High, Max]);
    assert_eq!(levels("deepseek-v4.1-flash"), [Off, Low, High, Max]);
    assert_eq!(levels("deepseek-v4-pro"), [Off, High, Max]);
    assert_eq!(levels("deepseek-v4.1-flash-1017"), [Off, Low, High, Max], "a dated build is a model of its own");
    let merged = directory.models(plan);
    assert!(merged.get("deepseek-v4.1-flash-1017").unwrap().meta.known);
    assert!(!merged.get("wan2.7-image").unwrap().meta.chat, "the catalog knows what is not a chat model");
}
