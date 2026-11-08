//! The methods behind the desktop's 自定义 page (docs/desktop/customize-design.md §6):
//! reading and writing the configuration, credentials, installing and removing skills,
//! and the state of MCP servers. The command line calls the same functions in her-core.

use schemars::JsonSchema;
use serde::Deserialize;
use serde::Serialize;
use std::collections::BTreeMap;
use std::path::PathBuf;

use crate::api::SkillEntry;

// ---- Configuration and credentials ----

/// A configuration layer, lowest priority first (design §4.3).
#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord, Hash, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum ConfigLayer {
    /// `~/.her/config.toml`
    User,
    /// `.mcp.json` at a trusted project's root: MCP servers only, never written by her.
    ProjectJson,
    /// `<project>/.her/config.toml`
    Project,
    /// `~/.her/projects/<key>/config.toml`: the user's own settings for one project.
    UserProject,
    /// `-c key=value` and `--mcp-config` on the command line.
    Flag,
}

impl ConfigLayer {
    pub fn as_str(self) -> &'static str {
        match self {
            ConfigLayer::User => "user",
            ConfigLayer::ProjectJson => "project_json",
            ConfigLayer::Project => "project",
            ConfigLayer::UserProject => "user_project",
            ConfigLayer::Flag => "flag",
        }
    }

    /// The layers her writes on the user's behalf.
    pub fn writable(self) -> bool {
        matches!(self, ConfigLayer::User | ConfigLayer::UserProject)
    }
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct ConfigReadParams {
    /// The directory whose project layers to include; absent: the user's file only.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub cwd: Option<PathBuf>,
    /// Also describe each layer: its file, version and whether her writes it.
    #[serde(default)]
    pub layers: bool,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct ConfigReadResult {
    /// The merged configuration as TOML would give it, `${credential:…}` references kept.
    pub config: serde_json::Value,
    /// The layer that last set each leaf, by dotted key path.
    pub origins: BTreeMap<String, ConfigLayer>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub layers: Vec<ConfigLayerInfo>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct ConfigLayerInfo {
    pub layer: ConfigLayer,
    pub file: PathBuf,
    /// The first 16 hex digits of the file's SHA-256, or "absent".
    pub version: String,
    pub writable: bool,
    pub exists: bool,
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum ConfigMerge {
    /// The value replaces whatever the key held.
    #[default]
    Replace,
    /// A table's fields are set one by one; fields not given stay.
    Upsert,
}

/// One change: a dotted key path such as `mcp_servers.github.enabled`, and its new value;
/// null removes the key.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct ConfigEdit {
    pub key_path: String,
    pub value: serde_json::Value,
    #[serde(default)]
    pub merge: ConfigMerge,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize, JsonSchema)]
pub struct ConfigWriteParams {
    /// `user` or `user_project`; the others are read-only.
    pub layer: ConfigLayer,
    /// Required for `user_project`: a directory in the project.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub cwd: Option<PathBuf>,
    pub edits: Vec<ConfigEdit>,
    /// The version `config/read` gave; the write fails if the file changed since.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub expected_version: Option<String>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum ConfigWriteStatus {
    Ok,
    /// Written, but a layer above sets some of the keys, so they do not take effect there.
    Overridden,
}

/// A written key that a higher layer sets too.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct ConfigOverride {
    pub key_path: String,
    pub layer: ConfigLayer,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub file: Option<PathBuf>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct ConfigWriteResult {
    pub status: ConfigWriteStatus,
    pub version: String,
    pub file: PathBuf,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub overridden_by: Vec<ConfigOverride>,
}

/// `config/changed`: a configuration file changed, written by her or by someone else.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct ConfigChangedParams {
    pub file: PathBuf,
}

/// A secret for configuration values to refer to as `${credential:<name>}`.
#[derive(Clone, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct CredentialsSetParams {
    /// Starts with `mcp.` or `web-search.`: 1-96 letters, digits, `.`, `_` or `-` after it.
    pub name: String,
    pub value: String,
}

impl std::fmt::Debug for CredentialsSetParams {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("CredentialsSetParams").field("name", &self.name).finish_non_exhaustive()
    }
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct CredentialsDeleteParams {
    pub name: String,
}

// ---- Skills ----

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum SkillSourceKind {
    /// From the application's catalog.
    Catalog,
    /// Picked by the user: a folder, an archive or a `SKILL.md`.
    Upload,
}

/// `.her-source.json` in a skill's directory: where her installed it from.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct SkillSource {
    pub kind: SkillSourceKind,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub version: Option<String>,
    /// Of the installed files; tells whether the user changed them since.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub sha256: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub installed_at: Option<String>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct SkillsFilesParams {
    /// The skill's directory or its `SKILL.md`.
    pub path: PathBuf,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct SkillFileInfo {
    /// Relative to the skill's directory.
    pub path: String,
    pub size: u64,
    pub dir: bool,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct SkillsFilesResult {
    pub skill_md: String,
    /// `skill_md` was cut at 256 KB.
    pub truncated: bool,
    /// At most 500 entries, 6 levels deep.
    pub files: Vec<SkillFileInfo>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct SkillsReadParams {
    /// The skill's directory or its `SKILL.md`.
    pub path: PathBuf,
    /// A file in it, as `skills/files` lists it.
    pub file: String,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct SkillsReadResult {
    /// Empty when the file is not text.
    pub text: String,
    /// `text` was cut at 256 KB.
    pub truncated: bool,
    /// Not UTF-8 text (an image, an archive, a program).
    pub binary: bool,
    /// The file is a PNG, JPEG, GIF or WebP image, by its signature: its bytes, to show.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub image: Option<SkillImage>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct SkillImage {
    pub mime_type: String,
    /// Base64.
    pub data: String,
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum SkillInstallScope {
    /// `~/.her/skills`
    #[default]
    User,
    /// `<project>/.her/skills`
    Project,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct SkillsInstallParams {
    /// A skill directory, a `SKILL.md`, or a `.zip` or `.skill` archive.
    pub from: PathBuf,
    #[serde(default)]
    pub scope: SkillInstallScope,
    /// Required for the project scope: a directory in the project.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub cwd: Option<PathBuf>,
    /// Recorded in `.her-source.json`; absent: an upload.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub source: Option<SkillSource>,
    /// Read and check only; write nothing.
    #[serde(default)]
    pub dry_run: bool,
    /// Replace a skill of the same name (moved to the trash first).
    #[serde(default)]
    pub replace: bool,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct SkillsInstallResult {
    /// As installed; for a dry run, as it would be.
    pub skill: SkillEntry,
    pub target: PathBuf,
    /// A dry run found a skill of the same name there.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub conflict: Option<PathBuf>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct SkillsRemoveParams {
    pub path: PathBuf,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct SkillsSetEnabledParams {
    /// The skill's `SKILL.md` or directory.
    pub path: PathBuf,
    pub enabled: bool,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct SkillsSetEnabledResult {
    /// The user's configuration file that now lists or no longer lists it.
    pub file: PathBuf,
    /// A name-form entry for the same skill was removed too.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub removed_names: Vec<String>,
}

/// `skills/changed`: a skill directory or a setting about skills changed.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct SkillsChangedParams {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub cwd: Option<PathBuf>,
}

// ---- MCP servers ----

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum McpTransportKind {
    Stdio,
    Http,
    /// The older HTTP+SSE transport.
    Sse,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum McpAuthKind {
    None,
    /// The configuration refers to saved credentials (`${credential:…}`).
    Credential,
    Oauth,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum McpAuthState {
    /// Every credential the configuration refers to is saved.
    Saved,
    /// At least one is not.
    Missing,
    LoggedIn,
    NeedsLogin,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum McpStatusState {
    Ok,
    Failed,
    /// Never connected with this configuration.
    Unknown,
    NeedsLogin,
    /// Defined in a project that is not trusted, so sessions do not start it.
    Untrusted,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum McpStatusSource {
    /// A session open now is connected to it.
    Session,
    /// The last connection's result (`~/.her/mcp/cache`).
    Cache,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct McpServerStatusView {
    pub state: McpStatusState,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub source: Option<McpStatusSource>,
    /// Open sessions connected to it.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub sessions: Option<u32>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub tools: Option<u32>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
    /// RFC 3339.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub checked_at: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub elapsed_ms: Option<u64>,
}

/// One server as the page shows it; secrets never appear, only the names they are saved under.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct McpServerView {
    /// Curated CLI connector, when installed through connectors/install.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub connector: Option<String>,
    pub name: String,
    /// The layer that defines the server (its command or URL).
    pub layer: ConfigLayer,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub file: Option<PathBuf>,
    /// Defined in a file her writes for the user.
    pub editable: bool,
    pub enabled: bool,
    /// When disabled: the layer that turned it off.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub disabled_by: Option<ConfigLayer>,
    pub transport: McpTransportKind,
    /// The command line, or the URL without its query.
    pub summary: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub description: Option<String>,
    pub auth: McpAuthKind,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub auth_state: Option<McpAuthState>,
    /// Authenticated, but the provider did not grant every requested permission.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub auth_partial: Option<bool>,
    /// The credentials its configuration refers to.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub credentials: Vec<String>,
    pub status: McpServerStatusView,
    /// Which service it reaches, whatever its name: two servers with the same identity are the
    /// same service (mcp-design §13.10).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub identity: Option<String>,
    /// The service's logo as a `data:` URI, when one was found (mcp-design §13.9).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub icon: Option<String>,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct McpListParams {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub cwd: Option<PathBuf>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct McpListResult {
    pub servers: Vec<McpServerView>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub warnings: Vec<String>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct McpServerParams {
    pub name: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub cwd: Option<PathBuf>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct McpToolSummary {
    pub name: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub title: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub description: Option<String>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum McpToolsSource {
    Session,
    Cache,
    None,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct McpToolsResult {
    pub tools: Vec<McpToolSummary>,
    pub source: McpToolsSource,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub checked_at: Option<String>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct McpCheckResult {
    pub server: McpServerView,
    pub tools: Vec<McpToolSummary>,
}

/// `mcp/changed`: a server's status, cached result or configuration changed; or a sign-in
/// started with `mcp/login` ended (`login_id`, with `login_error` when it failed).
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct McpChangedParams {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub name: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub login_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub login_error: Option<String>,
    /// A subsequent browser step. When present, this is progress, not completion.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub authorize_url: Option<String>,
}

/// `mcp/probe`: checks a server before it is added, writing nothing (mcp-design §13.6).
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct McpProbeParams {
    /// Chosen by the caller; progress notifications and `mcp/probe_cancel` use it.
    pub probe_id: String,
    /// The server as it would be written: secrets as `${credential:…}` references.
    pub server: crate::mcp::McpServerConfig,
    /// Working directory and root of a local command.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub cwd: Option<PathBuf>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct McpProbeCancelParams {
    pub probe_id: String,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum McpProbeStepId {
    /// Remote: the first request, without credentials.
    Connect,
    /// Remote: the protected resource metadata (RFC 9728).
    Resource,
    /// Remote: the authorization server's metadata (RFC 8414).
    AuthServer,
    /// Local: starting the command.
    Start,
    /// Local: the `initialize` handshake.
    Handshake,
    /// Local: `tools/list`.
    Tools,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum McpProbeStepState {
    Running,
    Ok,
    Failed,
    Skipped,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct McpProbeStep {
    pub id: McpProbeStepId,
    pub state: McpProbeStepState,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub detail: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub http_status: Option<u16>,
}

/// `mcp/probe_progress`: one step of a probe started or ended.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct McpProbeProgressParams {
    pub probe_id: String,
    pub step: McpProbeStep,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum McpProbeKind {
    Remote,
    Local,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum McpSuggestedAuth {
    Oauth,
    Token,
    None,
}

/// The OAuth client identity a sign-in would use (mcp-design D-182).
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "snake_case")]
pub enum McpOAuthClientKind {
    /// The managed CLI owns the provider's authorization flow.
    Managed,
    /// her's client metadata document.
    MetadataDocument,
    /// Dynamic client registration.
    Registration,
    /// The user's own client.
    Own,
}

/// Install a curated connector. No arbitrary download URLs or commands are accepted.
#[derive(Clone, Debug, Serialize, Deserialize, JsonSchema)]
#[serde(deny_unknown_fields)]
pub struct ConnectorInstallParams {
    pub id: String,
}

#[derive(Clone, Debug, Serialize, Deserialize, JsonSchema)]
pub struct ConnectorInstallResult {
    pub server: crate::McpServerConfig,
    pub version: String,
}

/// What the authorization server of a remote server supports.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct McpProbeOAuth {
    /// Its issuer.
    pub authorization_server: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub scopes: Vec<String>,
    /// PKCE with S256, which her requires.
    pub pkce: bool,
    pub metadata_document: bool,
    pub registration: bool,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct McpProbeAuth {
    /// The server answered 401.
    pub required: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub oauth: Option<McpProbeOAuth>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct McpProbeSuggestion {
    pub auth: McpSuggestedAuth,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub client: Option<McpOAuthClientKind>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct McpProbeResult {
    pub kind: McpProbeKind,
    pub steps: Vec<McpProbeStep>,
    /// Remote: the transport that answered.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub transport: Option<crate::mcp::McpRemoteTransport>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub auth: Option<McpProbeAuth>,
    pub suggested: McpProbeSuggestion,
    /// The server's tools, when the probe got that far.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub tools: Vec<McpToolSummary>,
    /// The service's logo as a `data:` URI: the icon the server declares, else its website's.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub icon: Option<String>,
    /// Why the probe failed; absent when it passed.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
}

/// `mcp/login`: starts signing in to a server. With `server`, the server is not in the
/// configuration yet: the token is saved under `name`, and adding it comes after. With
/// `redirect_uri`, the browser goes back to the client's device, and `mcp/login_complete` brings
/// what the redirect carried.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct McpLoginParams {
    pub name: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub cwd: Option<PathBuf>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub server: Option<crate::mcp::McpServerConfig>,
    /// The identity the person chose; used when the authorization server allows it, else the
    /// usual order (mcp-design D-182).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub client: Option<McpOAuthClientKind>,
    /// A loopback address on the client's device (RFC 8252): `http://127.0.0.1:<port>/…`,
    /// `http://[::1]:<port>/…` or `http://localhost:<port>/…`. Not for connectors that sign in
    /// through their own program.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub redirect_uri: Option<String>,
}

/// `mcp/login_complete`: finishes a sign-in started with a `redirect_uri`, with what its
/// redirect carried. It answers once the token is saved; `mcp/changed { login_id }` follows, as
/// for any sign-in.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct McpLoginCompleteParams {
    pub login_id: String,
    pub code: String,
    pub state: String,
    /// The redirect's `iss` (RFC 9207), when it has one: needed when the authorization server
    /// says it sends one.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub iss: Option<String>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct McpLoginResult {
    /// Open in the system browser; the sign-in ends with `mcp/changed { login_id }`.
    pub authorize_url: String,
    pub login_id: String,
    pub client: McpOAuthClientKind,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
pub struct McpLogoutParams {
    pub name: String,
}
