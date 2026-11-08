//! Public connection management and model-directory notifications; product logic is elsewhere.
use crate::{
    conn::{ConnHandle, ConnId},
    server::Server,
};
use her_protocol::api::method;
use her_rpc::RpcError;
impl Server {
    pub(crate) fn watch_models(&self, conn: &ConnHandle) {
        self.0.model_watchers.lock().insert(conn.id, conn.clone());
    }
    pub(crate) fn forget_model_watcher(&self, id: ConnId) {
        self.0.model_watchers.lock().remove(&id);
    }
    pub(crate) fn models_changed(&self) {
        for conn in self.0.model_watchers.lock().values() {
            conn.send(her_rpc::notification(
                method::MODELS_CHANGED,
                &serde_json::json!({}),
            ));
        }
    }
}
pub(crate) fn rpc_error(e: her_connections::ConnectionError) -> RpcError {
    let diagnostic = e.diagnostic();
    let data = serde_json::json!({"kind":e.code,"diagnostic":diagnostic});
    RpcError {
        code: -32000,
        message: e.into_message(),
        data: Some(data),
    }
}

#[cfg(test)]
mod tests {
    #[test]
    fn rpc_preserves_safe_structured_connection_diagnostics() {
        let mut failure =
            her_connections::ConnectionError::new("permission_denied", "Access denied")
                .at("authorization")
                .field("grant");
        failure.http_status = Some(403);
        let rpc = super::rpc_error(failure);
        let data = rpc.data.unwrap();
        assert_eq!(data["kind"], "permission_denied");
        assert_eq!(data["diagnostic"]["stage"], "authorization");
        assert_eq!(data["diagnostic"]["http_status"], 403);
        assert_eq!(data["diagnostic"]["action"], "check_account");
        assert_eq!(data["diagnostic"]["retryable"], false);
    }
}
