//! Platform reads and private credential storage. No source application file is written.
use crate::{ConnectionError, Result};
use async_trait::async_trait;
use cbc::cipher::{BlockDecryptMut, KeyIvInit, block_padding::Pkcs7};
use her_protocol::api::ConnectionDiscovery;
use serde_json::Value;
use std::{
    collections::BTreeMap,
    num::NonZeroU32,
    path::{Path, PathBuf},
    sync::Arc,
    time::Duration,
};
use tokio::{io::AsyncWriteExt, process::Command};
use zeroize::Zeroizing;

pub struct Login {
    pub subject: Option<String>,
    pub token: String,
    pub expires: i64,
    pub identity: String,
}
#[async_trait]
pub trait Environment: Send + Sync {
    async fn discover(&self) -> Result<ConnectionDiscovery>;
    async fn login(&self) -> Result<Login>;
    /// Only explicit settings actions may request an OS authorization prompt.
    async fn interactive_login(&self) -> Result<Login> {
        self.login().await
    }

    async fn headers(&self) -> Result<BTreeMap<String, String>>;
    async fn get(&self, id: &str) -> Result<Option<String>>;
    async fn set(&self, id: &str, value: &str) -> Result<()>;
    async fn delete(&self, id: &str) -> Result<()>;
    fn now(&self) -> i64 {
        chrono::Utc::now().timestamp_millis()
    }
}

pub struct System {
    namespace: String,
    product: &'static str,
}
pub fn system(home: &Path) -> Arc<dyn Environment> {
    system_for(home, "qwenwork")
}
pub fn system_for(home: &Path, product: &'static str) -> Arc<dyn Environment> {
    Arc::new(System {
        product,
        namespace: format!(
            "her-model-connections-{}",
            &digest(home.to_string_lossy().as_bytes())[..16]
        ),
    })
}
pub(crate) fn digest(value: &[u8]) -> String {
    ring::digest::digest(&ring::digest::SHA256, value)
        .as_ref()
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect()
}
impl System {
    fn app_path(&self) -> &'static str {
        if self.product == "qoder" {
            "/Applications/Qoder.app/Contents/Info.plist"
        } else {
            "/Applications/QwenWorkCN.app/Contents/Info.plist"
        }
    }
    fn auth_path(&self) -> Result<PathBuf> {
        if self.product == "qoder" {
            Ok(dirs::home_dir()
                .ok_or_else(ConnectionError::unsupported)?
                .join("Library/Application Support/com.qoder.app.stable/auth.v1.dat"))
        } else {
            auth_path()
        }
    }
    fn service(&self) -> &'static str {
        if self.product == "qoder" {
            "Qoder App Safe Storage"
        } else {
            "QwenWorkCN Safe Storage"
        }
    }
    async fn read_login(&self, interactive: bool) -> Result<Login> {
        supported()?;
        if !tokio::fs::try_exists(self.app_path()).await.map_err(|_| {
            ConnectionError::new("source_unreadable", "无法检查来源应用的安装信息").at("discovery")
        })? {
            return Err(ConnectionError::new("source_not_found", "来源应用不可用").at("discovery"));
        }
        tracing::debug!(stage = "login_file", "connection_local_stage_started");
        let file = self.auth_path()?;
        if tokio::fs::metadata(&file)
            .await
            .map_err(login_file_error)?
            .len()
            > 1024 * 1024
        {
            return Err(ConnectionError::new(
                "login_file_too_large",
                "来源应用登录文件超过大小限制",
            )
            .at("login_file"));
        }
        let sealed = tokio::fs::read(file).await.map_err(login_file_error)?;
        tracing::debug!(stage = "login_file", "connection_local_stage_completed");
        let keychain_started = std::time::Instant::now();
        if interactive {
            tracing::info!(stage = "keychain", "connection_stage_started");
        }
        let service = self.service();
        let span = tracing::Span::current();
        let password = tokio::task::spawn_blocking(move || {
            span.in_scope(|| source_password(service, interactive))
        })
        .await
        .map_err(|_| {
            ConnectionError::new("keychain_unavailable", "钥匙串读取任务失败").at("keychain")
        })??;
        if interactive {
            tracing::info!(
                stage = "keychain",
                elapsed_ms = keychain_started.elapsed().as_millis() as u64,
                "connection_stage_completed"
            );
        }
        tracing::debug!(stage = "decrypt", "connection_local_stage_started");
        let value = decrypt_value(&sealed, &password)?;
        tracing::debug!(stage = "decrypt", "connection_local_stage_completed");
        if self.product == "qoder" {
            crate::qoder::source_login(&value)
        } else {
            parse_qwen_login(&value)
        }
    }
}
fn qwen_headers(p: &Value) -> Result<BTreeMap<String, String>> {
    let read = |key: &str| {
        p[key]
            .as_str()
            .filter(|v| !v.is_empty())
            .filter(|v| reqwest::header::HeaderValue::from_str(v).is_ok())
            .ok_or_else(|| ConnectionError::format("app_metadata", key))
    };
    let version = read("CFBundleShortVersionString")?;
    let build = read("CFBundleVersion")?;
    Ok(BTreeMap::from([
        ("X-QwenWork-Version".into(), version.into()),
        (
            "X-QwenWork-Release-Version".into(),
            format!("{version}-{build}"),
        ),
        ("X-QwenWork-Build".into(), build.into()),
        ("X-QwenWork-Platform".into(), "darwin".into()),
        (
            "X-QwenWork-Arch".into(),
            if cfg!(target_arch = "aarch64") {
                "arm64"
            } else {
                "x64"
            }
            .into(),
        ),
        ("X-QwenWork-Channel".into(), "stable".into()),
    ]))
}
fn login_file_error(e: std::io::Error) -> ConnectionError {
    tracing::warn!(stage = "login_file", io_kind = ?e.kind(), os_error = ?e.raw_os_error(), "connection_local_io_failed");
    match e.kind() {
        std::io::ErrorKind::NotFound => ConnectionError::login().at("login_file"),
        std::io::ErrorKind::PermissionDenied => {
            ConnectionError::new("login_file_denied", "没有读取来源应用登录文件的权限")
                .at("login_file")
        }
        _ => ConnectionError::new("login_file_unreadable", "无法读取来源应用登录文件")
            .at("login_file"),
    }
}

fn auth_path() -> Result<PathBuf> {
    Ok(dirs::home_dir()
        .ok_or_else(|| ConnectionError::new("unsupported", "无法定位用户目录"))?
        .join("Library/Application Support/QwenWorkCN/auth-v2.dat"))
}
fn supported() -> Result<()> {
    if cfg!(target_os = "macos") {
        Ok(())
    } else {
        Err(ConnectionError::new("unsupported", "本机应用连接目前只支持 macOS").at("discovery"))
    }
}
async fn command(
    program: &str,
    args: &[&str],
    input: Option<&str>,
) -> Result<std::process::Output> {
    let stage = if program == "/usr/bin/plutil" {
        "app_metadata"
    } else {
        "credential_store"
    };
    let mut child = Command::new(program)
        .args(args)
        .kill_on_drop(true)
        .stdin(if input.is_some() {
            std::process::Stdio::piped()
        } else {
            std::process::Stdio::null()
        })
        .stdout(std::process::Stdio::piped())
        .stderr(std::process::Stdio::piped())
        .spawn()
        .map_err(|_| {
            ConnectionError::new("local_command_failed", "无法启动本地应用信息或凭据读取工具")
                .at(stage)
        })?;
    if let Some(value) = input {
        let mut stdin = child
            .stdin
            .take()
            .ok_or_else(ConnectionError::unavailable)?;
        stdin
            .write_all(value.as_bytes())
            .await
            .map_err(|_| ConnectionError::unavailable())?;
    }
    tokio::time::timeout(Duration::from_secs(30), child.wait_with_output())
        .await
        .map_err(|_| {
            ConnectionError::new(
                "local_command_timeout",
                "本地工具执行超时，请检查系统提示后重试",
            )
            .at(stage)
        })?
        .map_err(|_| ConnectionError::new("local_command_failed", "本地工具执行失败").at(stage))
}
#[async_trait]
impl Environment for System {
    async fn discover(&self) -> Result<ConnectionDiscovery> {
        let installed = cfg!(target_os = "macos")
            && tokio::fs::try_exists(self.app_path()).await.map_err(|_| {
                ConnectionError::new("source_unreadable", "无法检查来源应用的安装信息")
                    .at("discovery")
            })?;
        let version = if installed {
            let out = command(
                "/usr/bin/plutil",
                &[
                    "-extract",
                    "CFBundleShortVersionString",
                    "raw",
                    self.app_path(),
                ],
                None,
            )
            .await?;
            if out.status.success() {
                Some(String::from_utf8_lossy(&out.stdout).trim().to_string())
            } else {
                None
            }
        } else {
            None
        };
        Ok(ConnectionDiscovery {
            diagnostic: None,
            adapter_id: self.product.into(),
            installed,
            version,
            discovery_id: format!("{}-standard", self.product),
            login: if installed
                && tokio::fs::try_exists(self.auth_path()?)
                    .await
                    .map_err(login_file_error)?
            {
                "unknown"
            } else {
                "missing"
            }
            .into(),
        })
    }
    async fn login(&self) -> Result<Login> {
        self.read_login(false).await
    }
    async fn interactive_login(&self) -> Result<Login> {
        self.read_login(true).await
    }
    async fn headers(&self) -> Result<BTreeMap<String, String>> {
        supported()?;
        if self.product == "qoder" {
            // Qoder has no desktop version headers; validate its protocol on use.
            return Ok(BTreeMap::new());
        }
        let out = command(
            "/usr/bin/plutil",
            &["-convert", "json", "-o", "-", self.app_path()],
            None,
        )
        .await
        .map_err(|e| e.at("app_metadata"))?;
        if !out.status.success() {
            return Err(
                ConnectionError::new("app_metadata_unreadable", "无法读取来源应用信息")
                    .at("app_metadata"),
            );
        }
        let p: Value = serde_json::from_slice(&out.stdout).map_err(|_| {
            ConnectionError::new("app_metadata_invalid", "来源应用信息不是有效的 JSON 数据")
                .at("app_metadata")
        })?;
        qwen_headers(&p)
    }
    async fn get(&self, id: &str) -> Result<Option<String>> {
        supported()?;
        let out = command(
            "/usr/bin/security",
            &[
                "find-generic-password",
                "-s",
                &self.namespace,
                "-a",
                id,
                "-w",
            ],
            None,
        )
        .await?;
        match out.status.code() {
            Some(0) => Ok(Some(
                String::from_utf8_lossy(&out.stdout).trim().to_string(),
            )),
            Some(44) => Ok(None),
            _ => Err(ConnectionError::new(
                "connection_permission_required",
                "无法读取连接服务钥匙串",
            )
            .at("credential_store")),
        }
    }
    async fn set(&self, id: &str, value: &str) -> Result<()> {
        supported()?;
        if id.contains(['\n', '\r', '\0']) || value.contains(['\n', '\r', '\0']) {
            return Err(ConnectionError::unsupported());
        }
        let quote = |v: &str| format!("\"{}\"", v.replace('\\', "\\\\").replace('"', "\\\""));
        let input = Zeroizing::new(format!(
            "add-generic-password -U -s {} -a {} -w {}\n",
            quote(&self.namespace),
            quote(id),
            quote(value)
        ));
        let out = command("/usr/bin/security", &["-i"], Some(&input)).await?;
        let diagnostic = String::from_utf8_lossy(&out.stderr).to_lowercase();
        if !out.status.success()
            || ["seckeychain", "error:", "denied", "failed"]
                .iter()
                .any(|v| diagnostic.contains(v))
        {
            return Err(ConnectionError::new(
                "connection_permission_required",
                "无法保存连接服务钥匙串",
            )
            .at("credential_store"));
        }
        Ok(())
    }
    async fn delete(&self, id: &str) -> Result<()> {
        supported()?;
        let out = command(
            "/usr/bin/security",
            &["delete-generic-password", "-s", &self.namespace, "-a", id],
            None,
        )
        .await?;
        if matches!(out.status.code(), Some(0 | 44)) {
            Ok(())
        } else {
            Err(
                ConnectionError::new("connection_permission_required", "无法删除连接服务钥匙串")
                    .at("credential_store"),
            )
        }
    }
}

#[cfg(test)]
pub(crate) fn decrypt_login(sealed: &[u8], password: &[u8]) -> Result<Login> {
    parse_qwen_login(&decrypt_value(sealed, password)?)
}
fn decrypt_value(sealed: &[u8], password: &[u8]) -> Result<Value> {
    if !sealed.starts_with(b"v10") {
        return Err(ConnectionError::new(
            "unsupported_encryption",
            "来源应用凭据的加密格式暂不支持",
        )
        .at("decrypt"));
    }
    let mut key = Zeroizing::new([0u8; 16]);
    ring::pbkdf2::derive(
        ring::pbkdf2::PBKDF2_HMAC_SHA1,
        NonZeroU32::new(1003).unwrap(),
        b"saltysalt",
        password,
        key.as_mut(),
    );
    let plain = Zeroizing::new(
        cbc::Decryptor::<aes::Aes128>::new_from_slices(key.as_ref(), &[32; 16])
            .map_err(|_| {
                ConnectionError::new("credential_decrypt_failed", "无法初始化凭据解密")
                    .at("decrypt")
            })?
            .decrypt_padded_vec_mut::<Pkcs7>(&sealed[3..])
            .map_err(|_| {
                ConnectionError::new(
                    "credential_decrypt_failed",
                    "登录凭据解密失败，凭据可能损坏或与钥匙串不匹配",
                )
                .at("decrypt")
            })?,
    );
    serde_json::from_slice(&plain).map_err(|_| {
        ConnectionError::new(
            "credential_not_json",
            "解密后的登录信息不是有效的 JSON 数据",
        )
        .at("source_login")
    })
}
fn parse_qwen_login(value: &Value) -> Result<Login> {
    if value["schemaVersion"] != 2 {
        return Err(ConnectionError::new(
            "unsupported_login_schema",
            "来源应用登录文件格式暂不支持",
        )
        .at("source_login")
        .field("schemaVersion"));
    }
    if value["authRealm"] == "premiumIam" {
        return Err(ConnectionError::new(
            "unsupported_auth_realm",
            "当前适配器暂不支持此账户认证方式",
        )
        .at("source_login")
        .field("authRealm"));
    }
    for (field, v) in [
        ("endpoints.apiOrigin", &value["endpoints"]["apiOrigin"]),
        ("openApiBase", &value["openApiBase"]),
    ] {
        if v.as_str().is_some_and(|s| !s.is_empty()) {
            return Err(ConnectionError::new(
                "unsupported_origin",
                "当前适配器暂不支持自定义认证服务地址",
            )
            .at("source_login")
            .field(field));
        }
    }
    let token = value["token"]
        .as_str()
        .filter(|s| !s.is_empty())
        .ok_or_else(|| ConnectionError::format("source_login", "token"))?
        .to_string();
    let user = &value["user"];
    let stable = ["id", "userId", "uid", "sub", "email"]
        .iter()
        .find_map(|k| user.get(*k).filter(|v| !v.is_null()))
        .ok_or_else(|| ConnectionError::format("source_login", "user.id"))?;
    let tenant = user
        .get("tenantId")
        .or_else(|| user.get("orgId"))
        .filter(|v| !v.is_null())
        .cloned()
        .unwrap_or(Value::String(String::new()));
    let realm = value
        .get("authRealm")
        .filter(|v| !v.is_null())
        .cloned()
        .unwrap_or(Value::String("standard".into()));
    let identity = digest(
        serde_json::to_string(&serde_json::json!([stable, tenant, realm]))
            .map_err(|_| ConnectionError::unsupported())?
            .as_bytes(),
    );
    let expires = chrono::DateTime::parse_from_rfc3339(
        value["expiresAt"]
            .as_str()
            .ok_or_else(|| ConnectionError::format("source_login", "expiresAt"))?,
    )
    .map_err(|_| ConnectionError::format("source_login", "expiresAt"))?
    .timestamp_millis();
    Ok(Login {
        subject: None,
        token,
        identity,
        expires,
    })
}

#[cfg(target_os = "macos")]
fn source_password(service: &str, interactive: bool) -> Result<Zeroizing<Vec<u8>>> {
    use core_foundation::{
        base::{CFType, TCFType},
        boolean::CFBoolean,
        data::CFData,
        dictionary::CFDictionary,
        string::CFString,
    };
    use security_framework_sys::{item::*, keychain_item::SecItemCopyMatching};
    unsafe extern "C" {
        static kSecUseAuthenticationUIFail: core_foundation::string::CFStringRef;
    }
    unsafe {
        let string = |v| CFString::wrap_under_get_rule(v).as_CFType();
        let mut pairs = vec![
            (string(kSecClass), string(kSecClassGenericPassword)),
            (string(kSecAttrService), CFString::new(service).as_CFType()),
            (string(kSecReturnData), CFBoolean::true_value().as_CFType()),
        ];
        if !interactive {
            pairs.push((
                string(kSecUseAuthenticationUI),
                string(kSecUseAuthenticationUIFail),
            ));
        }
        let query = CFDictionary::from_CFType_pairs(&pairs);
        let mut result = std::ptr::null();
        let status = SecItemCopyMatching(query.as_concrete_TypeRef(), &mut result);
        if status != 0 {
            tracing::warn!(
                stage = "keychain",
                os_status = status,
                interactive,
                "connection_keychain_failed"
            );
            return Err(keychain_error(status));
        }
        if result.is_null() {
            return Err(
                ConnectionError::new("keychain_invalid", "钥匙串未返回凭据数据").at("keychain"),
            );
        }
        let result = CFType::wrap_under_create_rule(result);
        let data = result.downcast::<CFData>().ok_or_else(|| {
            ConnectionError::new("keychain_invalid", "钥匙串凭据数据格式无效").at("keychain")
        })?;
        Ok(Zeroizing::new(data.bytes().to_vec()))
    }
}
#[cfg(not(target_os = "macos"))]
fn source_password(_: &str, _: bool) -> Result<Zeroizing<Vec<u8>>> {
    Err(ConnectionError::unsupported())
}

// Stable Security.framework OSStatus codes; never expose keychain contents.
#[cfg(any(target_os = "macos", test))]
fn keychain_error(status: i32) -> ConnectionError {
    let (code, message) = match status {
        -128 => ("permission_cancelled", "已取消钥匙串授权，可点击重新授权"),
        -25300 => (
            "connection_login_required",
            "未找到来源应用钥匙串凭据，请打开来源应用恢复登录",
        ),
        -25293 | -25308 => (
            "connection_permission_required",
            "需要允许读取来源应用钥匙串，请在模型设置中重新授权",
        ),
        _ => ("keychain_unavailable", "无法读取来源应用钥匙串"),
    };
    ConnectionError::new(code, message).at("keychain")
}
#[cfg(test)]
mod diagnostic_tests {
    use super::*;
    #[test]
    fn desktop_versions_are_metadata_not_admission_rules() {
        for version in ["1.3.2", "1.4.0", "9.0.0", "2.0.0-beta.1"] {
            let h = qwen_headers(
                &serde_json::json!({"CFBundleShortVersionString":version,"CFBundleVersion":"987"}),
            )
            .unwrap();
            assert_eq!(h["X-QwenWork-Version"], version);
            assert_eq!(h["X-QwenWork-Release-Version"], format!("{version}-987"));
        }
        let e = qwen_headers(&serde_json::json!({"CFBundleVersion":"987"})).unwrap_err();
        assert_eq!(e.stage, "app_metadata");
        assert_eq!(e.field.as_deref(), Some("CFBundleShortVersionString"));
    }
    #[tokio::test]
    async fn qoder_headers_do_not_gate_on_installed_version() {
        if cfg!(target_os = "macos") {
            let system = System {
                namespace: "unused".into(),
                product: "qoder",
            };
            assert!(system.headers().await.unwrap().is_empty());
        }
    }
    #[test]
    fn file_and_keychain_failures_are_distinct() {
        assert_eq!(
            login_file_error(std::io::ErrorKind::NotFound.into()).code,
            "connection_login_required"
        );
        assert_eq!(
            login_file_error(std::io::ErrorKind::PermissionDenied.into()).code,
            "login_file_denied"
        );
        assert_eq!(keychain_error(-128).code, "permission_cancelled");
        assert_eq!(keychain_error(-25300).code, "connection_login_required");
        assert_eq!(
            keychain_error(-25308).code,
            "connection_permission_required"
        );
        assert_eq!(
            decrypt_value(b"v11fake", b"unused").unwrap_err().code,
            "unsupported_encryption"
        );
    }
}
