//! The machine-readable description of the native protocol: a JSON Schema (draft 7) whose
//! `definitions` cover every type, plus which method takes and returns which type, the
//! notifications and the error codes. `her protocol schema` prints it; clients generate
//! their types from it (for example TypeScript with json-schema-to-typescript).

use schemars::JsonSchema;
use schemars::r#gen::SchemaGenerator;
use schemars::r#gen::SchemaSettings;
use serde_json::Map;
use serde_json::Value;
use serde_json::json;

use crate::PROTOCOL_VERSION;
use crate::api;
use crate::api::error_code;
use crate::api::method;
use crate::bot;
use crate::customize;
use crate::deck;
use crate::docx;
use crate::event::Event;
use crate::files;
use crate::op::Op;
use crate::web_search;

/// Registers `T` and returns its definition name.
fn name<T: JsonSchema>(generator: &mut SchemaGenerator) -> String {
    generator.subschema_for::<T>();
    T::schema_name()
}

/// The schema document. Object keys are sorted, so the output is stable.
pub fn document() -> Value {
    let mut generator = SchemaSettings::draft07().into_generator();
    let g = &mut generator;
    let methods: Vec<(&str, Option<String>, String)> = vec![
        (
            method::INITIALIZE,
            Some(name::<api::InitializeParams>(g)),
            name::<api::InitializeResult>(g),
        ),
        (
            method::DOC_OPEN,
            Some(name::<crate::doc::DocOpenParams>(g)),
            name::<crate::doc::DocOpenResult>(g),
        ),
        (
            method::DOC_CLOSE,
            Some(name::<crate::doc::DocIdParams>(g)),
            name::<api::Empty>(g),
        ),
        (
            method::DOC_VIEW,
            Some(name::<crate::doc::DocViewParams>(g)),
            name::<api::Empty>(g),
        ),
        (
            method::DOC_LOCATE,
            Some(name::<crate::doc::DocLocateParams>(g)),
            name::<crate::doc::DocLocateResult>(g),
        ),
        (
            method::DOC_RESOLVE,
            Some(name::<crate::doc::DocResolveParams>(g)),
            name::<crate::doc::DocResolveResult>(g),
        ),
        (
            method::DOC_CELLS,
            Some(name::<crate::doc::DocCellsParams>(g)),
            name::<crate::doc::DocCellsResult>(g),
        ),
        (
            method::DOC_APPLY,
            Some(name::<crate::doc::DocApplyParams>(g)),
            name::<crate::doc::DocApplyResult>(g),
        ),
        (
            method::DOC_UNDO,
            Some(name::<crate::doc::DocIdParams>(g)),
            name::<crate::doc::DocApplyResult>(g),
        ),
        (
            method::DOC_REDO,
            Some(name::<crate::doc::DocIdParams>(g)),
            name::<crate::doc::DocApplyResult>(g),
        ),
        (
            method::DOC_RECALC,
            Some(name::<crate::doc::DocRecalcParams>(g)),
            name::<crate::doc::DocApplyResult>(g),
        ),
        (
            method::DOC_TRACE,
            Some(name::<crate::doc::DocTraceParams>(g)),
            name::<crate::doc::DocFoundResult>(g),
        ),
        (
            method::DOC_FIND,
            Some(name::<crate::doc::DocFindParams>(g)),
            name::<crate::doc::DocFoundResult>(g),
        ),
        (
            method::DOC_FILL,
            Some(name::<crate::doc::DocFillParams>(g)),
            name::<crate::doc::DocApplyResult>(g),
        ),
        (
            method::DOC_STRUCTURE,
            Some(name::<crate::doc::DocStructureParams>(g)),
            name::<crate::doc::DocApplyResult>(g),
        ),
        (
            method::DOC_SORT,
            Some(name::<crate::doc::DocSortParams>(g)),
            name::<crate::doc::DocApplyResult>(g),
        ),
        (
            method::DOC_AUTOFILL,
            Some(name::<crate::doc::DocAutofillParams>(g)),
            name::<crate::doc::DocApplyResult>(g),
        ),
        (
            method::DOC_SAVE,
            Some(name::<crate::doc::DocSaveParams>(g)),
            name::<crate::doc::DocInfo>(g),
        ),
        (
            method::SESSION_NEW,
            Some(name::<api::SessionNewParams>(g)),
            name::<api::SessionNewResult>(g),
        ),
        (
            method::SESSION_RESUME,
            Some(name::<api::SessionResumeParams>(g)),
            name::<api::SnapshotResult>(g),
        ),
        (
            method::SESSION_ATTACH,
            Some(name::<api::SessionAttachParams>(g)),
            name::<api::SnapshotResult>(g),
        ),
        (
            method::SESSION_DETACH,
            Some(name::<api::SessionIdParams>(g)),
            name::<api::Empty>(g),
        ),
        (
            method::SESSION_OP,
            Some(name::<api::SessionOpParams>(g)),
            name::<api::Empty>(g),
        ),
        (
            method::SESSION_HISTORY,
            Some(name::<api::SessionHistoryParams>(g)),
            name::<api::TranscriptPage>(g),
        ),
        (
            method::SESSION_CONTEXT,
            Some(name::<api::SessionIdParams>(g)),
            name::<crate::ContextReport>(g),
        ),
        (
            method::SESSION_LIST,
            Some(name::<api::SessionListParams>(g)),
            name::<api::SessionListResult>(g),
        ),
        (
            method::SESSION_CLOSE,
            Some(name::<api::SessionIdParams>(g)),
            name::<api::Empty>(g),
        ),
        (
            method::SESSION_DELETE,
            Some(name::<api::SessionIdParams>(g)),
            name::<api::Empty>(g),
        ),
        (
            method::RESOURCE_READ,
            Some(name::<api::ResourceReadParams>(g)),
            name::<api::ResourceReadResult>(g),
        ),
        (method::MODELS_LIST, None, name::<api::ModelsListResult>(g)),
        (method::SERVER_SHUTDOWN, None, name::<api::Empty>(g)),
        (
            method::SESSIONS_SUBSCRIBE,
            Some(name::<api::SessionListParams>(g)),
            name::<api::SessionListResult>(g),
        ),
        (method::SESSIONS_UNSUBSCRIBE, None, name::<api::Empty>(g)),
        (
            method::SESSION_RENAME,
            Some(name::<api::SessionRenameParams>(g)),
            name::<api::Empty>(g),
        ),
        (
            method::SESSION_ARCHIVE,
            Some(name::<api::SessionIdParams>(g)),
            name::<api::Empty>(g),
        ),
        (
            method::SESSION_UNARCHIVE,
            Some(name::<api::SessionIdParams>(g)),
            name::<api::Empty>(g),
        ),
        (
            method::AUTH_SET,
            Some(name::<api::AuthSetParams>(g)),
            name::<api::AuthSetResult>(g),
        ),
        (
            method::MODELS_REFRESH,
            Some(name::<api::ModelsRefreshParams>(g)),
            name::<api::ModelsRefreshResult>(g),
        ),
        (
            method::MODELS_SHOW,
            Some(name::<api::ModelsShowParams>(g)),
            name::<api::Empty>(g),
        ),
        (
            method::AUTH_DELETE,
            Some(name::<api::AuthDeleteParams>(g)),
            name::<api::Empty>(g),
        ),
        (method::CHATGPT_LOGIN, Some(name::<api::ChatGptLoginParams>(g)), name::<api::ChatGptStatus>(g)),
        (method::CHATGPT_COMPLETE, Some(name::<api::ChatGptCompleteParams>(g)), name::<api::ChatGptStatus>(g)),
        (method::CHATGPT_STATUS, None, name::<api::ChatGptStatus>(g)),
        (method::CHATGPT_CANCEL, Some(name::<api::ChatGptCancelParams>(g)), name::<api::ChatGptStatus>(g)),
        (method::CHATGPT_LOGOUT, None, name::<api::ChatGptStatus>(g)),
        (method::CONNECTIONS_LIST, None, name::<api::ConnectionsSnapshot>(g)),
        (method::CONNECTIONS_DISCOVER, None, name::<api::ConnectionsDiscoveryResult>(g)),
        (method::CONNECTIONS_CONNECT, Some(name::<api::ConnectionConnectParams>(g)), name::<api::ConnectionsSnapshot>(g)),
        (method::CONNECTIONS_RETRY, Some(name::<api::ConnectionIdParams>(g)), name::<api::ConnectionsSnapshot>(g)),
        (method::CONNECTIONS_DISCONNECT, Some(name::<api::ConnectionIdParams>(g)), name::<api::ConnectionsSnapshot>(g)),
        (method::CONNECTIONS_REMOVE, Some(name::<api::ConnectionIdParams>(g)), name::<api::ConnectionsSnapshot>(g)),
        (method::CONNECTIONS_MODELS, Some(name::<api::ConnectionIdParams>(g)), name::<api::ConnectionsSnapshot>(g)),
        (method::SKILLS_LIST, Some(name::<api::SkillsListParams>(g)), name::<api::SkillsListResult>(g)),
        (method::TOOLS_LIST, Some(name::<api::ToolsListParams>(g)), name::<api::ToolsListResult>(g)),
        (method::CONFIG_READ, Some(name::<customize::ConfigReadParams>(g)), name::<customize::ConfigReadResult>(g)),
        (method::CONFIG_WRITE, Some(name::<customize::ConfigWriteParams>(g)), name::<customize::ConfigWriteResult>(g)),
        (method::CREDENTIALS_SET, Some(name::<customize::CredentialsSetParams>(g)), name::<api::Empty>(g)),
        (method::CREDENTIALS_DELETE, Some(name::<customize::CredentialsDeleteParams>(g)), name::<api::Empty>(g)),
        (method::SKILLS_FILES, Some(name::<customize::SkillsFilesParams>(g)), name::<customize::SkillsFilesResult>(g)),
        (method::SKILLS_READ, Some(name::<customize::SkillsReadParams>(g)), name::<customize::SkillsReadResult>(g)),
        (method::SKILLS_INSTALL, Some(name::<customize::SkillsInstallParams>(g)), name::<customize::SkillsInstallResult>(g)),
        (method::SKILLS_REMOVE, Some(name::<customize::SkillsRemoveParams>(g)), name::<api::Empty>(g)),
        (method::SKILLS_SET_ENABLED, Some(name::<customize::SkillsSetEnabledParams>(g)), name::<customize::SkillsSetEnabledResult>(g)),
        (method::MCP_LIST, Some(name::<customize::McpListParams>(g)), name::<customize::McpListResult>(g)),
        (method::MCP_TOOLS, Some(name::<customize::McpServerParams>(g)), name::<customize::McpToolsResult>(g)),
        (method::MCP_CHECK, Some(name::<customize::McpServerParams>(g)), name::<customize::McpCheckResult>(g)),
        (method::MCP_PROBE, Some(name::<customize::McpProbeParams>(g)), name::<customize::McpProbeResult>(g)),
        (method::MCP_PROBE_CANCEL, Some(name::<customize::McpProbeCancelParams>(g)), name::<api::Empty>(g)),
        (method::MCP_LOGIN, Some(name::<customize::McpLoginParams>(g)), name::<customize::McpLoginResult>(g)),
        (method::MCP_LOGIN_CANCEL, Some(name::<customize::McpLogoutParams>(g)), name::<api::Empty>(g)),
        (method::MCP_LOGIN_COMPLETE, Some(name::<customize::McpLoginCompleteParams>(g)), name::<api::Empty>(g)),
        (method::CONNECTORS_INSTALL, Some(name::<customize::ConnectorInstallParams>(g)), name::<customize::ConnectorInstallResult>(g)),
        (method::MCP_LOGOUT, Some(name::<customize::McpLogoutParams>(g)), name::<api::Empty>(g)),
        (method::WEB_SEARCH_STATUS, None, name::<web_search::WebSearchStatus>(g)),
        (method::WEB_SEARCH_TEST, Some(name::<web_search::WebSearchTestParams>(g)), name::<web_search::WebSearchTestResult>(g)),
        (method::WEB_SEARCH_SET_KEY, Some(name::<web_search::WebSearchSetKeyParams>(g)), name::<web_search::WebSearchSetKeyResult>(g)),
        (method::BROWSER_HOST, Some(name::<api::BrowserHostParams>(g)), name::<api::BrowserHostResult>(g)),
        (method::DOCX_HOST, None, name::<api::Empty>(g)),
        (method::BROWSER_STATUS, None, name::<api::BrowserStatus>(g)),
        (method::BROWSER_SITE_ICON, Some(name::<api::BrowserSiteIconParams>(g)), name::<api::BrowserSiteIconResult>(g)),
        (method::DESKTOP_STATUS, None, name::<api::DesktopStatus>(g)),
        (method::DATA_EXPORT, None, name::<api::DataExportResult>(g)),
        (method::BROWSER_REVEAL, Some(name::<api::BrowserRevealParams>(g)), name::<api::Empty>(g)),
        (method::BROWSER_CLOSE_TABS, Some(name::<api::BrowserCloseTabsParams>(g)), name::<api::Empty>(g)),
        (method::BROWSER_ALLOW, Some(name::<api::BrowserSessionParams>(g)), name::<api::Empty>(g)),
        (method::BROWSER_CANCEL_WAIT, Some(name::<api::BrowserSessionParams>(g)), name::<api::Empty>(g)),
        (method::BROWSER_USE_EXTENSION, Some(name::<api::BrowserUseExtensionParams>(g)), name::<api::Empty>(g)),
        (method::BROWSER_ASSIGN_TAB, Some(name::<api::BrowserAssignTabParams>(g)), name::<api::Empty>(g)),
        (method::FILES_LIST, Some(name::<files::FilesListParams>(g)), name::<files::FilesListResult>(g)),
        (method::FILES_READ, Some(name::<files::FilesReadParams>(g)), name::<files::FilesReadResult>(g)),
        (method::PPTX_READ, Some(name::<crate::pptx::PptxReadParams>(g)), name::<crate::pptx::PptxReadResult>(g)),
        (method::PPTX_WRITE, Some(name::<crate::pptx::PptxWriteParams>(g)), name::<files::FilesWriteResult>(g)),
        (method::PPTX_METAFILE, Some(name::<crate::pptx::PptxMetafileParams>(g)), name::<crate::pptx::PptxMetafileResult>(g)),
        (method::DECK_OPEN, Some(name::<deck::DeckOpenParams>(g)), name::<deck::DeckOpenResult>(g)),
        (method::DECK_CLOSE, Some(name::<deck::DeckIdParams>(g)), name::<api::Empty>(g)),
        (method::DECK_SYNC, Some(name::<deck::DeckSyncParams>(g)), name::<deck::DeckSyncResult>(g)),
        (method::DECK_SAVE, Some(name::<deck::DeckSaveParams>(g)), name::<deck::DeckSaveResult>(g)),
        (method::DECK_SUMMARY, Some(name::<deck::DeckSummaryParams>(g)), name::<deck::DeckSummary>(g)),
        (method::DECK_READ, Some(name::<deck::DeckReadParams>(g)), name::<deck::DeckReadResult>(g)),
        (method::DECK_FIND, Some(name::<deck::DeckFindParams>(g)), name::<deck::DeckFindResult>(g)),
        (method::DECK_VIEW, Some(name::<deck::DeckViewParams>(g)), name::<deck::DeckViewResult>(g)),
        (method::DECK_FOCUS, Some(name::<deck::DeckFocusParams>(g)), name::<api::Empty>(g)),
        (method::DECK_EDIT, Some(name::<deck::DeckEditParams>(g)), name::<deck::DeckEditResult>(g)),
        (method::DECK_COPY, Some(name::<deck::DeckCopyParams>(g)), name::<deck::DeckCopyResult>(g)),
        (method::DECK_UNDO, Some(name::<deck::DeckUndoParams>(g)), name::<deck::DeckUndoResult>(g)),
        (method::DECK_PROPOSAL_LIST, Some(name::<deck::DeckProposalListParams>(g)), name::<deck::DeckProposalListResult>(g)),
        (method::DECK_PROPOSAL_ACCEPT, Some(name::<deck::DeckProposalDecideParams>(g)), name::<deck::DeckProposalResult>(g)),
        (method::DECK_PROPOSAL_REJECT, Some(name::<deck::DeckProposalDecideParams>(g)), name::<deck::DeckProposalResult>(g)),
        (method::DECK_PROPOSAL_REVERT, Some(name::<deck::DeckProposalIdParams>(g)), name::<deck::DeckProposalResult>(g)),
        (method::DECK_CHECK, Some(name::<deck::DeckCheckParams>(g)), name::<deck::DeckCheckResult>(g)),
        (method::DECK_LAYOUTS, Some(name::<deck::DeckLayoutsParams>(g)), name::<deck::DeckLayoutsResult>(g)),
        (method::DECK_TEMPLATE_PROFILE, Some(name::<deck::DeckTemplateProfileParams>(g)), name::<deck::TemplateProfile>(g)),
        (method::DECK_LINKS, Some(name::<deck::DeckLinksParams>(g)), name::<deck::DeckLinksResult>(g)),
        (method::DECK_DIFF, Some(name::<deck::DeckDiffParams>(g)), name::<deck::DeckDiff>(g)),
        (method::AI_ACTION, Some(name::<deck::AiActionParams>(g)), name::<deck::AiActionResult>(g)),
        (method::DOCX_OPEN, Some(name::<docx::DocxOpenParams>(g)), name::<docx::DocxOpenResult>(g)),
        (method::DOCX_SYNC, Some(name::<docx::DocxSyncParams>(g)), name::<docx::DocxSyncResult>(g)),
        (method::DOCX_SAVE, Some(name::<docx::DocxSaveParams>(g)), name::<docx::DocxSaveResult>(g)),
        (method::DOCX_CLOSE, Some(name::<docx::DocxIdParams>(g)), name::<api::Empty>(g)),
        (method::DOCX_UNDO, Some(name::<docx::DocxUndoParams>(g)), name::<docx::DocxRevisionResult>(g)),
        (method::DOCX_REVIEW, Some(name::<docx::DocxReviewParams>(g)), name::<docx::DocxRevisionResult>(g)),
        (method::FILES_WRITE, Some(name::<files::FilesWriteParams>(g)), name::<files::FilesWriteResult>(g)),
        (method::FILES_WATCH, Some(name::<files::FilesWatchParams>(g)), name::<api::Empty>(g)),
        (method::BOTS_SUBSCRIBE, None, name::<bot::BotsResult>(g)),
        (method::BOTS_UNSUBSCRIBE, None, name::<api::Empty>(g)),
        (method::BOTS_CREATE, Some(name::<bot::BotCreateParams>(g)), name::<bot::BotResult>(g)),
        (method::BOTS_UPDATE, Some(name::<bot::BotUpdateParams>(g)), name::<bot::BotResult>(g)),
        (method::BOTS_AVATAR, Some(name::<bot::BotIdParams>(g)), name::<bot::BotAvatarResult>(g)),
        (method::BOTS_OPEN, Some(name::<bot::BotOpenParams>(g)), name::<api::SessionNewResult>(g)),
        (method::BOTS_READ, Some(name::<bot::BotIdParams>(g)), name::<api::Empty>(g)),
        (method::BOTS_UNREAD, Some(name::<bot::BotIdParams>(g)), name::<api::Empty>(g)),
        (method::BOTS_CLEAR, Some(name::<bot::BotIdParams>(g)), name::<api::Empty>(g)),
        (method::BOTS_DELETE, Some(name::<bot::BotIdParams>(g)), name::<api::Empty>(g)),
        (method::BOTS_ROUTINE, Some(name::<bot::BotRoutineParams>(g)), name::<bot::BotRoutineResult>(g)),
        (method::BOTS_ROUTINE_RUNS, Some(name::<bot::BotRoutineRunsParams>(g)), name::<bot::BotRoutineRunsResult>(g)),
        (method::BOTS_ROUTINE_RUN, Some(name::<bot::BotRoutineRunParams>(g)), name::<bot::BotRoutineRunResult>(g)),
        (method::BOTS_ROUTINE_SET_ENABLED, Some(name::<bot::BotRoutineSetEnabledParams>(g)), name::<api::Empty>(g)),
        (method::BOTS_ROUTINE_RUN_NOW, Some(name::<bot::BotRoutineParams>(g)), name::<api::Empty>(g)),
        (method::BOTS_ROUTINE_STOP, Some(name::<bot::BotRoutineParams>(g)), name::<api::Empty>(g)),
        (method::BOTS_ROUTINE_DELETE, Some(name::<bot::BotRoutineParams>(g)), name::<api::Empty>(g)),
    ];
    let client_notifications = [
        (method::CANCEL_REQUEST, name::<api::CancelRequestParams>(g)),
        (method::BROWSER_CDP, name::<api::BrowserCdpParams>(g)),
        (method::BROWSER_EXTENSION, name::<api::ExtensionMessage>(g)),
    ];
    let server_notifications = [
        (method::MODELS_CHANGED, name::<api::Empty>(g)),
        (method::BROWSER_CHANGED, name::<api::BrowserStatus>(g)),
        (method::BROWSER_REQUEST, name::<api::BrowserRequest>(g)),
        (method::BROWSER_CONTROL, name::<api::BrowserControl>(g)),
        (method::CONFIG_CHANGED, name::<customize::ConfigChangedParams>(g)),
        (method::SKILLS_CHANGED, name::<customize::SkillsChangedParams>(g)),
        (method::MCP_CHANGED, name::<customize::McpChangedParams>(g)),
        (method::MCP_PROBE_PROGRESS, name::<customize::McpProbeProgressParams>(g)),
        (method::PPTX_READ_PROGRESS, name::<crate::pptx::PptxReadProgress>(g)),
        (method::FILES_CHANGED, name::<files::FilesChangedParams>(g)),
        (method::DECK_SYNC, name::<deck::DeckSyncNotice>(g)),
        (method::DECK_CHANGED, name::<deck::DeckChangedParams>(g)),
        (method::DECK_PROPOSAL, name::<deck::DeckProposalEvent>(g)),
        (method::AI_ACTION_PROGRESS, name::<deck::AiActionProgress>(g)),
        (method::DOCX_SYNC, name::<docx::DocxSyncNotice>(g)),
        (method::DOCX_CHANGED, name::<docx::DocxChangedParams>(g)),
        (method::DOC_CHANGED, name::<crate::doc::DocChangedParams>(g)),
        (method::BOTS_CHANGED, name::<bot::BotsChangedParams>(g)),
        (method::SESSION_EVENT, name::<api::SessionEventParams>(g)),
        (method::BROWSER_CDP, name::<api::BrowserCdpParams>(g)),
        (
            method::SESSION_DETACHED,
            name::<api::SessionDetachedParams>(g),
        ),
        (
            method::SESSIONS_CHANGED,
            name::<api::SessionsChangedParams>(g),
        ),
    ];
    // Requests the server sends to a client, which answers them.
    let server_requests = [(method::DOCX_SERIALIZE, name::<api::DocxSerializeParams>(g), name::<api::DocxSerializeResult>(g))];
    // Also used on their own: `her exec --output stream-json` lines and `--ops-stdin` lines.
    // The deck's rule ids, for clients to name checks (`DeckDiagnostic.rule` is a string).
    let standalone = [name::<Event>(g), name::<Op>(g), name::<deck::DiagnosticRule>(g)];

    let reference = |name: &str| json!({ "$ref": format!("#/definitions/{name}") });
    let mut methods_doc = Map::new();
    let mut roots = Vec::new();
    for (method, params, result) in &methods {
        let mut entry = Map::new();
        if let Some(params) = params {
            entry.insert("params".into(), json!(params));
            roots.push(params.clone());
        }
        entry.insert("result".into(), json!(result));
        roots.push(result.clone());
        methods_doc.insert(method.to_string(), Value::Object(entry));
    }
    let notifications = |list: &[(&str, String)]| -> Value {
        Value::Object(
            list.iter()
                .map(|(m, n)| (m.to_string(), json!(n)))
                .collect(),
        )
    };
    roots.extend(server_requests.iter().flat_map(|(_, p, r)| [p.clone(), r.clone()]));
    roots.extend(client_notifications.iter().map(|(_, n)| n.clone()));
    roots.extend(server_notifications.iter().map(|(_, n)| n.clone()));
    roots.extend(standalone.iter().cloned());
    roots.sort();
    roots.dedup();
    let errors: Map<String, Value> = error_code::ALL
        .iter()
        .map(|(code, kind)| (code.to_string(), json!(kind)))
        .collect();
    let definitions = serde_json::to_value(generator.take_definitions()).unwrap_or_default();
    sorted(json!({
        "$schema": "http://json-schema.org/draft-07/schema#",
        "title": "her native protocol",
        "description": "JSON-RPC 2.0 over newline-delimited JSON. Every message type is under `definitions`; the root accepts any top-level payload. `x-methods` maps each request to its params and result types, `x-client-notifications` and `x-server-notifications` map notifications to their params, `x-server-requests` maps the requests the server sends to their params and result types, and `x-error-codes` maps error codes to the `kind` in the error data.",
        "x-protocol-version": PROTOCOL_VERSION,
        "x-methods": methods_doc,
        "x-client-notifications": notifications(&client_notifications),
        "x-server-notifications": notifications(&server_notifications),
        "x-server-requests": server_requests.iter().map(|(m, p, r)| (m.to_string(), json!({"params": p, "result": r}))).collect::<Map<String, Value>>(),
        "x-error-codes": errors,
        "x-stability": {
            "methods": methods.iter().map(|(m, ..)| (m.to_string(), json!(stability(m)))).collect::<Map<String, Value>>(),
            "notifications": client_notifications.iter().chain(&server_notifications).map(|(m, _)| (m.to_string(), json!(stability(m)))).collect::<Map<String, Value>>(),
            "server_requests": server_requests.iter().map(|(m, ..)| (m.to_string(), json!(stability(m)))).collect::<Map<String, Value>>(),
        },
        "anyOf": roots.iter().map(|n| reference(n)).collect::<Vec<_>>(),
        "definitions": definitions,
    }))
}

/// How much a method or notification can be relied on (standalone-cli-design §9.2). `stable`
/// follows the compatibility rules; `experimental` may change with any version; `internal` is for
/// the desktop app and the extension host.
pub fn stability(name: &str) -> &'static str {
    const INTERNAL: &[&str] = &["browser/host", "browser/cdp", "browser/extension", "browser/control", "browser/request", "docx/host", "docx/serialize"];
    const EXPERIMENTAL: &[&str] = &["connections/", "connectors/", "doc/", "docx/", "pptx/", "bots/", "browser/", "routines/", "bot/", "connector/", "desktop/", "data/"];
    if INTERNAL.contains(&name) {
        "internal"
    } else if EXPERIMENTAL.iter().any(|prefix| name.starts_with(prefix)) {
        "experimental"
    } else {
        "stable"
    }
}

/// What a schema `new` breaks of what `old` promised as stable: the same names, and the same
/// shapes for what those names take and return (§9.3).
pub fn broken_promises(old: &Value, new: &Value) -> Vec<String> {
    let mut problems = Vec::new();
    let mut roots: Vec<String> = vec!["Event".to_string(), "Op".to_string()];
    for (table, kinds) in [("x-methods", "methods"), ("x-client-notifications", "notifications"), ("x-server-notifications", "notifications")] {
        let Some(entries) = old[table].as_object() else { continue };
        for (name, entry) in entries {
            if old["x-stability"][kinds][name] != "stable" {
                continue;
            }
            let Some(now) = new[table].get(name) else {
                problems.push(format!("{name} is gone"));
                continue;
            };
            if now != entry {
                problems.push(format!("{name} changed its types: {entry} is now {now}"));
            }
            match entry {
                Value::String(notification) => roots.push(notification.clone()),
                _ => roots.extend(["params", "result"].iter().filter_map(|key| entry[*key].as_str().map(str::to_string))),
            }
        }
    }
    let mut seen = std::collections::BTreeSet::new();
    while let Some(name) = roots.pop() {
        if !seen.insert(name.clone()) {
            continue;
        }
        let definition = &old["definitions"][&name];
        collect_references(definition, &mut roots);
        match new["definitions"].get(&name) {
            Some(now) => shape_problems(&name, definition, now, &mut problems),
            None => problems.push(format!("the type {name} is gone")),
        }
    }
    problems
}

fn collect_references(value: &Value, out: &mut Vec<String>) {
    match value {
        Value::Object(map) => {
            if let Some(reference) = map.get("$ref").and_then(Value::as_str).and_then(|r| r.strip_prefix("#/definitions/")) {
                out.push(reference.to_string());
            }
            map.values().for_each(|v| collect_references(v, out));
        }
        Value::Array(items) => items.iter().for_each(|v| collect_references(v, out)),
        _ => {}
    }
}

/// Adding is allowed: an optional field, a variant. Taking away, or changing a type, is not; a
/// field that becomes required breaks whoever sends the old form.
fn shape_problems(path: &str, old: &Value, new: &Value, out: &mut Vec<String>) {
    if old.get("type") != new.get("type") && old.get("type").is_some() {
        out.push(format!("{path} changed type: {} is now {}", old["type"], new["type"]));
    }
    if old.get("$ref").is_some() && old.get("$ref") != new.get("$ref") {
        out.push(format!("{path} now refers to {} instead of {}", new["$ref"], old["$ref"]));
    }
    if let (Some(old_values), Some(new_values)) = (old["enum"].as_array(), new["enum"].as_array()) {
        for value in old_values {
            if !new_values.contains(value) {
                out.push(format!("{path} lost the value {value}"));
            }
        }
    }
    if let Some(properties) = old["properties"].as_object() {
        for (name, shape) in properties {
            match new["properties"].get(name) {
                Some(now) => shape_problems(&format!("{path}.{name}"), shape, now, out),
                None => out.push(format!("{path}.{name} is gone")),
            }
        }
    }
    let required = |value: &Value| -> Vec<String> {
        value["required"].as_array().map(|list| list.iter().filter_map(|v| v.as_str().map(str::to_string)).collect()).unwrap_or_default()
    };
    let before = required(old);
    for name in required(new) {
        if !before.contains(&name) && old["properties"].get(&name).is_some() {
            out.push(format!("{path}.{name} became required"));
        } else if !before.contains(&name) && old["properties"].is_object() {
            out.push(format!("{path}.{name} is new and required"));
        }
    }
    if let Some(old_variants) = old["oneOf"].as_array().or(old["anyOf"].as_array()) {
        let tag = |variant: &Value| variant["properties"]["type"]["enum"][0].as_str().map(str::to_string);
        let new_variants = new["oneOf"].as_array().or(new["anyOf"].as_array());
        for variant in old_variants {
            let Some(name) = tag(variant) else { continue };
            match new_variants.and_then(|all| all.iter().find(|v| tag(v).as_deref() == Some(&name))) {
                Some(now) => shape_problems(&format!("{path}<{name}>"), variant, now, out),
                None => out.push(format!("{path} lost the variant {name}")),
            }
        }
    }
}

/// The value with object keys in sorted order, whatever order serde_json keeps maps in.
fn sorted(value: Value) -> Value {
    match value {
        Value::Object(map) => {
            let mut entries: Vec<(String, Value)> = map.into_iter().collect();
            entries.sort_by(|a, b| a.0.cmp(&b.0));
            Value::Object(entries.into_iter().map(|(k, v)| (k, sorted(v))).collect())
        }
        Value::Array(items) => Value::Array(items.into_iter().map(sorted).collect()),
        other => other,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::path::PathBuf;

    #[test]
    fn every_method_has_a_tier_and_the_counts_are_what_the_design_says() {
        let doc = document();
        let methods = doc["x-stability"]["methods"].as_object().unwrap();
        assert_eq!(methods.len(), doc["x-methods"].as_object().unwrap().len());
        assert_eq!(methods["tools/list"], "stable");
        assert_eq!(methods["session/op"], "stable");
        assert_eq!(methods["doc/open"], "experimental");
        assert_eq!(methods["pptx/read"], "experimental");
        assert_eq!(methods["browser/host"], "internal");
        assert_eq!(doc["x-stability"]["notifications"]["session/event"], "stable");
    }

    #[test]
    fn taking_away_a_promise_is_caught_and_adding_is_not() {
        let old = json!({
            "x-methods": { "a/b": { "params": "P", "result": "R" }, "doc/x": { "result": "R" } },
            "x-stability": { "methods": { "a/b": "stable", "doc/x": "experimental" }, "notifications": {} },
            "definitions": {
                "Event": {}, "Op": {},
                "P": { "type": "object", "properties": { "x": { "type": "string" } }, "required": ["x"] },
                "R": { "type": "object", "properties": { "y": { "$ref": "#/definitions/Y" } } },
                "Y": { "type": "string", "enum": ["one", "two"] }
            }
        });
        let mut grown = old.clone();
        grown["definitions"]["P"]["properties"]["z"] = json!({ "type": "string" });
        grown["definitions"]["Y"]["enum"] = json!(["one", "two", "three"]);
        grown["x-methods"]["c/d"] = json!({ "result": "R" });
        grown["x-methods"].as_object_mut().unwrap().remove("doc/x");
        assert_eq!(broken_promises(&old, &grown), Vec::<String>::new());

        let mut cut = old.clone();
        cut["x-methods"].as_object_mut().unwrap().remove("a/b");
        assert_eq!(broken_promises(&old, &cut), vec!["a/b is gone"]);
        let mut cut = old.clone();
        cut["definitions"]["Y"]["enum"] = json!(["one"]);
        assert!(broken_promises(&old, &cut)[0].contains("lost the value"));
        let mut cut = old.clone();
        cut["definitions"]["P"]["properties"]["x"]["type"] = json!("number");
        assert!(broken_promises(&old, &cut)[0].contains("changed type"));
        let mut cut = old.clone();
        cut["definitions"]["P"]["properties"].as_object_mut().unwrap().remove("x");
        assert!(broken_promises(&old, &cut).iter().any(|p| p.contains("P.x is gone")));
        let mut cut = old.clone();
        cut["definitions"]["R"]["required"] = json!(["y"]);
        assert!(broken_promises(&old, &cut)[0].contains("became required"));
    }

    /// `schema-released.json` is the schema of the last release; the release script copies it. What
    /// it promised has to hold until the protocol version changes (§9.3).
    #[test]
    fn what_the_last_release_promised_still_holds() {
        let path = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("schema-released.json");
        let Ok(text) = std::fs::read_to_string(&path) else { return };
        let old: Value = serde_json::from_str(&text).unwrap();
        let new = document();
        if old["x-protocol-version"] == new["x-protocol-version"] {
            assert_eq!(broken_promises(&old, &new), Vec::<String>::new());
        }
    }

    #[test]
    fn every_method_and_event_is_described() {
        let doc = document();
        let defs = doc["definitions"].as_object().unwrap();
        for (_, entry) in doc["x-methods"].as_object().unwrap() {
            for key in ["params", "result"] {
                if let Some(name) = entry.get(key).and_then(Value::as_str) {
                    assert!(defs.contains_key(name), "missing definition {name}");
                }
            }
        }
        // The event kinds are inlined into `Event`, next to `seq` and `ts`.
        let event = &defs["Event"];
        let kinds: Vec<&str> = event["oneOf"]
            .as_array()
            .unwrap()
            .iter()
            .filter_map(|v| v["properties"]["type"]["enum"][0].as_str())
            .collect();
        for kind in [
            "session_started",
            "tool_started",
            "tool_running",
            "task_completed",
            "session_closed",
        ] {
            assert!(kinds.contains(&kind), "Event lacks {kind}: {kinds:?}");
        }
        assert!(event["properties"].get("seq").is_some());
        assert_eq!(doc["x-error-codes"]["-32015"], "approval_not_pending");
    }

    /// The committed copy (`crates/her-protocol/schema.json`) must match the types. Run with
    /// HER_UPDATE_SNAPSHOTS=1 to rewrite it after a deliberate protocol change.
    #[test]
    fn committed_schema_matches_the_types() {
        let path = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("schema.json");
        let doc = document();
        if std::env::var_os("HER_UPDATE_SNAPSHOTS").is_some() {
            std::fs::write(&path, serde_json::to_string_pretty(&doc).unwrap() + "\n").unwrap();
            return;
        }
        let saved: Value =
            serde_json::from_str(&std::fs::read_to_string(&path).unwrap_or_default())
                .expect("schema.json exists; create it with HER_UPDATE_SNAPSHOTS=1");
        assert!(
            saved == doc,
            "schema.json is out of date; rerun with HER_UPDATE_SNAPSHOTS=1 and review the diff"
        );
    }
}
