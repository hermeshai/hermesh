//! Diagnostics carry only locally chosen messages/field names, never remote bodies or secrets.
use crate::Result;
use her_llm::{ErrorKind, LlmError};
use her_protocol::api::ConnectionDiagnostic;

/// Boxed so `Result<T, ConnectionError>` stays one pointer wide on the happy path.
#[derive(Clone, Debug)]
pub struct ConnectionError(Box<ConnectionErrorDetail>);

#[derive(Clone, Debug)]
pub struct ConnectionErrorDetail {
    pub code: String,
    pub message: String,
    pub stage: String,
    pub retryable: bool,
    pub action: String,
    pub http_status: Option<u16>,
    pub field: Option<String>,
}
impl std::ops::Deref for ConnectionError {
    type Target = ConnectionErrorDetail;
    fn deref(&self) -> &ConnectionErrorDetail {
        &self.0
    }
}
impl std::ops::DerefMut for ConnectionError {
    fn deref_mut(&mut self) -> &mut ConnectionErrorDetail {
        &mut self.0
    }
}
impl ConnectionError {
    pub fn new(code: &str, message: &str) -> Self {
        let retryable = matches!(
            code,
            "connection_unavailable"
                | "network_error"
                | "network_timeout"
                | "service_unavailable"
                | "rate_limited"
        );
        let action = match code {
            "connection_login_required" => "open_source",
            "connection_permission_required" | "permission_cancelled" => "authorize",
            "identity_changed" => "reconnect_account",
            "permission_denied" => "check_account",
            "source_not_found" => "open_source",
            _ if retryable => "retry",
            _ => "check_details",
        };
        Self(Box::new(ConnectionErrorDetail {
            code: code.into(),
            message: message.into(),
            stage: "connection".into(),
            retryable,
            action: action.into(),
            http_status: None,
            field: None,
        }))
    }
    pub fn into_message(self) -> String {
        self.0.message
    }
    pub fn at(mut self, stage: &str) -> Self {
        self.stage = stage.into();
        self
    }
    pub fn field(mut self, field: &str) -> Self {
        self.field = Some(field.into());
        self
    }
    pub fn unavailable() -> Self {
        Self::new("connection_unavailable", "模型连接暂时不可用，请重试")
    }
    pub fn unsupported() -> Self {
        Self::new("unsupported", "连接数据格式暂不支持，请查看失败阶段")
    }
    pub fn format(stage: &str, field: &str) -> Self {
        Self::new("response_invalid", "连接数据缺少必需字段或字段格式无效")
            .at(stage)
            .field(field)
    }
    pub fn login() -> Self {
        Self::new(
            "connection_login_required",
            "请打开来源应用恢复登录，然后重新连接",
        )
        .at("source_login")
    }
    /// Preserve the HTTP status and stage when a source credential was rejected.
    pub fn source_rejected(mut self) -> Self {
        if self.code == "auth_rejected" {
            self.code = "connection_login_required".into();
            self.message = "来源应用登录凭据被拒绝，请打开来源应用恢复登录".into();
            self.action = "open_source".into();
        }
        self
    }
    pub fn network(error: &reqwest::Error, stage: &str) -> Self {
        // reqwest errors may embed URLs or request data. Do not expose their text.
        tracing::warn!(
            stage,
            timeout = error.is_timeout(),
            connect = error.is_connect(),
            body = error.is_body(),
            decode = error.is_decode(),
            request = error.is_request(),
            "connection_transport_failed"
        );
        if error.is_timeout() {
            Self::new("network_timeout", "连接服务超时，请检查网络后重试").at(stage)
        } else if error.is_builder() {
            Self::new(
                "request_invalid",
                "无法构造连接请求，请检查应用元数据或协议配置",
            )
            .at(stage)
        } else {
            Self::new(
                "network_error",
                "无法完成网络请求，请检查网络、代理或证书配置后重试",
            )
            .at(stage)
        }
    }
    pub fn diagnostic(&self) -> ConnectionDiagnostic {
        ConnectionDiagnostic {
            stage: self.stage.clone(),
            code: self.code.clone(),
            message: self.message.clone(),
            retryable: self.retryable,
            action: self.action.clone(),
            checked_at: her_llm::discovery::now(),
            http_status: self.http_status,
            field: self.field.clone(),
        }
    }
    pub(crate) fn llm(&self) -> LlmError {
        let kind = match self.code.as_str() {
            "connection_login_required"
            | "identity_changed"
            | "connection_permission_required"
            | "auth_rejected" => ErrorKind::Auth,
            "rate_limited" => ErrorKind::RateLimited,
            _ if self.retryable => ErrorKind::Network,
            _ => ErrorKind::InvalidRequest,
        };
        let mut e = LlmError::new(kind, &self.message);
        e.provider_code = Some(self.code.clone());
        e.status = self.http_status;
        e
    }
}
impl std::fmt::Display for ConnectionError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "{}", self.message)
    }
}
impl std::error::Error for ConnectionError {}

/// One correlated operation, including failures before a connection record exists.
/// Never record future results: they may contain source or model credentials.
pub(crate) async fn operation<T>(
    name: &'static str,
    adapter: &str,
    connection_id: &str,
    foreground: bool,
    work: impl std::future::Future<Output = Result<T>>,
) -> Result<T> {
    use tracing::Instrument;
    let adapter = match adapter {
        "qwenwork" => "qwenwork",
        "qoder" => "qoder",
        "chatgpt" => "chatgpt",
        _ => "unknown",
    };
    let connection_id = uuid::Uuid::parse_str(connection_id)
        .map(|id| id.to_string())
        .unwrap_or_default();
    let span = tracing::info_span!("model_connection", operation = name, adapter,
        attempt_id = %uuid::Uuid::new_v4(), connection_id = %connection_id);
    async move {
        let started = std::time::Instant::now();
        if foreground {
            tracing::info!("connection_operation_started");
        } else {
            tracing::debug!("connection_operation_started");
        }
        let result = work.await;
        let elapsed_ms = started.elapsed().as_millis() as u64;
        match &result {
            Ok(_) if foreground => tracing::info!(elapsed_ms, "connection_operation_completed"),
            Ok(_) => tracing::debug!(elapsed_ms, "connection_operation_completed"),
            Err(error) => log_failure(error, elapsed_ms, "operation"),
        }
        result
    }
    .instrument(span)
    .await
}

pub(crate) fn log_failure(error: &ConnectionError, elapsed_ms: u64, scope: &'static str) {
    tracing::warn!(scope, stage = %error.stage, code = %error.code,
        http_status = ?error.http_status, field = ?error.field,
        retryable = error.retryable, action = %error.action, elapsed_ms,
        "connection_failed");
}

pub(crate) async fn stage<T>(
    stage: &'static str,
    work: impl std::future::Future<Output = Result<T>>,
) -> Result<T> {
    let started = std::time::Instant::now();
    tracing::info!(stage, "connection_stage_started");
    let result = work.await;
    let elapsed_ms = started.elapsed().as_millis() as u64;
    match &result {
        Ok(_) => tracing::info!(stage, elapsed_ms, "connection_stage_completed"),
        Err(error) => log_failure(error, elapsed_ms, stage),
    }
    result
}

/// Only log an operation label and response metadata, never URLs, headers or bodies.
pub(crate) async fn request_json(
    request: reqwest::RequestBuilder,
    stage: &'static str,
) -> Result<serde_json::Value> {
    self::stage(stage, async {
        let response = request
            .send()
            .await
            .map_err(|e| ConnectionError::network(&e, stage))?;
        tracing::info!(
            stage,
            http_status = response.status().as_u16(),
            "connection_http_response"
        );
        response_json(response, stage).await
    })
    .await
}

/// Shared transport classification; adapters supply the operation, not untrusted response text.
pub(crate) async fn response_json(
    mut response: reqwest::Response,
    stage: &str,
) -> Result<serde_json::Value> {
    let status = response.status().as_u16();
    if !response.status().is_success() {
        let (code, message) = match status {
            401 => ("auth_rejected", "服务端拒绝了当前授权凭据"),
            403 => ("permission_denied", "服务端拒绝访问，请检查账户或组织权限"),
            429 => ("rate_limited", "服务端限制了请求，请稍后重试或检查账户额度"),
            500..=599 => ("service_unavailable", "服务端暂时异常，请稍后重试"),
            300..=399 => (
                "redirect_rejected",
                "连接接口返回重定向，未向新地址发送授权凭据",
            ),
            _ => (
                "request_rejected",
                "服务端拒绝了连接请求，请查看 HTTP 状态码",
            ),
        };
        let mut e = ConnectionError::new(code, message).at(stage);
        e.http_status = Some(status);
        return Err(e);
    }
    let mut bytes = Vec::new();
    while let Some(chunk) = response
        .chunk()
        .await
        .map_err(|e| ConnectionError::network(&e, stage))?
    {
        if bytes.len() + chunk.len() > 2 * 1024 * 1024 {
            let mut e =
                ConnectionError::new("response_too_large", "接口响应超过大小限制").at(stage);
            e.http_status = Some(status);
            return Err(e);
        }
        bytes.extend_from_slice(&chunk);
    }
    serde_json::from_slice(&bytes).map_err(|_| {
        let mut e =
            ConnectionError::new("response_not_json", "接口未返回有效的 JSON 数据").at(stage);
        e.http_status = Some(status);
        e
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use wiremock::{Mock, MockServer, ResponseTemplate, matchers::path};

    #[tokio::test]
    async fn timeout_and_oversized_body_have_safe_specific_diagnostics() {
        let server = MockServer::start().await;
        let http = her_llm::ModelRegistry::new(
            her_llm::Catalog::builtin().unwrap(),
            her_llm::RegistryOptions::default(),
        )
        .http_client()
        .unwrap();
        Mock::given(path("/slow"))
            .respond_with(
                ResponseTemplate::new(200).set_delay(std::time::Duration::from_millis(200)),
            )
            .mount(&server)
            .await;
        let raw = http
            .get(format!("{}/slow", server.uri()))
            .timeout(std::time::Duration::from_millis(20))
            .send()
            .await
            .unwrap_err();
        let e = ConnectionError::network(&raw, "catalog");
        assert_eq!(e.code, "network_timeout");
        assert!(e.retryable);
        assert_eq!(e.stage, "catalog");
        Mock::given(path("/large"))
            .respond_with(
                ResponseTemplate::new(200).set_body_string("x".repeat(2 * 1024 * 1024 + 1)),
            )
            .mount(&server)
            .await;
        let response = http
            .get(format!("{}/large", server.uri()))
            .send()
            .await
            .unwrap();
        let e = response_json(response, "authorization").await.unwrap_err();
        assert_eq!(e.code, "response_too_large");
        assert_eq!(e.http_status, Some(200));
        assert_eq!(e.stage, "authorization");
        assert!(!e.retryable);
    }

    #[derive(Clone, Default)]
    struct LogBuffer(std::sync::Arc<std::sync::Mutex<Vec<u8>>>);
    impl std::io::Write for LogBuffer {
        fn write(&mut self, bytes: &[u8]) -> std::io::Result<usize> {
            self.0.lock().unwrap().extend_from_slice(bytes);
            Ok(bytes.len())
        }
        fn flush(&mut self) -> std::io::Result<()> {
            Ok(())
        }
    }
    impl<'a> tracing_subscriber::fmt::MakeWriter<'a> for LogBuffer {
        type Writer = Self;
        fn make_writer(&'a self) -> Self::Writer {
            self.clone()
        }
    }

    #[tokio::test]
    async fn default_logs_correlate_operations_and_do_not_emit_credentials_or_bodies() {
        let server = MockServer::start().await;
        Mock::given(path("/authorize"))
            .respond_with(ResponseTemplate::new(403).set_body_string("secret-response-body"))
            .mount(&server)
            .await;
        let http = her_llm::ModelRegistry::new(
            her_llm::Catalog::builtin().unwrap(),
            her_llm::RegistryOptions::default(),
        )
        .http_client()
        .unwrap();
        let buffer = LogBuffer::default();
        let subscriber = tracing_subscriber::fmt()
            .with_writer(buffer.clone())
            .without_time()
            .with_ansi(false)
            .with_max_level(tracing::Level::INFO)
            .finish();
        // Match production's process-wide subscriber. Scoped dispatchers can have
        // callsite-interest races with concurrently running tests without subscribers.
        tracing::subscriber::set_global_default(subscriber).unwrap();
        async {
            let denied = operation("logging_test_connect", "qwenwork", "", true, async {
                request_json(
                    http.post(format!("{}/authorize", server.uri()))
                        .bearer_auth("secret-bearer-token")
                        .json(&serde_json::json!({"token":"secret-request-body"})),
                    "authorization",
                )
                .await
            });
            let allowed = operation("logging_test_retry", "qoder", "", true, async {
                tokio::task::yield_now().await;
                stage("source_login", async { Ok("secret-success-value") }).await
            });
            let (denied, allowed) = tokio::join!(denied, allowed);
            assert_eq!(denied.unwrap_err().code, "permission_denied");
            assert!(allowed.is_ok());
            let _: Result<()> = operation("logging_test_connect", "qwenwork", "", true, async {
                Err(
                    ConnectionError::new("credential_decrypt_failed", "secret-error-message")
                        .at("decrypt"),
                )
            })
            .await;
        }
        .await;
        let output = String::from_utf8(buffer.0.lock().unwrap().clone()).unwrap();
        assert!(output.contains("connection_operation_started"));
        assert!(output.contains("connection_operation_completed"));
        assert!(output.contains("connection_failed"), "{output}");
        assert!(output.contains("permission_denied"));
        assert!(output.contains("credential_decrypt_failed"));
        assert!(output.contains("elapsed_ms="));
        assert!(output.contains("http_status=Some(403)"));
        for line in output
            .lines()
            .filter(|line| line.contains("operation=\"logging_test_"))
        {
            assert!(line.contains("attempt_id="), "uncorrelated line: {line}");
            assert!(!(line.contains("adapter=\"qoder\"") && line.contains("permission_denied")));
        }
        for secret in [
            "secret-bearer-token",
            "secret-request-body",
            "secret-response-body",
            "secret-success-value",
            "secret-error-message",
        ] {
            assert!(!output.contains(secret));
        }
    }
}
