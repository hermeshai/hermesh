//! Curated CLI integrations. Manifests are shipped with her, never executed as scripts.
//! Installation and authorization belong to the service; the adapter exposes CLI calls to
//! the existing MCP runtime. Provider credentials stay in the provider's isolated store.

use her_protocol::McpServerConfig;
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use std::{
    collections::BTreeMap,
    io::Read,
    path::{Path, PathBuf},
    process::Stdio,
    sync::Arc,
    time::Duration,
};
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt},
    process::{Child, Command},
    sync::{mpsc, oneshot},
};
use tokio_util::sync::CancellationToken;

const MAX_ARCHIVE: usize = 64 << 20;
const MAX_BINARY: u64 = 128 << 20;
const MAX_OUTPUT: u64 = 2 << 20;

#[derive(Clone, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Manifest {
    pub schema_version: u32,
    pub id: String,
    pub name: String,
    pub version: String,
    pub license: String,
    pub source: String,
    pub description: String,
    pub tool_description: String,
    config_env: String,
    storage_env: String,
    auth: Auth,
    blocked_commands: Vec<String>,
    blocked_flags: Vec<String>,
    artifacts: BTreeMap<String, Artifact>,
}
#[derive(Clone, Deserialize)]
#[serde(deny_unknown_fields)]
struct Auth {
    kind: String,
    login: Vec<String>,
    status: Vec<String>,
    #[serde(default)]
    logout: Vec<String>,
    #[serde(default)]
    logout_files: Vec<String>,
    #[serde(default)]
    status_field: String,
    #[serde(default = "default_status_value")]
    status_value: Value,
    #[serde(default)]
    status_text: bool,
    #[serde(default)]
    setup: Option<Setup>,
    #[serde(default)]
    url_field: Option<String>,
    #[serde(default)]
    partial_success: Option<PartialSuccess>,
    authorize_origin: String,
    authorize_path: String,
}
#[derive(Clone, Deserialize)]
#[serde(deny_unknown_fields)]
struct PartialSuccess {
    exit_code: i32,
    event: String,
    warning_type: String,
}

fn default_status_value() -> Value {
    Value::Bool(true)
}
#[derive(Clone, Deserialize)]
#[serde(deny_unknown_fields)]
struct Setup {
    command: Vec<String>,
    config_file: String,
    authorize_origin: String,
    authorize_path: String,
}
#[derive(Clone, Deserialize)]
#[serde(deny_unknown_fields)]
struct Artifact {
    url: String,
    sha256: String,
    entry: String,
    #[serde(default = "default_notices")]
    notices: Vec<String>,
}
fn default_notices() -> Vec<String> {
    vec!["LICENSE".into(), "NOTICE".into()]
}
#[derive(Serialize, Deserialize)]
struct Receipt {
    version: String,
    binary_sha256: String,
}

pub fn manifest(id: &str) -> Result<Manifest, String> {
    let raw = match id {
        "dingtalk" => include_str!("dingtalk.json"),
        "feishu" => include_str!("feishu.json"),
        "wecom" => include_str!("wecom.json"),
        _ => return Err("未知的托管连接器".into()),
    };
    let m: Manifest = serde_json::from_str(raw).map_err(|e| e.to_string())?;
    if m.schema_version != 1 || m.id != id || m.auth.kind != "cli_browser" {
        return Err("不支持的连接器配置版本".into());
    }
    Ok(m)
}
fn root(home: &Path, m: &Manifest) -> PathBuf {
    home.join("connectors").join(&m.id)
}
fn release(home: &Path, m: &Manifest) -> PathBuf {
    root(home, m).join("releases").join(&m.version)
}
fn binary(home: &Path, m: &Manifest) -> PathBuf {
    release(home, m).join("cli")
}
fn digest(bytes: &[u8]) -> String {
    ring::digest::digest(&ring::digest::SHA256, bytes)
        .as_ref()
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect()
}
fn private_dir(path: &Path) -> Result<(), String> {
    std::fs::create_dir_all(path).map_err(|e| e.to_string())?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(path, std::fs::Permissions::from_mode(0o700))
            .map_err(|e| e.to_string())?;
    }
    Ok(())
}

pub fn installed(home: &Path, m: &Manifest) -> bool {
    release(home, m).join("receipt.json").is_file() && binary(home, m).is_file()
}
fn verify(home: &Path, m: &Manifest) -> Result<(), String> {
    let receipt: Receipt = serde_json::from_slice(
        &std::fs::read(release(home, m).join("receipt.json")).map_err(|_| "请先安装连接器")?,
    )
    .map_err(|_| "连接器安装记录无效")?;
    let bytes = std::fs::read(binary(home, m)).map_err(|_| "连接器组件缺失，请重新安装")?;
    if receipt.version != m.version || digest(&bytes) != receipt.binary_sha256 {
        return Err("连接器组件校验失败，请重新安装".into());
    }
    Ok(())
}

#[derive(Debug)]
struct Bundle {
    binary: Vec<u8>,
    notices: BTreeMap<String, Vec<u8>>,
}
/// Extract the pinned program and its license notices into fixed destinations. Never unpack
/// paths, permissions, links or install scripts supplied by the archive.
fn unpack(bytes: &[u8], artifact: &Artifact) -> Result<Bundle, String> {
    if digest(bytes) != artifact.sha256 {
        return Err("下载文件 SHA-256 校验失败".into());
    }
    let mut archive = tar::Archive::new(flate2::read::GzDecoder::new(bytes));
    let mut found = None;
    let mut notices = BTreeMap::new();
    for entry in archive.entries().map_err(|e| e.to_string())? {
        let mut entry = entry.map_err(|e| e.to_string())?;
        let p = entry.path().map_err(|e| e.to_string())?;
        let p = p.strip_prefix(".").unwrap_or(&p);
        if artifact.notices.iter().any(|name| p == Path::new(name)) {
            let name = p
                .file_name()
                .ok_or("许可证文件名无效")?
                .to_string_lossy()
                .into_owned();
            if !entry.header().entry_type().is_file()
                || entry.size() > 1 << 20
                || notices.contains_key(&name)
            {
                return Err("连接器许可证文件无效".into());
            }
            let mut data = Vec::new();
            entry.read_to_end(&mut data).map_err(|e| e.to_string())?;
            notices.insert(name, data);
            continue;
        }
        if p != Path::new(&artifact.entry) {
            continue;
        }
        if found.is_some() || !entry.header().entry_type().is_file() || entry.size() > MAX_BINARY {
            return Err("连接器压缩包中的程序无效".into());
        }
        let mut data = Vec::new();
        entry.read_to_end(&mut data).map_err(|e| e.to_string())?;
        found = Some(data);
    }
    let binary = found
        .filter(|b| !b.is_empty())
        .ok_or("压缩包缺少连接器程序")?;
    if notices.len() != artifact.notices.len() {
        return Err("压缩包缺少许可证文件".into());
    }
    Ok(Bundle { binary, notices })
}

pub async fn install(home: &Path, id: &str) -> Result<Manifest, String> {
    let m = manifest(id)?;
    let platform = format!("{}-{}", std::env::consts::OS, std::env::consts::ARCH);
    let artifact = m
        .artifacts
        .get(&platform)
        .ok_or("这个连接器暂不支持当前系统")?;
    if verify(home, &m).is_ok() {
        return Ok(m);
    }
    let tls = her_llm::tls_client_config().map_err(|e| e.to_string())?;
    let http = reqwest::Client::builder()
        .use_preconfigured_tls(tls)
        .redirect(reqwest::redirect::Policy::custom(|a| {
            if a.previous().len() < 5 && a.url().scheme() == "https" {
                a.follow()
            } else {
                a.stop()
            }
        }))
        .connect_timeout(Duration::from_secs(20))
        .timeout(Duration::from_secs(180))
        .user_agent("Hermesh connector installer")
        .build()
        .map_err(|e| e.to_string())?;
    let mut response = http
        .get(&artifact.url)
        .send()
        .await
        .map_err(|e| format!("下载连接器失败：{e}"))?
        .error_for_status()
        .map_err(|e| e.to_string())?;
    if response
        .content_length()
        .is_some_and(|n| n > MAX_ARCHIVE as u64)
    {
        return Err("连接器下载文件过大".into());
    }
    let mut bytes = Vec::new();
    while let Some(chunk) = response.chunk().await.map_err(|e| e.to_string())? {
        if bytes.len() + chunk.len() > MAX_ARCHIVE {
            return Err("连接器下载文件过大".into());
        }
        bytes.extend_from_slice(&chunk);
    }
    let (home, copy, artifact) = (home.to_path_buf(), m.clone(), artifact.clone());
    tokio::task::spawn_blocking(move || publish(&home, &copy, &unpack(&bytes, &artifact)?))
        .await
        .map_err(|e| e.to_string())??;
    Ok(m)
}
fn publish(home: &Path, m: &Manifest, bundle: &Bundle) -> Result<(), String> {
    let bytes = &bundle.binary;
    let dir = release(home, m);
    private_dir(&dir)?;
    for (name, data) in &bundle.notices {
        std::fs::write(dir.join(name), data).map_err(|e| e.to_string())?;
    }
    let temp = dir.join(format!(".cli-{}", uuid::Uuid::new_v4()));
    std::fs::write(&temp, bytes).map_err(|e| e.to_string())?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(&temp, std::fs::Permissions::from_mode(0o700))
            .map_err(|e| e.to_string())?;
    }
    std::fs::rename(&temp, binary(home, m)).map_err(|e| e.to_string())?;
    let receipt = serde_json::to_vec(&Receipt {
        version: m.version.clone(),
        binary_sha256: digest(bytes),
    })
    .map_err(|e| e.to_string())?;
    let temp = dir.join("receipt.tmp");
    std::fs::write(&temp, receipt).map_err(|e| e.to_string())?;
    std::fs::rename(temp, dir.join("receipt.json")).map_err(|e| e.to_string())
}

pub fn server_config(home: &Path, m: &Manifest, executable: &Path) -> McpServerConfig {
    McpServerConfig {
        name: m.id.clone(),
        connector: Some(m.id.clone()),
        enabled: Some(false),
        command: Some(executable.to_string_lossy().into_owned()),
        args: vec![
            "connector".into(),
            "serve".into(),
            m.id.clone(),
            "--home".into(),
            home.to_string_lossy().into_owned(),
        ],
        description: Some(m.description.clone()),
        ..Default::default()
    }
}
/// A marker alone never grants access to a managed credential directory.
pub fn for_server(home: &Path, s: &McpServerConfig) -> Option<Manifest> {
    let m = manifest(s.connector.as_deref()?).ok()?;
    let exe = std::env::current_exe().ok()?;
    let expected = server_config(home, &m, &exe);
    (s.name == m.id
        && s.command == expected.command
        && s.args == expected.args
        && s.env.is_empty()
        && s.env_vars.is_empty()
        && s.url.is_none())
    .then_some(m)
}

/// Unauthenticated managed connectors never join a session, including legacy enabled configs.
pub fn authorized(home: &Path, server: &McpServerConfig) -> bool {
    server.connector.is_none() || for_server(home, server).is_some_and(|m| signed_in(home, &m))
}

fn command(home: &Path, m: &Manifest, args: &[String]) -> Result<Command, String> {
    verify(home, m)?;
    let dir = root(home, m);
    private_dir(&dir.join("config"))?;
    private_dir(&dir.join("credentials"))?;
    let mut c = Command::new(binary(home, m));
    c.env_clear();
    // Keep platform login/keychain and network settings, not ambient DWS credentials or agent
    // identity variables. In particular, never inherit DWS_DISABLE_KEYCHAIN.
    for key in [
        "HOME",
        "USER",
        "LOGNAME",
        "PATH",
        "LANG",
        "LC_ALL",
        "TMPDIR",
        "TMP",
        "TEMP",
        "SystemRoot",
        "HTTP_PROXY",
        "HTTPS_PROXY",
        "ALL_PROXY",
        "NO_PROXY",
        "http_proxy",
        "https_proxy",
        "all_proxy",
        "no_proxy",
        "DBUS_SESSION_BUS_ADDRESS",
        "XDG_RUNTIME_DIR",
    ] {
        if let Some(value) = std::env::var_os(key) {
            c.env(key, value);
        }
    }
    c.args(args)
        .env(&m.config_env, dir.join("config"))
        .env(&m.storage_env, dir.join("credentials"))
        .env("NO_COLOR", "1")
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .kill_on_drop(true);
    Ok(c)
}
async fn bounded(pipe: impl tokio::io::AsyncRead + Unpin) -> Result<Vec<u8>, String> {
    let mut bytes = Vec::new();
    pipe.take(MAX_OUTPUT + 1)
        .read_to_end(&mut bytes)
        .await
        .map_err(|e| e.to_string())?;
    if bytes.len() as u64 > MAX_OUTPUT {
        return Err("连接器输出过大，请缩小查询范围".into());
    }
    Ok(bytes)
}
async fn collect(mut child: Child, limit: Duration) -> Result<(bool, String, String), String> {
    let out = child.stdout.take().ok_or("没有标准输出")?;
    let err = child.stderr.take().ok_or("没有错误输出")?;
    tokio::time::timeout(limit, async {
        let (out, err, status) = tokio::try_join!(bounded(out), bounded(err), async {
            child.wait().await.map_err(|e| e.to_string())
        })?;
        Ok((
            status.success(),
            String::from_utf8_lossy(&out).into_owned(),
            String::from_utf8_lossy(&err).into_owned(),
        ))
    })
    .await
    .map_err(|_| "连接器执行超时".to_string())?
}
fn remember(home: &Path, m: &Manifest, authenticated: bool) {
    let _ = std::fs::write(
        root(home, m).join("auth-state.json"),
        if authenticated {
            b"true".as_slice()
        } else {
            b"false".as_slice()
        },
    );
}
pub fn signed_in(home: &Path, m: &Manifest) -> bool {
    installed(home, m)
        && std::fs::read(root(home, m).join("auth-state.json")).is_ok_and(|b| b == b"true")
}
pub async fn status(home: &Path, m: &Manifest) -> Result<bool, String> {
    if !installed(home, m) {
        return Ok(false);
    }
    let result = collect(
        command(home, m, &m.auth.status)?
            .spawn()
            .map_err(|e| e.to_string())?,
        Duration::from_secs(20),
    )
    .await?;
    let auth = result.0
        && if m.auth.status_text {
            Some(result.1.trim()) == m.auth.status_value.as_str()
        } else {
            let value: Value =
                serde_json::from_str(result.1.trim()).map_err(|_| "无法读取连接器登录状态")?;
            value.get(&m.auth.status_field) == Some(&m.auth.status_value)
        };
    remember(home, m, auth);
    Ok(auth)
}
pub fn partial_permissions(home: &Path, m: &Manifest) -> bool {
    signed_in(home, m)
        && std::fs::read(root(home, m).join("auth-partial.json")).is_ok_and(|b| b == b"true")
}
pub async fn logout(home: &Path, m: &Manifest) -> Result<(), String> {
    remember(home, m, false);
    let _ = std::fs::remove_file(root(home, m).join("auth-partial.json"));
    if installed(home, m) && !m.auth.logout.is_empty() {
        let (ok, _, _) = collect(
            command(home, m, &m.auth.logout)?
                .spawn()
                .map_err(|e| e.to_string())?,
            Duration::from_secs(30),
        )
        .await?;
        if !ok {
            return Err("退出登录失败，请重试".into());
        }
    }
    // Some providers have no logout command. Remove only their manifest-listed files
    // in our isolated config directory; do not touch another application's credentials.
    for name in &m.auth.logout_files {
        if Path::new(name).file_name().and_then(|n| n.to_str()) != Some(name.as_str()) {
            return Err("无效的凭据文件名".into());
        }
        match std::fs::remove_file(root(home, m).join("config").join(name)) {
            Ok(()) => {}
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
            Err(e) => return Err(e.to_string()),
        }
    }
    Ok(())
}

pub struct PendingLogin {
    pub url: String,
    updates: Option<mpsc::UnboundedReceiver<String>>,
    done: oneshot::Receiver<Result<(), String>>,
    _guard: tokio_util::sync::DropGuard,
}
impl PendingLogin {
    /// Later browser steps, if the provider requires application setup before user consent.
    pub fn take_updates(&mut self) -> mpsc::UnboundedReceiver<String> {
        self.updates.take().expect("login updates already taken")
    }
    pub async fn wait(self) -> Result<(), String> {
        let Self { done, _guard, .. } = self;
        let result = done.await.map_err(|_| "登录流程已结束".to_string())?;
        _guard.disarm();
        result
    }
}
fn allowed_url(origin: &str, path: &str, word: &str) -> Option<String> {
    let url = url::Url::parse(word).ok()?;
    (url.origin().ascii_serialization() == origin
        && url.path() == path
        && url.username().is_empty()
        && url.password().is_none())
    .then(|| url.to_string())
}
fn auth_url(origin: &str, path: &str, field: Option<&str>, text: &str) -> Option<String> {
    // A device-flow response can contain both a bare URL and a complete one with the
    // user code. Parse the configured field, never accidentally open the bare URL.
    if let Some(field) = field {
        return text
            .lines()
            .filter_map(|line| serde_json::from_str::<Value>(line).ok())
            .find_map(|v| {
                v.get(field)
                    .and_then(Value::as_str)
                    .and_then(|s| allowed_url(origin, path, s))
            });
    }
    text.split(|c: char| c.is_whitespace() || ['"', '\'', '<', '>'].contains(&c))
        .find_map(|word| allowed_url(origin, path, word))
}
/// A nonzero exit is accepted only when this pinned provider explicitly reports a
/// completed authorization with a scope warning. Status is independently checked next.
fn partial_success(rule: &PartialSuccess, code: Option<i32>, output: &[u8]) -> bool {
    if code != Some(rule.exit_code) {
        return false;
    }
    let text = String::from_utf8_lossy(output);
    let event = text
        .lines()
        .rev()
        .filter_map(|line| serde_json::from_str::<Value>(line).ok())
        .find(|v| v.get("event").is_some());
    event.is_some_and(|v| {
        v["event"] == rule.event
            && v["warning"]["type"] == rule.warning_type
            && v["user_open_id"].as_str().is_some_and(|s| !s.is_empty())
            && v["missing"].as_array().is_some_and(|a| !a.is_empty())
    })
}
async fn login_step(
    mut child: Child,
    origin: &str,
    path: &str,
    field: Option<&str>,
    tx: mpsc::UnboundedSender<String>,
    cancel: &CancellationToken,
    partial: Option<&PartialSuccess>,
) -> Result<bool, String> {
    let stdout = child.stdout.take().ok_or("无法读取登录输出")?;
    let stderr = child.stderr.take().ok_or("无法读取登录输出")?;
    let sent = Arc::new(std::sync::atomic::AtomicBool::new(false));
    let scan = |mut pipe: Box<dyn tokio::io::AsyncRead + Unpin + Send>| {
        let sent = sent.clone();
        let tx = tx.clone();
        async move {
            let mut buffer = Vec::new();
            let mut chunk = [0u8; 4096];
            loop {
                let n = pipe.read(&mut chunk).await.map_err(|e| e.to_string())?;
                if n == 0 {
                    break;
                }
                buffer.extend_from_slice(&chunk[..n]);
                if buffer.len() > 65536 {
                    return Err("登录输出异常".to_string());
                }
                let text = String::from_utf8_lossy(&buffer);
                if let Some(end) = text.rfind(char::is_whitespace)
                    && let Some(url) = auth_url(origin, path, field, &text[..end])
                    && !sent.swap(true, std::sync::atomic::Ordering::SeqCst)
                {
                    let _ = tx.send(url);
                }
            }
            Ok(buffer)
        }
    };
    let work = async {
        let (out, _, exit) =
            tokio::try_join!(scan(Box::new(stdout)), scan(Box::new(stderr)), async {
                child.wait().await.map_err(|e| e.to_string())
            })?;
        if exit.success() {
            return Ok(false);
        }
        if partial.is_some_and(|rule| partial_success(rule, exit.code(), &out)) {
            return Ok(true);
        }
        Err("授权未完成，请重试并检查账号和企业设置".into())
    };
    let result = tokio::select! {
        _ = cancel.cancelled() => Err("已取消登录".into()),
        r = tokio::time::timeout(Duration::from_secs(600), work) => r.unwrap_or_else(|_| Err("登录超时，请重试".into())),
    };
    let _ = child.kill().await;
    result
}
pub async fn login(
    home: PathBuf,
    m: Manifest,
    cancel: CancellationToken,
) -> Result<PendingLogin, String> {
    verify(&home, &m)?;
    let (tx, mut updates) = mpsc::unbounded_channel();
    let guard = cancel.clone().drop_guard();
    let token = cancel.clone();
    let (done_tx, done) = oneshot::channel();
    tokio::spawn(async move {
        let work = async {
            if let Some(setup) = &m.auth.setup
                && !root(&home, &m)
                    .join("config")
                    .join(&setup.config_file)
                    .is_file()
            {
                let child = command(&home, &m, &setup.command)?
                    .spawn()
                    .map_err(|e| e.to_string())?;
                login_step(
                    child,
                    &setup.authorize_origin,
                    &setup.authorize_path,
                    None,
                    tx.clone(),
                    &token,
                    None,
                )
                .await?;
            }
            if token.is_cancelled() {
                return Err("已取消登录".into());
            }
            let child = command(&home, &m, &m.auth.login)?
                .spawn()
                .map_err(|e| e.to_string())?;
            let partial = login_step(
                child,
                &m.auth.authorize_origin,
                &m.auth.authorize_path,
                m.auth.url_field.as_deref(),
                tx.clone(),
                &token,
                m.auth.partial_success.as_ref(),
            )
            .await?;
            if token.is_cancelled() {
                return Err("已取消登录".into());
            }
            if !status(&home, &m).await? {
                return Err("授权结束，但未检测到有效登录".into());
            }
            if token.is_cancelled() {
                return Err("已取消登录".into());
            }
            std::fs::write(
                root(&home, &m).join("auth-partial.json"),
                if partial { "true" } else { "false" },
            )
            .map_err(|e| e.to_string())?;
            Ok(())
        };
        let result = work.await;
        drop(tx);
        let _ = done_tx.send(result);
    });
    let url = tokio::select! {
        _ = cancel.cancelled() => Err("已取消登录".to_string()),
        result = tokio::time::timeout(Duration::from_secs(30), updates.recv()) => result.ok().flatten().ok_or_else(|| "无法获取授权链接，请重试".to_string()),
    };
    match url {
        Ok(url) => Ok(PendingLogin {
            url,
            updates: Some(updates),
            done,
            _guard: guard,
        }),
        Err(error) => {
            cancel.cancel();
            let _ = done.await;
            Err(error)
        }
    }
}

fn validate_args(m: &Manifest, args: &[String]) -> Result<(), String> {
    if args.is_empty() || args.len() > 256 || args.iter().map(String::len).sum::<usize>() > 65536 {
        return Err("请提供有效的命令参数数组".into());
    }
    // The first argument is a command, or help/version. Disallow leading global options so
    // they cannot disguise an account-management command later in the argument vector.
    if args[0].starts_with('-') && !matches!(args[0].as_str(), "--help" | "-h" | "--version") {
        return Err("请把子命令放在参数数组开头".into());
    }
    if args[0].starts_with('-') && args.len() != 1 {
        return Err("帮助和版本参数必须单独使用".into());
    }
    if m.blocked_commands.contains(&args[0])
        || args.iter().any(|a| {
            m.blocked_flags
                .iter()
                .any(|f| a == f || a.starts_with(&format!("{f}=")))
        })
    {
        return Err("账号、授权和组件管理请在连接器页面操作".into());
    }
    Ok(())
}
pub async fn execute(home: &Path, m: &Manifest, args: Vec<String>) -> Result<Value, String> {
    validate_args(m, &args)?;
    if !status(home, m).await? {
        return Err("连接器需要登录，请在连接器页面连接账号".into());
    }
    let (ok, out, err) = collect(
        command(home, m, &args)?
            .spawn()
            .map_err(|e| e.to_string())?,
        Duration::from_secs(120),
    )
    .await?;
    Ok(
        json!({"content":[{"type":"text","text": if out.trim().is_empty() {err} else {out}}],"isError":!ok}),
    )
}

/// A small stdio MCP boundary, keeping the rest of her's tool routing unchanged. Only the
/// curated program can run; argv is never interpolated into a shell or environment.
pub async fn serve<R, W>(home: &Path, id: &str, reader: R, mut writer: W) -> Result<(), String>
where
    R: tokio::io::AsyncRead + Unpin,
    W: tokio::io::AsyncWrite + Unpin,
{
    let m = manifest(id)?;
    if !status(home, &m).await? {
        return Err("连接器尚未授权，不能启动".into());
    }
    let mut reader = tokio::io::BufReader::new(reader);
    let mut bytes = Vec::new();
    let mut eof = false;
    let mut calls = tokio::task::JoinSet::new();
    let mut cancellations = BTreeMap::<String, CancellationToken>::new();
    use tokio::io::AsyncBufReadExt;
    loop {
        // Keep the partial input buffer across select! iterations; read_until is cancel-safe.
        let mut limited = (&mut reader).take((1 << 20) - bytes.len() as u64);
        let response = tokio::select! {
            completed = calls.join_next(), if !calls.is_empty() => {
                let (id, result) = completed.ok_or("MCP call ended")?.map_err(|e| e.to_string())?;
                cancellations.remove(&format!("{id}"));
                json!({"jsonrpc":"2.0","id":id,"result":result})
            }
            read = limited.read_until(b'\n', &mut bytes), if !eof => {
                let n = read.map_err(|e| e.to_string())?;
                if n == 0 && bytes.is_empty() { eof = true; continue; }
                if bytes.last() != Some(&b'\n') { return Err("MCP request too large or incomplete".into()); }
                let request: Value = serde_json::from_slice(&bytes).map_err(|e| e.to_string())?;
                bytes.clear();
                let method = request["method"].as_str().unwrap_or_default();
                if method == "notifications/cancelled" {
                    if let Some(token) = cancellations.get(&format!("{}", request["params"]["requestId"])) { token.cancel(); }
                    continue;
                }
                let Some(id) = request.get("id").cloned() else { continue; };
                let result = match method {
                    "initialize" => Ok(json!({"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":m.id,"version":m.version},"instructions":m.tool_description})),
                    "ping" => Ok(json!({})),
                    "tools/list" => Ok(json!({"tools":[{"name":"run","description":m.tool_description,"inputSchema":{"type":"object","properties":{"args":{"type":"array","items":{"type":"string"},"minItems":1,"maxItems":256}},"required":["args"],"additionalProperties":false}}]})),
                    "tools/call" if request["params"]["name"] == "run" => {
                        match serde_json::from_value::<Vec<String>>(request["params"]["arguments"]["args"].clone()) {
                            Ok(args) if calls.len() < 8 && !cancellations.contains_key(&format!("{id}")) => {
                                let token = CancellationToken::new();
                                cancellations.insert(format!("{id}"), token.clone());
                                let (home, m) = (home.to_path_buf(), m.clone());
                                calls.spawn(async move {
                                    let result = tokio::select! {
                                        biased;
                                        _ = token.cancelled() => Err("工具调用已取消".into()),
                                        result = execute(&home, &m, args) => result,
                                    };
                                    (id, result.unwrap_or_else(|e| json!({"content":[{"type":"text","text":e}],"isError":true})))
                                });
                                continue;
                            }
                            Ok(_) => Err((-32600, "too many calls or duplicate request id")),
                            Err(_) => Err((-32602, "args must be a string array")),
                        }
                    }
                    _ => Err((-32601, "method or tool not found")),
                };
                match result {
                    Ok(value) => json!({"jsonrpc":"2.0","id":id,"result":value}),
                    Err((code, message)) => json!({"jsonrpc":"2.0","id":id,"error":{"code":code,"message":message}}),
                }
            }
            else => return Ok(()),
        };
        let mut line = serde_json::to_vec(&response).map_err(|e| e.to_string())?;
        line.push(b'\n');
        writer.write_all(&line).await.map_err(|e| e.to_string())?;
        writer.flush().await.map_err(|e| e.to_string())?;
    }
}

#[cfg(test)]
mod tests;
