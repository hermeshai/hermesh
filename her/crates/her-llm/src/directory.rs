//! Every channel's models, merged from the layers of docs/kernel/model-catalog-design.md §4: what
//! the channel declares (models.toml and built-in entries, an account connection's catalog), its
//! list, its row in the public catalog or else the row of the model's maker, the defaults.
//! Everything is read on first use and per provider, so a command that uses one model reads
//! one provider's files.

use parking_lot::Mutex;
use parking_lot::RwLock;
use serde::Deserialize;
use serde::Serialize;
use std::collections::BTreeSet;
use std::collections::HashMap;
use std::path::Path;
use std::path::PathBuf;
use std::sync::Arc;

use crate::catalog::ModelToml;
use crate::catalog::ProviderConfig;
use crate::catalog::merge_model;
use crate::discovery::ListSource;
use crate::discovery::ListedModel;
use crate::discovery::ModelList;
use crate::error::LlmError;
use crate::model::CatalogThinking;
use crate::model::Modality;
use crate::model::Model;
use crate::model::ThinkingBudgets;
use crate::public_catalog::PublicModel;
use crate::public_catalog::PublicProvider;
use crate::public_catalog::ReasoningOption;
use crate::types::Currency;
use crate::types::ThinkingLevel;

/// How many models a provider shows by default (the newest).
pub const DEFAULT_SHOWN: usize = 12;

/// Where a provider's model ids came from.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum IdSource {
    /// The provider's list.
    Provider,
    /// No list: the public catalog's models.
    Catalog,
    /// Only the models written in models.toml or the built-in entries.
    Config,
}

/// What a picker needs to know about a model besides the model itself.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct ModelMeta {
    /// Some layer above the defaults describes it.
    pub known: bool,
    pub deprecated: bool,
    /// `YYYY-MM-DD`
    pub released: Option<String>,
    /// Written in models.toml or the built-in entries.
    pub configured: bool,
    /// A chat model an agent can use.
    pub chat: bool,
    /// Its name by language tag, when its channel declares them.
    pub names: std::collections::BTreeMap<String, String>,
}

#[derive(Clone, Debug)]
pub struct DirectoryModel {
    pub model: Arc<Model>,
    pub meta: ModelMeta,
}

/// One provider's merged models, in the order of their source.
#[derive(Clone, Debug)]
pub struct ProviderModels {
    pub source: IdSource,
    pub list: Option<ModelList>,
    pub models: Vec<DirectoryModel>,
}

impl ProviderModels {
    pub fn get(&self, id: &str) -> Option<&DirectoryModel> {
        self.models.iter().find(|m| m.model.id == id)
    }

    /// Ids shown by default (§6.2): known, not deprecated, the newest `DEFAULT_SHOWN`; configured
    /// models always. A provider that shares another's list shows only configured ones.
    pub fn default_shown(&self, shares_list: bool) -> BTreeSet<String> {
        let mut shown: BTreeSet<String> =
            self.models.iter().filter(|m| m.meta.configured).map(|m| m.model.id.clone()).collect();
        if shares_list {
            return shown;
        }
        let mut candidates: Vec<&DirectoryModel> = self
            .models
            .iter()
            .filter(|m| m.meta.chat && m.meta.known && !m.meta.deprecated && !m.meta.configured)
            .collect();
        candidates.sort_by(|a, b| {
            b.meta.released.is_some().cmp(&a.meta.released.is_some())
                .then_with(|| b.meta.released.cmp(&a.meta.released))
                .then_with(|| a.model.id.cmp(&b.model.id))
        });
        let room = DEFAULT_SHOWN.saturating_sub(shown.len());
        shown.extend(candidates.into_iter().take(room).map(|m| m.model.id.clone()));
        shown
    }
}

/// Models the user showed or hid by hand, by `provider/model-id` (§6.3), and the thinking level
/// last chosen on each model (§18).
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
pub struct Selection {
    #[serde(default)]
    pub version: u32,
    #[serde(default)]
    pub show: BTreeSet<String>,
    #[serde(default)]
    pub hide: BTreeSet<String>,
    #[serde(default, skip_serializing_if = "std::collections::BTreeMap::is_empty")]
    pub thinking: std::collections::BTreeMap<String, ThinkingLevel>,
}

impl Selection {
    pub fn is_shown(&self, reference: &str, by_default: bool) -> bool {
        !self.hide.contains(reference) && (by_default || self.show.contains(reference))
    }

    /// Records a choice; a choice equal to the default is forgotten.
    pub fn set(&mut self, reference: &str, shown: bool, by_default: bool) {
        self.show.remove(reference);
        self.hide.remove(reference);
        match (shown, by_default) {
            (true, false) => {
                self.show.insert(reference.to_string());
            }
            (false, true) => {
                self.hide.insert(reference.to_string());
            }
            _ => {}
        }
    }
}

/// The merged models of every provider, read lazily and dropped when something is refreshed.
#[derive(Default)]
pub struct ModelDirectory {
    dir: Option<PathBuf>,
    public: RwLock<HashMap<String, Option<Arc<PublicProvider>>>>,
    /// Makers' rows found by a channel's name for a model, and the names that have none.
    maker_rows: RwLock<HashMap<String, Option<Arc<PublicModel>>>>,
    merged: RwLock<HashMap<String, Arc<ProviderModels>>>,
    selection: RwLock<Option<Arc<Selection>>>,
    /// Providers that said a model does not exist; their lists are refreshed.
    missing: Arc<Mutex<BTreeSet<String>>>,
}

impl ModelDirectory {
    /// `dir` is `~/.her/models`; `None` keeps everything in memory (tests).
    pub fn new(dir: Option<PathBuf>) -> Self {
        Self { dir, ..Default::default() }
    }

    pub fn dir(&self) -> Option<&Path> {
        self.dir.as_deref()
    }

    pub fn catalog_dir(&self) -> Option<PathBuf> {
        self.dir.as_ref().map(|d| d.join("catalog"))
    }

    pub fn lists_dir(&self) -> Option<PathBuf> {
        self.dir.as_ref().map(|d| d.join("lists"))
    }

    /// Forget what was read, after a refresh or a key change.
    pub fn invalidate(&self) {
        self.public.write().clear();
        self.maker_rows.write().clear();
        self.merged.write().clear();
        *self.selection.write() = None;
    }

    /// Forget a provider's merged models: what it declares has changed.
    pub fn forget(&self, provider: &str) {
        self.merged.write().remove(provider);
    }

    /// Where requests note providers whose model turned out not to exist.
    pub fn missing_handle(&self) -> Arc<Mutex<BTreeSet<String>>> {
        self.missing.clone()
    }

    /// Providers noted since the last call.
    pub fn take_missing(&self) -> BTreeSet<String> {
        std::mem::take(&mut *self.missing.lock())
    }

    pub fn list(&self, provider: &str) -> Option<ModelList> {
        crate::discovery::read_list(&self.lists_dir()?, provider)
    }

    pub fn public(&self, catalog_id: &str) -> Option<Arc<PublicProvider>> {
        if let Some(found) = self.public.read().get(catalog_id) {
            return found.clone();
        }
        let loaded = crate::public_catalog::load(self.catalog_dir().as_deref(), catalog_id).map(Arc::new);
        self.public.write().insert(catalog_id.to_string(), loaded.clone());
        loaded
    }

    /// A provider's merged models.
    pub fn models(&self, provider: &ProviderConfig) -> Arc<ProviderModels> {
        if let Some(found) = self.merged.read().get(&provider.id) {
            return found.clone();
        }
        let merged = Arc::new(self.merge(provider));
        self.merged.write().insert(provider.id.clone(), merged.clone());
        merged
    }

    /// One model. Without the provider's merged models at hand, only what this model needs is
    /// read: its line of the public catalog and the provider's list. A model the catalog knows
    /// is found even when the list leaves it out, unless the channel fixes its membership.
    pub fn model(&self, provider: &ProviderConfig, id: &str) -> Option<Arc<Model>> {
        if provider.fixed_membership() {
            return self.models(provider).get(id).map(|m| m.model.clone());
        }
        if let Some(found) = self.merged.read().get(&provider.id).and_then(|m| m.get(id).map(|m| m.model.clone())) {
            return Some(found);
        }
        let listing = &provider.listing;
        let list = self.list(listing.from.as_deref().unwrap_or(&provider.id));
        let listed = list
            .as_ref()
            .filter(|l| l.source == ListSource::Provider)
            .and_then(|l| l.models.iter().find(|m| m.id == id));
        let own = listing.catalog_id.as_deref().and_then(|c| {
            match self.public.read().get(c) {
                Some(loaded) => loaded.as_ref().and_then(|p| p.model(id).cloned()),
                None => crate::public_catalog::load_one(self.catalog_dir().as_deref(), c, id),
            }
        });
        let maker = if own.is_none() { self.maker_row(id) } else { None };
        let entry = provider.entry(id);
        if listed.is_none() && own.is_none() && maker.is_none() && entry.is_none() {
            return None;
        }
        let row = own.as_ref().map(Row::Own).or(maker.as_deref().map(Row::Maker));
        Some(build(provider, id, row, listed, entry).model)
    }

    /// The row of the model's maker, as this channel calls the model. Asked on every request of
    /// a model without a row of its channel, so the answer is kept. A name that this build's
    /// snapshots do not have, and no refresh could have brought, costs one search.
    fn maker_row(&self, id: &str) -> Option<Arc<PublicModel>> {
        if let Some(found) = self.maker_rows.read().get(id) {
            return found.clone();
        }
        let dir = self.catalog_dir();
        let row = (crate::public_catalog::compiled_maker_has(id) || self.has_local_copies())
            .then(|| {
                crate::public_catalog::MAKERS.iter().find_map(|maker| match self.public.read().get(*maker) {
                    Some(loaded) => loaded.as_ref().and_then(|p| p.model_named(id).cloned()),
                    None => crate::public_catalog::load_named(dir.as_deref(), maker, id),
                })
            })
            .flatten()
            .map(Arc::new);
        self.maker_rows.write().insert(id.to_string(), row.clone());
        row
    }

    /// Whether a refresh left local copies of the makers' rows, which may be newer than this
    /// build's. One file says so: a refresh writes them together.
    fn has_local_copies(&self) -> bool {
        self.catalog_dir().is_some_and(|dir| dir.join(format!("{}.json", crate::public_catalog::MAKERS[0])).exists())
    }

    fn merge(&self, provider: &ProviderConfig) -> ProviderModels {
        let listing = &provider.listing;
        let list = self.list(listing.from.as_deref().unwrap_or(&provider.id));
        let public = listing.catalog_id.as_deref().and_then(|id| self.public(id));
        let listed: Option<&[ListedModel]> = list
            .as_ref()
            .filter(|l| l.source == ListSource::Provider && (provider.plan.is_some() || !l.models.is_empty()))
            .map(|l| l.models.as_slice());
        let (source, mut ids): (IdSource, Vec<String>) = match (listed, &public) {
            (Some(listed), _) => (IdSource::Provider, listed.iter().map(|m| m.id.clone()).collect()),
            (None, Some(public)) if !provider.fixed_membership() => (IdSource::Catalog, public.models.iter().map(|m| m.id.clone()).collect()),
            (None, _) => (IdSource::Config, Vec::new()),
        };
        for entry in &listing.entries {
            // A plan list is authoritative; seeds only cover a missing list endpoint.
            if !(provider.plan.is_some() && listed.is_some()) && !ids.contains(&entry.id) {
                ids.push(entry.id.clone());
            }
        }
        let unrowed = |id: &str| public.as_ref().is_none_or(|p| p.model(id).is_none());
        let unrowed: Vec<&String> = ids.iter().filter(|id| unrowed(id)).collect();
        if unrowed.iter().any(|id| crate::public_catalog::compiled_maker_has(id)) || (!unrowed.is_empty() && self.has_local_copies()) {
            // A list of many models the catalog does not know reads each maker once, not per model.
            for maker in crate::public_catalog::MAKERS {
                self.public(maker);
            }
        }
        let models = ids
            .iter()
            .map(|id| {
                let own = public.as_ref().and_then(|p| p.model(id));
                let maker = if own.is_none() { self.maker_row(id) } else { None };
                let row = own.map(Row::Own).or(maker.as_deref().map(Row::Maker));
                let listed = listed.and_then(|l| l.iter().find(|m| m.id == *id));
                build(provider, id, row, listed, provider.entry(id))
            })
            .collect();
        ProviderModels { source, list, models }
    }

    pub fn selection(&self) -> Arc<Selection> {
        if let Some(found) = self.selection.read().as_ref() {
            return found.clone();
        }
        let loaded: Selection = self
            .dir
            .as_ref()
            .and_then(|d| std::fs::read(d.join("selection.json")).ok())
            .and_then(|b| serde_json::from_slice(&b).ok())
            .unwrap_or_default();
        let loaded = Arc::new(loaded);
        *self.selection.write() = Some(loaded.clone());
        loaded
    }

    pub fn set_shown(&self, reference: &str, shown: bool, by_default: bool) -> Result<(), LlmError> {
        self.change_selection(|selection| selection.set(reference, shown, by_default))
    }

    /// The thinking level last chosen on a model, if any.
    pub fn remembered_thinking(&self, reference: &str) -> Option<ThinkingLevel> {
        self.selection().thinking.get(reference).copied()
    }

    /// Remembers the level chosen on a model; `Default` forgets it. Unchanged levels write nothing.
    pub fn remember_thinking(&self, reference: &str, level: ThinkingLevel) -> Result<(), LlmError> {
        if self.remembered_thinking(reference).unwrap_or_default() == level {
            return Ok(());
        }
        self.change_selection(|selection| {
            if level == ThinkingLevel::Default {
                selection.thinking.remove(reference);
            } else {
                selection.thinking.insert(reference.to_string(), level);
            }
        })
    }

    fn change_selection(&self, change: impl FnOnce(&mut Selection)) -> Result<(), LlmError> {
        let mut selection = (*self.selection()).clone();
        selection.version = 1;
        change(&mut selection);
        if let Some(dir) = &self.dir {
            crate::discovery::write_json(&dir.join("selection.json"), &selection)?;
        }
        *self.selection.write() = Some(Arc::new(selection));
        Ok(())
    }
}

/// A model's row in the public catalog.
#[derive(Clone, Copy)]
enum Row<'a> {
    /// The channel's own: all of it applies.
    Own(&'a PublicModel),
    /// The maker's, for a channel the catalog has no row of it for: what the model can do applies,
    /// not what the maker charges or has retired.
    Maker(&'a PublicModel),
}

impl<'a> Row<'a> {
    fn model(self) -> &'a PublicModel {
        match self {
            Row::Own(p) | Row::Maker(p) => p,
        }
    }
}

/// One model over the layers: public catalog, the provider's list, what the channel declares,
/// defaults.
fn build(
    provider: &ProviderConfig,
    id: &str,
    row: Option<Row<'_>>,
    listed: Option<&ListedModel>,
    entry: Option<&ModelToml>,
) -> DirectoryModel {
    let mut lower = row.map(|row| public_layer(row, id)).unwrap_or_else(|| ModelToml::new(id));
    if let Some(listed) = listed {
        merge_model(&mut lower, list_layer(listed));
    }
    let top = entry.cloned().unwrap_or_else(|| ModelToml::new(id));
    let mut model = provider.build_model(Some(&lower), &top);
    let public = row.map(Row::model);
    // Long-context prices are the catalog's, in dollars; a price set above replaces them all.
    if let Some(Row::Own(public)) = row
        && provider.plan.is_none()
        && model.currency == Currency::Usd
        && lower.cost == public.cost
        && top.cost.is_none()
        && listed.is_none_or(|l| l.cost.is_none())
    {
        model.cost_tiers = public.cost_tiers.clone();
    }
    let meta = ModelMeta {
        known: public.is_some() || listed.is_some_and(ListedModel::has_details) || entry.is_some_and(ModelToml::describes_model),
        deprecated: matches!(row, Some(Row::Own(p)) if p.deprecated),
        released: public.and_then(|p| p.released.clone()),
        configured: entry.is_some(),
        chat: entry.is_some()
            || (public.is_none_or(PublicModel::is_chat) && listed.is_none_or(|l| l.tool_call != Some(false))),
        names: entry.map(|e| e.names.clone()).unwrap_or_default(),
    };
    DirectoryModel { model: Arc::new(model), meta }
}

/// A row as a layer of the model the channel calls `id`, which is what requests must say.
fn public_layer(row: Row<'_>, id: &str) -> ModelToml {
    let p = row.model();
    let (thinking, thinking_budgets) = row_thinking(p);
    let own = matches!(row, Row::Own(_));
    ModelToml {
        id: id.to_string(),
        name: p.name.clone(),
        context_window: p.context_window,
        max_output_tokens: p.max_output_tokens,
        reasoning: p.reasoning,
        catalog_thinking: Some(thinking),
        thinking_budgets,
        input: (!p.input.is_empty()).then(|| {
            if p.input.iter().any(|i| i == "image") {
                vec![Modality::Text, Modality::Image]
            } else {
                vec![Modality::Text]
            }
        }),
        cost: p.cost.clone().filter(|c| own && c.is_configured()),
        currency: own.then_some(Currency::Usd),
        ..Default::default()
    }
}

fn list_layer(l: &ListedModel) -> ModelToml {
    ModelToml {
        id: l.id.clone(),
        name: l.name.clone(),
        context_window: l.context_window,
        max_output_tokens: l.max_output_tokens,
        reasoning: l.reasoning,
        input: l.images.map(|images| if images { vec![Modality::Text, Modality::Image] } else { vec![Modality::Text] }),
        currency: l.cost.as_ref().map(|_| Currency::Usd),
        cost: l.cost.clone().filter(|c| c.is_configured()),
        ..Default::default()
    }
}

/// Models whose thinking can be switched off although the public catalog names only their efforts:
/// Anthropic's API takes `thinking: {type: "disabled"}` on them, and the aggregator's rows of the
/// same models list `none`. A correction of the row, not a model entry, so that it does not pin
/// them to the model menu (§18.2).
const SWITCH_MISSING_FROM_THE_CATALOG: &[&str] = &["claude-opus-4-7", "claude-opus-4-8", "claude-opus-5"];

/// How a row's model is controlled: what the row says, with what it is known to leave out.
pub fn row_thinking(row: &PublicModel) -> (CatalogThinking, Option<ThinkingBudgets>) {
    let (mut thinking, budgets) = derive_thinking(&row.reasoning_options);
    thinking.switch |= SWITCH_MISSING_FROM_THE_CATALOG.contains(&row.id.as_str());
    (thinking, budgets)
}

/// How a model is controlled, and its budgets, from the catalog's reasoning options (§18): the
/// provider's own effort values (`none` is "off"), a switch, and a token budget, which has her's
/// budgets for the levels within the model's range and its largest for `max`. Values her has no
/// level for are left out.
pub fn derive_thinking(options: &[ReasoningOption]) -> (CatalogThinking, Option<ThinkingBudgets>) {
    let mut thinking = CatalogThinking::default();
    let mut range = None;
    for option in options {
        match option {
            ReasoningOption::Toggle => thinking.switch = true,
            ReasoningOption::Effort { values } => {
                for value in values {
                    match value.as_str() {
                        "none" => thinking.off_effort = true,
                        other => thinking.efforts.extend(other.parse::<ThinkingLevel>().ok().filter(|l| l.is_effort())),
                    }
                }
            }
            ReasoningOption::BudgetTokens { min, max } => {
                thinking.budget = true;
                range = Some((*min, *max));
            }
        }
    }
    thinking.efforts.sort();
    thinking.efforts.dedup();
    let budgets = range.filter(|(min, max)| min.is_some() || max.is_some()).map(|(min, max)| {
        let clamp = |default: u32| Some(default.max(min.unwrap_or(0)).min(max.unwrap_or(u32::MAX)));
        ThinkingBudgets {
            minimal: clamp(1_024),
            low: clamp(4_096),
            medium: clamp(10_240),
            high: clamp(24_576),
            xhigh: clamp(28_672),
            max: max.or_else(|| clamp(32_768)),
        }
    });
    (thinking, budgets)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::catalog::Catalog;
    use crate::catalog::Listing;
    use crate::catalog::offered_by_row;
    use crate::discovery::ModelList;
    use crate::model::ModelCost;

    fn effort(values: &[&str]) -> Vec<ReasoningOption> {
        vec![ReasoningOption::Effort { values: values.iter().map(|v| v.to_string()).collect() }]
    }

    /// The levels of a row on a channel that can send everything.
    fn levels(options: &[ReasoningOption]) -> Vec<ThinkingLevel> {
        let mut levels = offered_by_row(&derive_thinking(options).0, "openai-chat", Some(crate::model::ThinkingFormat::Qwen));
        levels.sort();
        levels
    }

    #[test]
    fn effort_values_are_the_levels() {
        use ThinkingLevel::*;
        assert_eq!(levels(&effort(&["low", "high", "max"])), [Low, High, Max]);
        assert_eq!(levels(&effort(&["none", "low", "medium", "high", "xhigh"])), [Off, Low, Medium, High, Xhigh]);
        assert_eq!(levels(&effort(&["low", "turbo"])), [Low], "values her has no level for are left out");
        assert_eq!(levels(&effort(&["none"])), [Off]);
    }

    #[test]
    fn switches_and_budgets() {
        use ThinkingLevel::*;
        assert_eq!(levels(&[ReasoningOption::Toggle]), [Off, On]);
        // DeepSeek: a switch and three efforts.
        assert_eq!(levels(&[ReasoningOption::Toggle, effort(&["low", "high", "max"])[0].clone()]), [Off, Low, High, Max]);
        // A budget offers levels; "off" only if it can be switched off.
        let budget = ReasoningOption::BudgetTokens { min: Some(2_048), max: Some(160_000) };
        assert_eq!(levels(std::slice::from_ref(&budget)), [Low, Medium, High, Max]);
        assert_eq!(levels(&[ReasoningOption::Toggle, budget.clone()]), [Off, Low, Medium, High, Max]);
        let budgets = derive_thinking(&[budget]).1.unwrap();
        assert_eq!((budgets.minimal, budgets.high), (Some(2_048), Some(24_576)));
        assert_eq!(budgets.max, Some(160_000), "the highest level is as much as the model takes");
        assert_eq!(derive_thinking(&[]), (CatalogThinking::default(), None));
        assert_eq!(levels(&[]), [] as [ThinkingLevel; 0]);
    }

    #[test]
    fn a_channel_offers_what_it_can_send() {
        use crate::model::ThinkingFormat as F;
        use ThinkingLevel::*;
        let of = |options: &[ReasoningOption], api: &str, format: Option<F>| {
            let mut levels = offered_by_row(&derive_thinking(options).0, api, format);
            levels.sort();
            levels
        };
        let toggle_effort = [ReasoningOption::Toggle, effort(&["low", "high"])[0].clone()];
        let budget = [ReasoningOption::Toggle, ReasoningOption::BudgetTokens { min: None, max: Some(100_000) }];
        // Efforts go out on any channel; its switch only where it is known.
        assert_eq!(of(&toggle_effort, "openai-chat", None), [Low, High]);
        assert_eq!(of(&toggle_effort, "openai-chat", Some(F::Deepseek)), [Off, Low, High]);
        assert_eq!(of(&effort(&["none", "high"]), "openai-chat", None), [Off, High], "`none` is itself an effort");
        // Budgets and a bare switch need a channel that can say them.
        assert_eq!(of(&budget, "openai-chat", None), [] as [ThinkingLevel; 0]);
        assert_eq!(of(&budget, "openai-chat", Some(F::Deepseek)), [Off, On]);
        assert_eq!(of(&budget, "openai-chat", Some(F::Qwen)), [Off, Low, Medium, High, Max]);
        assert_eq!(of(&[ReasoningOption::Toggle], "openai-chat", None), [] as [ThinkingLevel; 0]);
        // An older Claude thinks only when given a budget; a newer one cannot always be turned off.
        assert_eq!(of(&[ReasoningOption::BudgetTokens { min: Some(1_024), max: None }], "anthropic-messages", None), [Off, Low, Medium, High, Max]);
        assert_eq!(of(&effort(&["low", "max"]), "anthropic-messages", None), [Low, Max]);
    }

    #[test]
    fn a_row_that_names_no_control_offers_none_and_written_levels_win() {
        use ThinkingLevel::*;
        let catalog = Catalog::from_toml(
            r#"
            [providers.p]
            api = "openai-chat"
            base_url = "https://example.invalid/v1"
            compat = { thinking_format = "qwen" }

            [providers.r]
            api = "openai-chat"
            base_url = "https://example.invalid/v1"
            compat = { thinking_format = "openrouter" }

            [providers.n]
            api = "openai-chat"
            base_url = "https://example.invalid/v1"
            compat = { thinking_format = "none" }
            "#,
        )
        .unwrap();
        let (p, r, n) = (&catalog.providers["p"], &catalog.providers["r"], &catalog.providers["n"]);
        let row = PublicModel { id: "m".into(), reasoning: Some(true), ..PublicModel::default() };
        let thinks = ModelToml { reasoning: Some(true), ..ModelToml::new("m") };
        let levels = |p: &ProviderConfig, row: Option<&PublicModel>, entry: Option<&ModelToml>| {
            build(p, "m", row.map(Row::Own), None, entry).model.thinking.clone()
        };
        assert_eq!(levels(p, Some(&row), None), [] as [ThinkingLevel; 0], "thinks, and cannot be told");
        assert_eq!(levels(r, Some(&row), None), [Off, Low, Medium, High], "an aggregator translates");
        assert_eq!(levels(p, None, Some(&thinks)), [Off, On], "written by hand: what the format can say");
        let written = ModelToml { thinking: Some(vec![High, Low]), ..thinks.clone() };
        assert_eq!(levels(p, Some(&row), Some(&written)), [Low, High]);
        assert_eq!(levels(n, Some(&row), Some(&written)), [] as [ThinkingLevel; 0], "the channel refuses it");
    }

    fn provider(entries: Vec<ModelToml>) -> ProviderConfig {
        let mut p = Catalog::from_toml(
            r#"
            [providers.p]
            api = "openai-chat"
            base_url = "https://example.invalid/v1"
            api_key_env = "P_KEY"
            currency = "CNY"
            catalog_id = "deepseek"
            compat = { thinking_format = "deepseek" }
            "#,
        )
        .unwrap()
        .providers
        .remove("p")
        .unwrap();
        p.listing.entries = entries;
        p
    }

    fn public_model(id: &str, released: &str) -> PublicModel {
        PublicModel {
            id: id.into(),
            name: Some(format!("Public {id}")),
            context_window: Some(64_000),
            max_output_tokens: Some(8_000),
            reasoning: Some(true),
            reasoning_options: effort(&["low", "high"]),
            input: vec!["text".into()],
            released: Some(released.into()),
            cost: Some(ModelCost { input: 1.0, output: 2.0, ..Default::default() }),
            ..Default::default()
        }
    }

    #[test]
    fn layers_merge_field_by_field() {
        let p = provider(vec![ModelToml {
            id: "a".into(),
            name: Some("Written".into()),
            cost: Some(ModelCost { input: 7.0, output: 8.0, ..Default::default() }),
            ..Default::default()
        }]);
        let listed = ListedModel { id: "a".into(), context_window: Some(99_000), ..Default::default() };
        let built = build(&p, "a", Some(Row::Own(&public_model("a", "2026-01-01"))), Some(&listed), p.listing.entries.first());
        let m = &built.model;
        assert_eq!(m.name, "Written");
        assert_eq!(m.context_window, 99_000, "the list's window beats the catalog's");
        assert_eq!(m.max_output_tokens, Some(8_000), "the catalog fills what the others leave");
        assert!(m.reasoning);
        assert_eq!(m.thinking, [ThinkingLevel::Low, ThinkingLevel::High]);
        assert_eq!((m.cost.input, m.currency), (7.0, Currency::Cny), "a written price is in the provider's currency");
        assert!(built.meta.known && built.meta.configured);
        let plain = build(&p, "b", Some(Row::Own(&public_model("b", "2026-01-01"))), None, None);
        assert_eq!((plain.model.cost.input, plain.model.currency), (1.0, Currency::Usd), "catalog prices are dollars");
        let unknown = build(&p, "c", None, Some(&ListedModel { id: "c".into(), ..Default::default() }), None);
        assert!(!unknown.meta.known);
        assert_eq!(unknown.model.context_window, crate::model::DEFAULT_CONTEXT_WINDOW);
        let refuses_tools = build(&p, "d", None, Some(&ListedModel { id: "d".into(), tool_call: Some(false), ..Default::default() }), None);
        assert!(!refuses_tools.meta.chat);
        // A name alone is no knowledge of the model: it stays in the menu, marked unknown.
        let named = ModelToml { name: Some("Named".into()), ..ModelToml::new("e") };
        let named = build(&p, "e", None, None, Some(&named));
        assert!(!named.meta.known && named.meta.configured);
        assert_eq!(named.model.max_output_tokens, None);
    }

    #[test]
    fn a_maker_row_lends_what_the_model_can_do() {
        let p = provider(vec![]);
        let mut row = public_model("x", "2026-01-01");
        row.deprecated = true;
        let own = build(&p, "x", Some(Row::Own(&row)), None, None);
        assert_eq!(own.model.cost.input, 1.0);
        assert!(own.meta.deprecated);
        let borrowed = build(&p, "x", Some(Row::Maker(&row)), None, None);
        assert_eq!(borrowed.model.context_window, 64_000);
        assert_eq!(borrowed.model.thinking, [ThinkingLevel::Low, ThinkingLevel::High]);
        assert!(borrowed.meta.known);
        assert!(!borrowed.model.cost.is_configured(), "the maker's price is not this channel's");
        assert_eq!(borrowed.model.currency, Currency::Cny);
        assert!(!borrowed.meta.deprecated, "nor is its retirement");
    }

    #[test]
    fn a_channel_without_a_row_finds_the_makers_row() {
        let catalog = Catalog::builtin().unwrap();
        let directory = ModelDirectory::new(None);
        let tencent = &catalog.providers["tencent-token-plan"];
        // Its own row, then each maker, as this channel calls the models.
        let model = |id: &str| directory.model(tencent, id).unwrap();
        assert_eq!(model("hy3").context_window, 192_000, "the channel's own row, input limit included");
        assert_eq!(model("glm-5.3").context_window, 1_000_000);
        assert_eq!(model("glm-5.3").max_output_tokens, Some(131_072));
        assert!(model("kimi-k3").accepts_images());
        assert!(model("deepseek/deepseek-flash").reasoning, "without the maker's prefix");
        assert!(model("minimax-m3").accepts_images(), "whatever the case");
        // Requests say what the channel calls it.
        assert_eq!(model("deepseek/deepseek-flash").id, "deepseek/deepseek-flash");
        assert_eq!(model("minimax-m3").id, "minimax-m3");
        // A routing name is not a model: unknown, whatever an aggregator calls by it.
        let merged = directory.models(tencent);
        let auto = merged.get("tc-code-latest").unwrap();
        assert!(!auto.meta.known && auto.meta.configured);
        assert_eq!(auto.model.max_output_tokens, None);
        assert_eq!(merged.models.iter().filter(|m| m.meta.known).count(), 9);
        // The same answers through either way of asking.
        assert_eq!(directory.model(tencent, "kimi-k3"), Some(merged.get("kimi-k3").unwrap().model.clone()));
        assert!(directory.model(tencent, "not-in-the-plan").is_none());
        // Without a row anywhere, the id alone: a model of an open channel is still resolvable.
        assert!(directory.model(&catalog.providers["deepseek"], "brand-new").is_none());
    }

    #[test]
    fn an_account_connections_catalog_wins_and_the_maker_fills_the_gaps() {
        let mut p = provider(vec![
            ModelToml { context_window: Some(272_000), ..ModelToml::new("gpt-6-sol") },
            ModelToml { context_window: Some(1_000_000), ..ModelToml::new("flash") },
        ]);
        p.auth = crate::AuthConfig::Managed;
        p.listing.catalog_id = None;
        p.listing.path = None;
        let directory = ModelDirectory::new(None);
        let sol = directory.model(&p, "gpt-6-sol").unwrap();
        assert_eq!(sol.context_window, 272_000);
        assert_eq!(sol.max_output_tokens, Some(128_000), "the maker's, since the catalog does not say");
        assert!(!sol.cost.is_configured(), "a subscription is not billed per token");
        let flash = directory.model(&p, "flash").unwrap();
        assert_eq!(flash.max_output_tokens, None, "no row: left to the provider");
        assert!(directory.model(&p, "gpt-6-luna").is_none(), "only the models of the connection");
    }

    #[test]
    fn a_maker_row_newer_than_this_build_is_found() {
        let dir = tempfile::tempdir().unwrap();
        let mut newer = crate::public_catalog::load(None, "deepseek").unwrap();
        newer.fetched_at = "2099-01-01T00:00:00Z".into();
        newer.models.push(public_model("deepseek-v9", "2099-01-01"));
        let catalog = dir.path().join("catalog");
        std::fs::create_dir_all(&catalog).unwrap();
        std::fs::write(catalog.join("deepseek.json"), crate::public_catalog::to_file(&newer)).unwrap();
        assert!(!crate::public_catalog::compiled_maker_has("deepseek-v9"));
        let catalogs = Catalog::builtin().unwrap();
        let tencent = &catalogs.providers["kimi"];
        // Asked the way a command asks, and the way a whole list is merged.
        let directory = ModelDirectory::new(Some(dir.path().to_path_buf()));
        assert_eq!(directory.model(tencent, "deepseek-v9").unwrap().context_window, 64_000);
        assert!(directory.model(tencent, "no-such-model").is_none());
        directory.invalidate();
        let mut seeded = tencent.clone();
        seeded.listing.entries.push(ModelToml::new("deepseek-v9"));
        assert_eq!(directory.models(&seeded).get("deepseek-v9").unwrap().model.context_window, 64_000);
    }

    #[test]
    fn the_list_decides_which_models_exist() {
        let dir = tempfile::tempdir().unwrap();
        let directory = ModelDirectory::new(Some(dir.path().to_path_buf()));
        let p = provider(vec![ModelToml { id: "mine".into(), ..Default::default() }]);
        // No list yet: the catalog's models, and the written one.
        let from_catalog = directory.models(&p);
        assert_eq!(from_catalog.source, IdSource::Catalog);
        assert!(from_catalog.get("deepseek-flash").is_some() && from_catalog.get("mine").is_some());
        let list = ModelList {
            version: 1,
            fetched_at: Some(crate::discovery::now()),
            checked_at: crate::discovery::now(),
            source: ListSource::Provider,
            credential: None,
            error: None,
            models: vec![ListedModel { id: "deepseek-flash".into(), ..Default::default() }, ListedModel { id: "brand-new".into(), ..Default::default() }],
        };
        crate::discovery::write_json(&dir.path().join("lists/p.json"), &list).unwrap();
        directory.invalidate();
        let listed = directory.models(&p);
        assert_eq!(listed.source, IdSource::Provider);
        let ids: Vec<_> = listed.models.iter().map(|m| m.model.id.as_str()).collect();
        assert_eq!(ids, ["deepseek-flash", "brand-new", "mine"]);
        assert!(listed.get("deepseek-flash").unwrap().meta.known);
        assert!(!listed.get("brand-new").unwrap().meta.known);
        // A provider sharing the list reads the same file.
        let mut alternate = provider(vec![]);
        alternate.id = "alt".into();
        alternate.listing = Listing { from: Some("p".into()), catalog_id: Some("deepseek".into()), ..Default::default() };
        assert_eq!(directory.models(&alternate).models.len(), 2);
    }

    #[test]
    fn plans_use_only_their_own_membership_and_never_public_prices() {
        let dir = tempfile::tempdir().unwrap();
        let directory = ModelDirectory::new(Some(dir.path().to_path_buf()));
        let mut p = provider(vec![ModelToml::new("deepseek-flash")]);
        p.plan = Some(crate::catalog::PlanConfig { key_url: "https://example.invalid/plan".into() });
        let seeds = directory.models(&p);
        assert_eq!(seeds.source, IdSource::Config);
        assert_eq!(seeds.models.len(), 1, "do not import the full API catalog");
        let m = &seeds.get("deepseek-flash").unwrap().model;
        assert!(m.context_window > 100_000, "still enrich model capabilities");
        assert!(!m.cost.is_configured());
        assert!(m.cost_tiers.is_empty());
        directory.invalidate();
        assert!(directory.model(&p, "deepseek-v4-pro").is_none(), "single-model lookup also respects plan membership");

        let mut list = ModelList {
            version: 1, fetched_at: Some(crate::discovery::now()), checked_at: crate::discovery::now(),
            source: ListSource::Provider, credential: None, error: None,
            models: vec![ListedModel { id: "new-plan-model".into(), ..Default::default() }],
        };
        crate::discovery::write_json(&dir.path().join("lists/p.json"), &list).unwrap();
        directory.invalidate();
        let live = directory.models(&p);
        assert_eq!(live.source, IdSource::Provider);
        assert_eq!(live.models.len(), 1);
        assert!(live.get("new-plan-model").is_some());
        assert!(directory.model(&p, "deepseek-flash").is_none(), "removed seeds stay removed");

        list.models.clear();
        crate::discovery::write_json(&dir.path().join("lists/p.json"), &list).unwrap();
        directory.invalidate();
        assert!(directory.models(&p).models.is_empty(), "an empty successful list is authoritative too");
    }

    #[test]
    fn default_shown_is_the_newest_known_models() {
        let p = provider(vec![ModelToml { id: "mine".into(), ..Default::default() }]);
        let mut models: Vec<DirectoryModel> = (0..20)
            .map(|i| build(&p, &format!("m{i:02}"), Some(Row::Own(&public_model(&format!("m{i:02}"), &format!("2026-01-{:02}", i + 1)))), None, None))
            .collect();
        let mut old = public_model("old", "2026-12-31");
        old.deprecated = true;
        models.push(build(&p, "old", Some(Row::Own(&old)), None, None));
        models.push(build(&p, "unknown", None, None, None));
        models.push(build(&p, "mine", None, None, p.listing.entries.first()));
        let pm = ProviderModels { source: IdSource::Catalog, list: None, models };
        let shown = pm.default_shown(false);
        assert_eq!(shown.len(), DEFAULT_SHOWN);
        assert!(shown.contains("mine") && shown.contains("m19") && !shown.contains("m08"));
        assert!(!shown.contains("old") && !shown.contains("unknown"));
        assert_eq!(pm.default_shown(true), BTreeSet::from(["mine".to_string()]));
    }

    #[test]
    fn selection_keeps_only_changes() {
        let dir = tempfile::tempdir().unwrap();
        let directory = ModelDirectory::new(Some(dir.path().to_path_buf()));
        directory.set_shown("p/a", false, true).unwrap();
        directory.set_shown("p/b", true, false).unwrap();
        directory.set_shown("p/c", true, true).unwrap();
        let reread = ModelDirectory::new(Some(dir.path().to_path_buf())).selection();
        assert_eq!(reread.hide, BTreeSet::from(["p/a".to_string()]));
        assert_eq!(reread.show, BTreeSet::from(["p/b".to_string()]));
        assert!(!reread.is_shown("p/a", true) && reread.is_shown("p/b", false) && reread.is_shown("p/new", true));
        directory.set_shown("p/a", true, true).unwrap();
        assert!(directory.selection().hide.is_empty());
    }
}
