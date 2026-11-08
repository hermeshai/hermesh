//! The native protocol: `session/op` carries an Op and `session/event` an Event, both in the
//! same JSON as the in-process API, so no field is mapped on the way through.

use bytes::Bytes;
use her_protocol::Op;
use her_protocol::PROTOCOL_VERSION;
use her_protocol::api::AuthDeleteParams;
use her_protocol::api::AuthSetParams;
use her_protocol::api::BrowserSiteIconParams;
use her_protocol::files::FilesListParams;
use her_protocol::files::FilesReadParams;
use her_protocol::files::FilesWatchParams;
use her_protocol::files::FilesWriteParams;
use her_protocol::api::InitializeParams;
use her_protocol::api::InitializeResult;
use her_protocol::api::ModelsListResult;
use her_protocol::api::ModelsRefreshParams;
use her_protocol::api::ModelsShowParams;
use her_protocol::api::PeerInfo;
use her_protocol::api::ResourceReadParams;
use her_protocol::api::ServerCapabilities;
use her_protocol::api::SessionAttachParams;
use her_protocol::api::SessionHistoryParams;
use her_protocol::api::SessionIdParams;
use her_protocol::api::SessionListParams;
use her_protocol::api::SessionNewParams;
use her_protocol::api::SessionOpParams;
use her_protocol::api::SessionRenameParams;
use her_protocol::api::SessionResumeParams;
use her_protocol::api::SessionSnapshot;
use her_protocol::api::SkillsListParams;
use her_protocol::api::ToolsListParams;
use her_protocol::api::method;
use her_protocol::bot::BotCreateParams;
use her_protocol::bot::BotIdParams;
use her_protocol::bot::BotOpenParams;
use her_protocol::bot::BotUpdateParams;
use her_rpc::Id;
use her_rpc::Kind;
use her_rpc::RawMessage;
use her_rpc::RpcError;
use parking_lot::Mutex;
use serde::Deserialize;
use serde::Serialize;
use serde_json::json;
use serde_json::value::RawValue;
use std::collections::HashMap;
use std::future::Future;
use std::sync::Arc;
use std::sync::atomic::AtomicBool;
use std::sync::atomic::Ordering;
use tokio::task::AbortHandle;

use crate::conn::ConnHandle;
use crate::conn::Encoder;
use crate::conn::Outgoing;
use crate::conn::Protocol;
use crate::errors;
use crate::hub::AttachRequest;
use crate::server::NewSession;
use crate::server::ResumeOverrides;
use crate::server::Server;
use her_protocol::customize::*;

pub struct NativeProtocol {
    server: Server,
    initialized: AtomicBool,
    /// Spawned requests that `$/cancel_request` can abort.
    running: Arc<Mutex<HashMap<Id, AbortHandle>>>,
    /// Set once the connection called `browser/host`, with what it hosts.
    browser_host: Mutex<Option<(Arc<crate::browser_host::HostLink>, her_protocol::api::BrowserHostKind)>>,
    /// Set once the connection called `docx/host`.
    docx_host: Mutex<Option<Arc<crate::docx_host::HostLink>>>,
}

#[derive(Serialize)]
struct NewResult<'a> {
    session_id: &'a str,
    snapshot: &'a SessionSnapshot,
}

#[derive(Serialize)]
struct SnapshotResult<'a> {
    snapshot: &'a SessionSnapshot,
}

#[derive(Deserialize)]
struct CancelParams {
    id: Id,
}

fn reply<T: Serialize>(id: &Id, result: Result<T, RpcError>) -> Vec<u8> {
    match result {
        Ok(value) => her_rpc::response(id, &value),
        Err(error) => her_rpc::error_response(Some(id), &error),
    }
}

impl NativeProtocol {
    pub fn new(server: Server) -> Arc<Self> {
        Arc::new(Self {
            server,
            initialized: AtomicBool::new(false),
            running: Arc::new(Mutex::new(HashMap::new())),
            browser_host: Mutex::new(None),
            docx_host: Mutex::new(None),
        })
    }

    /// Runs a request in its own task; the task answers on its own unless cancelled.
    /// Parses the parameters, runs `work` on its own task and replies with its result.
    fn call<P, R, F, Fut>(&self, id: &Id, params: Option<&RawValue>, conn: &ConnHandle, work: F)
    where
        P: serde::de::DeserializeOwned + Send + 'static,
        R: Serialize,
        F: FnOnce(Server, P) -> Fut + Send + 'static,
        Fut: Future<Output = Result<R, RpcError>> + Send + 'static,
    {
        match her_rpc::params::<P>(params) {
            Ok(p) => {
                let reply_id = id.clone();
                let server = self.server.clone();
                self.spawn(id.clone(), conn, async move { Some(reply(&reply_id, work(server, p).await)) });
            }
            Err(err) => conn.send_error(Some(id), &err),
        }
    }

    fn spawn<F>(&self, id: Id, conn: &ConnHandle, work: F)
    where
        F: Future<Output = Option<Vec<u8>>> + Send + 'static,
    {
        let running = self.running.clone();
        let conn = conn.clone();
        let key = id.clone();
        let mut guard = self.running.lock();
        let task = tokio::spawn(async move {
            let out = work.await;
            if running.lock().remove(&key).is_some()
                && let Some(out) = out
            {
                conn.send(out);
            }
        });
        guard.insert(id, task.abort_handle());
    }

    fn cancel(&self, id: &Id, conn: &ConnHandle) {
        if let Some(task) = self.running.lock().remove(id) {
            task.abort();
            conn.send_error(Some(id), &RpcError::cancelled());
        }
    }

    fn attach_request(
        conn: &ConnHandle,
        id: &Id,
        events: her_protocol::api::EventFilter,
        limit: Option<u32>,
        new: bool,
    ) -> AttachRequest {
        let reply_id = id.clone();
        let respond: crate::hub::Responder = if new {
            Box::new(move |snapshot: &SessionSnapshot| {
                let result = NewResult {
                    session_id: &snapshot.session.session_id,
                    snapshot,
                };
                vec![Outgoing::Frame(Bytes::from(her_rpc::response(
                    &reply_id, &result,
                )))]
            })
        } else {
            Box::new(move |snapshot: &SessionSnapshot| {
                vec![Outgoing::Frame(Bytes::from(her_rpc::response(
                    &reply_id,
                    &SnapshotResult { snapshot },
                )))]
            })
        };
        AttachRequest {
            conn: conn.clone(),
            filter: events,
            history_limit: Some(limit.map_or(100, |n| n.clamp(1, 1000)) as usize),
            respond,
        }
    }

    fn request(
        self: &Arc<Self>,
        id: &Id,
        method_name: &str,
        params: Option<&RawValue>,
        conn: &ConnHandle,
    ) {
        if method_name != method::INITIALIZE && !self.initialized.load(Ordering::Relaxed) {
            conn.send_error(Some(id), &errors::not_initialized());
            return;
        }
        let server = self.server.clone();
        match method_name {
            method::INITIALIZE => {
                let result = her_rpc::params::<InitializeParams>(params).and_then(|p| {
                    // Version 2 renamed the permission modes, so older clients are refused.
                    if p.protocol_version < PROTOCOL_VERSION {
                        return Err(RpcError {
                            data: Some(json!({"kind": "invalid_params", "supported": [PROTOCOL_VERSION]})),
                            ..RpcError::invalid_params(format!(
                                "unsupported protocol version {}; this server speaks {PROTOCOL_VERSION}",
                                p.protocol_version
                            ))
                        });
                    }
                    conn.coalesce.store(p.capabilities.coalesce, Ordering::Relaxed);
                    self.initialized.store(true, Ordering::Relaxed);
                    Ok(InitializeResult {
                        protocol_version: PROTOCOL_VERSION,
                        server_info: PeerInfo {
                            name: "her".into(),
                            version: Some(env!("CARGO_PKG_VERSION").into()),
                        },
                        capabilities: ServerCapabilities {
                            max_frame_bytes: server.settings().max_frame_bytes as u64,
                            session_delete: true,
                            resource_read: true,
                            event_filter: true,
                            sessions_subscribe: true,
                            session_metadata: true,
                            transcript_task_end: true,
                            auth: true,
                            agents: true,
                            model_connections: true,
                            model_discovery: true,
                            customize: true,
                            embedded_browser: true,
                            input_queue: true,
                            message_resend: true,
                            workbooks: true,
                            bots: true,
                            routines: true,
                        },
                    })
                });
                conn.send(reply(id, result));
            }
            method::SESSION_NEW => match her_rpc::params::<SessionNewParams>(params) {
                Ok(p) => {
                    let attach = Self::attach_request(conn, id, p.events, p.history_limit, true);
                    let args = NewSession {
                        require_credentials: false,
                        cwd: p.cwd,
                        extra_roots: p.extra_roots,
                        model: p.model,
                        thinking: p.thinking,
                        permission_mode: p.permission_mode,
                        mcp_servers: p.mcp_servers,
                    };
                    let reply_id = id.clone();
                    self.spawn(id.clone(), conn, async move {
                        match server.create_session(args, attach).await {
                            Ok(_) => None,
                            Err(err) => Some(her_rpc::error_response(Some(&reply_id), &err)),
                        }
                    });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::SESSION_RESUME => match her_rpc::params::<SessionResumeParams>(params) {
                Ok(p) => {
                    let attach = Self::attach_request(conn, id, p.events, p.history_limit, false);
                    let overrides = ResumeOverrides {
                        model: p.model,
                        thinking: p.thinking,
                        permission_mode: p.permission_mode,
                        mcp_servers: p.mcp_servers,
                    };
                    let reply_id = id.clone();
                    self.spawn(id.clone(), conn, async move {
                        match server
                            .resume_session(&p.session_id, overrides, attach)
                            .await
                        {
                            Ok(()) => None,
                            Err(err) => Some(her_rpc::error_response(Some(&reply_id), &err)),
                        }
                    });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::SESSION_ATTACH => match her_rpc::params::<SessionAttachParams>(params) {
                Ok(p) => {
                    let attach = Self::attach_request(conn, id, p.events, p.history_limit, false);
                    let (conn, id) = (conn.clone(), id.clone());
                    tokio::spawn(async move {
                        if let Err(err) = server.attach(&p.session_id, attach).await {
                            conn.send_error(Some(&id), &err);
                        }
                    });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::SESSION_DETACH => match her_rpc::params::<SessionIdParams>(params) {
                Ok(p) => {
                    let (conn, id) = (conn.clone(), id.clone());
                    tokio::spawn(async move {
                        let reply_id = id.clone();
                        let respond: crate::hub::DetachResponder = Box::new(move || {
                            vec![Outgoing::Frame(Bytes::from(her_rpc::response(
                                &reply_id,
                                &json!({}),
                            )))]
                        });
                        if let Err(err) = server
                            .detach(&p.session_id, conn.clone(), Some(respond))
                            .await
                        {
                            conn.send_error(Some(&id), &err);
                        }
                    });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::SESSION_OP => match her_rpc::params::<SessionOpParams>(params) {
                Ok(p) => {
                    let (conn, id) = (conn.clone(), id.clone());
                    tokio::spawn(async move {
                        let reply_id = id.clone();
                        let respond: crate::hub::OpResponder = Box::new(move |result| {
                            vec![Outgoing::Frame(Bytes::from(reply(
                                &reply_id,
                                result.map(|()| json!({})),
                            )))]
                        });
                        if let Err(err) = server
                            .submit(&p.session_id, conn.clone(), p.op, false, respond)
                            .await
                        {
                            conn.send_error(Some(&id), &err);
                        }
                    });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::SESSION_HISTORY => match her_rpc::params::<SessionHistoryParams>(params) {
                Ok(p) => {
                    let (conn, id) = (conn.clone(), id.clone());
                    tokio::spawn(async move {
                        let limit = p.limit.unwrap_or(100).clamp(1, 1000) as usize;
                        let result = server.history(&p.session_id, p.before, limit).await;
                        conn.send(reply(&id, result));
                    });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::SESSION_CONTEXT => match her_rpc::params::<SessionIdParams>(params) {
                Ok(p) => {
                    let (conn, id) = (conn.clone(), id.clone());
                    tokio::spawn(async move {
                        let result = server.context(&p.session_id).await;
                        conn.send(reply(&id, result));
                    });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::SESSION_LIST => match her_rpc::params::<Option<SessionListParams>>(params) {
                Ok(p) => {
                    let reply_id = id.clone();
                    self.spawn(id.clone(), conn, async move {
                        let result = tokio::task::spawn_blocking(move || {
                            server.list_sessions(&p.unwrap_or_default())
                        })
                        .await
                        .unwrap_or_else(|e| Err(RpcError::internal(e.to_string())));
                        Some(reply(&reply_id, result))
                    });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::SESSION_CLOSE => match her_rpc::params::<SessionIdParams>(params) {
                Ok(p) => {
                    let (conn, id) = (conn.clone(), id.clone());
                    tokio::spawn(async move {
                        let result = server
                            .close_session(&p.session_id)
                            .await
                            .map(|()| json!({}));
                        conn.send(reply(&id, result));
                    });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::SESSION_DELETE => match her_rpc::params::<SessionIdParams>(params) {
                Ok(p) => {
                    let (conn, id) = (conn.clone(), id.clone());
                    tokio::spawn(async move {
                        if server.is_loaded(&p.session_id) {
                            let _ = server.close_session(&p.session_id).await;
                        }
                        let result = tokio::task::spawn_blocking(move || {
                            server.delete_session(&p.session_id)
                        })
                        .await
                        .unwrap_or_else(|e| Err(RpcError::internal(e.to_string())))
                        .map(|()| json!({}));
                        conn.send(reply(&id, result));
                    });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::RESOURCE_READ => match her_rpc::params::<ResourceReadParams>(params) {
                Ok(p) => {
                    let reply_id = id.clone();
                    self.spawn(id.clone(), conn, async move {
                        let result = tokio::task::spawn_blocking(move || server.read_resource(&p))
                            .await
                            .unwrap_or_else(|e| Err(RpcError::internal(e.to_string())));
                        Some(reply(&reply_id, result))
                    });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::CHATGPT_STATUS => conn.send(reply(id, server.0.connections.chatgpt.status().map_err(crate::connections::rpc_error))),
            method::CHATGPT_LOGIN => {
                // Without params, as before: her catches the redirect itself.
                let redirect = her_rpc::params::<Option<her_protocol::api::ChatGptLoginParams>>(params)
                    .and_then(|p| p.and_then(|p| p.redirect_uri).map(|uri| crate::customize::loopback_redirect(&uri)).transpose());
                match redirect {
                    Ok(redirect) => {
                        let reply_id = id.clone();
                        self.spawn(id.clone(), conn, async move {
                            Some(reply(&reply_id, server.0.connections.chatgpt.start(redirect).await.map_err(crate::connections::rpc_error)))
                        });
                    }
                    Err(err) => conn.send_error(Some(id), &err),
                }
            }
            method::CHATGPT_COMPLETE => self.call(id, params, conn, |server, p: her_protocol::api::ChatGptCompleteParams| async move {
                server.0.connections.chatgpt.finish(&p.login_id, &p.code, &p.state, p.client_id.as_deref()).await.map_err(crate::connections::rpc_error)
            }),
            method::CHATGPT_LOGOUT => {
                let reply_id = id.clone();
                self.spawn(id.clone(), conn, async move {
                    Some(reply(&reply_id, server.0.connections.chatgpt.logout().await.map_err(crate::connections::rpc_error)))
                });
            }
            method::CHATGPT_CANCEL => {
                let result = her_rpc::params::<her_protocol::api::ChatGptCancelParams>(params)
                    .and_then(|p| server.0.connections.chatgpt.cancel(&p.login_id).map_err(crate::connections::rpc_error));
                conn.send(reply(id, result));
            }
            method::CONNECTIONS_LIST => conn.send(reply(id, server.0.connections.snapshot().map_err(crate::connections::rpc_error))),
            method::CONNECTIONS_DISCOVER => {
                let reply_id = id.clone();
                self.spawn(id.clone(), conn, async move {
                    Some(reply(&reply_id, server.0.connections.discover().await.map(|discoveries| her_protocol::api::ConnectionsDiscoveryResult{discoveries}).map_err(crate::connections::rpc_error)))
                });
            }
            method::CONNECTIONS_CONNECT => match her_rpc::params::<her_protocol::api::ConnectionConnectParams>(params) {
                Ok(p) => {
                    let reply_id = id.clone();
                    self.spawn(id.clone(), conn, async move {
                        Some(reply(&reply_id, server.0.connections.connect(p).await.map_err(crate::connections::rpc_error)))
                    });
                }
                Err(error) => conn.send_error(Some(id), &error),
            },
            method::CONNECTIONS_RETRY | method::CONNECTIONS_DISCONNECT | method::CONNECTIONS_REMOVE | method::CONNECTIONS_MODELS => match her_rpc::params::<her_protocol::api::ConnectionIdParams>(params) {
                Ok(p) => {
                    let reply_id = id.clone(); let method = method_name.to_string();
                    self.spawn(id.clone(), conn, async move {
                        let manager = &server.0.connections;
                        let result = match method.as_str() {
                            method::CONNECTIONS_DISCONNECT => manager.disconnect(&p.connection_id).await,
                            method::CONNECTIONS_REMOVE => manager.remove(&p.connection_id).await,
                            _ => manager.retry(&p.connection_id).await,
                        };
                        Some(reply(&reply_id, result.map_err(crate::connections::rpc_error)))
                    });
                }
                Err(error) => conn.send_error(Some(id), &error),
            },
            method::CONFIG_READ => {
                let conn2 = conn.clone();
                self.call(id, params, conn, move |server, p: ConfigReadParams| async move { server.config_read(p, &conn2).await })
            }
            method::CONFIG_WRITE => self.call(id, params, conn, |server, p: ConfigWriteParams| async move { server.config_write(p).await }),
            method::CREDENTIALS_SET => self.call(id, params, conn, |server, p: CredentialsSetParams| async move { server.credentials_set(p).await }),
            method::CREDENTIALS_DELETE => {
                self.call(id, params, conn, |server, p: CredentialsDeleteParams| async move { server.credentials_delete(p).await })
            }
            method::WEB_SEARCH_STATUS => {
                let reply_id = id.clone();
                let server = self.server.clone();
                let conn2 = conn.clone();
                self.spawn(id.clone(), conn, async move { Some(reply(&reply_id, server.web_search_status(&conn2).await)) });
            }
            method::WEB_SEARCH_TEST => self.call(id, params, conn, |server, p: her_protocol::web_search::WebSearchTestParams| async move { server.web_search_test(p).await }),
            method::WEB_SEARCH_SET_KEY => self.call(id, params, conn, |server, p: her_protocol::web_search::WebSearchSetKeyParams| async move { server.web_search_set_key(p).await }),
            method::SKILLS_INSTALL => self.call(id, params, conn, |server, p: SkillsInstallParams| async move { server.skills_install(p).await }),
            method::SKILLS_REMOVE => self.call(id, params, conn, |server, p: SkillsRemoveParams| async move { server.skills_remove(p).await }),
            method::SKILLS_SET_ENABLED => {
                self.call(id, params, conn, |server, p: SkillsSetEnabledParams| async move { server.skills_set_enabled(p).await })
            }
            method::FILES_LIST => self.call(id, params, conn, |server, p: FilesListParams| async move { server.files_list(p).await }),
            method::FILES_READ => self.call(id, params, conn, |server, p: FilesReadParams| async move { server.files_read(p).await }),
            method::PPTX_READ => {
                let conn2 = conn.clone();
                self.call(id, params, conn, move |server, p: her_protocol::pptx::PptxReadParams| async move { server.pptx_read(p, &conn2).await })
            }
            method::PPTX_WRITE => self.call(id, params, conn, |server, p: her_protocol::pptx::PptxWriteParams| async move { server.pptx_write(p).await }),
            method::DECK_OPEN => match her_rpc::params::<her_protocol::deck::DeckOpenParams>(params) {
                Ok(p) => {
                    let (reply_id, conn2) = (id.clone(), conn.clone());
                    self.spawn(id.clone(), conn, async move { Some(reply(&reply_id, server.deck_open(p, &conn2).await)) });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::DECK_SYNC => self.call(id, params, conn, |server, p: her_protocol::deck::DeckSyncParams| async move { server.deck_sync(p).await }),
            method::DECK_SAVE => self.call(id, params, conn, |server, p: her_protocol::deck::DeckSaveParams| async move { server.deck_save(p).await }),
            method::DECK_CLOSE => self.call(id, params, conn, |server, p: her_protocol::deck::DeckIdParams| async move { server.deck_close(p).await }),
            method::DECK_UNDO => self.call(id, params, conn, |server, p: her_protocol::deck::DeckUndoParams| async move { server.deck_undo(p).await }),
            method::DOCX_OPEN => match her_rpc::params::<her_protocol::docx::DocxOpenParams>(params) {
                Ok(p) => {
                    let (reply_id, conn2) = (id.clone(), conn.clone());
                    self.spawn(id.clone(), conn, async move { Some(reply(&reply_id, server.docx_open(p, &conn2).await)) });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::DOCX_SYNC => self.call(id, params, conn, |server, p: her_protocol::docx::DocxSyncParams| async move { server.docx_sync(p).await }),
            method::DOCX_SAVE => self.call(id, params, conn, |server, p: her_protocol::docx::DocxSaveParams| async move { server.docx_save(p).await }),
            method::DOCX_CLOSE => self.call(id, params, conn, |server, p: her_protocol::docx::DocxIdParams| async move { server.docx_close(p).await }),
            method::DOCX_UNDO => self.call(id, params, conn, |server, p: her_protocol::docx::DocxUndoParams| async move { server.docx_undo(p).await }),
            method::DOCX_REVIEW => self.call(id, params, conn, |server, p: her_protocol::docx::DocxReviewParams| async move { server.docx_review(p).await }),
            method::PPTX_METAFILE => self.call(id, params, conn, |server, p: her_protocol::pptx::PptxMetafileParams| async move { server.pptx_metafile(p).await }),
            method::FILES_WRITE => self.call(id, params, conn, |server, p: FilesWriteParams| async move { server.files_write(p).await }),
            method::FILES_WATCH => {
                let conn2 = conn.clone();
                self.call(id, params, conn, move |server, p: FilesWatchParams| async move { server.files_watch(p, &conn2).await })
            }
            method::SKILLS_FILES => self.call(id, params, conn, |server, p: SkillsFilesParams| async move { server.skills_files(p).await }),
            method::SKILLS_READ => self.call(id, params, conn, |server, p: SkillsReadParams| async move { server.skills_read(p).await }),
            method::MCP_LIST => {
                let conn2 = conn.clone();
                self.call(id, params, conn, move |server, p: McpListParams| async move { server.mcp_list(p, &conn2).await })
            }
            method::MCP_TOOLS => self.call(id, params, conn, |server, p: McpServerParams| async move { server.mcp_tools(p).await }),
            method::MCP_CHECK => self.call(id, params, conn, |server, p: McpServerParams| async move { server.mcp_check(p).await }),
            method::MCP_PROBE => {
                let conn2 = conn.clone();
                self.call(id, params, conn, move |server, p: McpProbeParams| async move { server.mcp_probe(p, &conn2).await })
            }
            method::MCP_PROBE_CANCEL => self.call(id, params, conn, |server, p: McpProbeCancelParams| async move { server.mcp_probe_cancel(p).await }),
            method::MCP_LOGIN => self.call(id, params, conn, |server, p: McpLoginParams| async move { server.mcp_login(p).await }),
            method::MCP_LOGIN_CANCEL => self.call(id, params, conn, |server, p: McpLogoutParams| async move { server.mcp_login_cancel(p).await }),
            method::MCP_LOGIN_COMPLETE => self.call(id, params, conn, |server, p: McpLoginCompleteParams| async move { server.mcp_login_complete(p).await }),
            method::CONNECTORS_INSTALL => self.call(id, params, conn, |server, p: ConnectorInstallParams| async move { server.connector_install(p).await }),
            method::BROWSER_HOST => {
                use her_protocol::api::BrowserHostKind;
                // No params at all is an older desktop app.
                let kind = match params {
                    None => BrowserHostKind::Embedded,
                    Some(_) => match her_rpc::params::<her_protocol::api::BrowserHostParams>(params) {
                        Ok(p) => p.kind,
                        Err(err) => return conn.send_error(Some(id), &err),
                    },
                };
                // Only her's own native messaging host lends the user's Chrome (D-198).
                if kind == BrowserHostKind::Extension
                    && let Err(reason) = crate::peer::is_this_program(conn.peer_pid)
                {
                    let message = format!("not her's extension host: {reason}");
                    server.host_refused(&message);
                    // The host exits on this kind, and the extension starts the current one.
                    return conn.send_error(Some(id), &RpcError {
                        data: Some(json!({"kind": her_protocol::api::HOST_REFUSED})),
                        ..RpcError::invalid_request(message)
                    });
                }
                let p = match (kind, params) {
                    (BrowserHostKind::Extension, Some(_)) => her_rpc::params::<her_protocol::api::BrowserHostParams>(params).unwrap_or_default(),
                    _ => Default::default(),
                };
                // An extension from before bridge versions speaks version 1 (D-219).
                let range = p.bridge.unwrap_or(her_protocol::api::BridgeRange { min: 1, max: 1 });
                let bridge = her_browser::methods::negotiate(range.min, range.max);
                // A later host replaces the one before it on this connection (browser-design §8.3.1).
                let keeps_tabs = kind == BrowserHostKind::Extension && bridge.is_ok_and(|b| b >= 2);
                let link = Arc::new(crate::browser_host::HostLink::new(conn.clone(), keeps_tabs));
                if let Some((before, _)) = self.browser_host.lock().replace((link.clone(), kind)) {
                    before.close();
                }
                let mut result = her_protocol::api::BrowserHostResult::default();
                match kind {
                    BrowserHostKind::Embedded => server.0.browser_pool.set_host(Some(link)),
                    BrowserHostKind::Extension => {
                        use her_browser::EmbeddedHost;
                        use her_browser::methods::BridgeMismatch;
                        use her_protocol::api::ExtensionProblem;
                        let problem = bridge.err().map(|mismatch| match mismatch {
                            BridgeMismatch::ExtensionTooOld => ExtensionProblem::UpdateExtension,
                            BridgeMismatch::HerTooOld => ExtensionProblem::UpdateHer,
                        });
                        let status = her_protocol::api::ExtensionStatus {
                            browser: p.browser,
                            extension_version: p.extension_version,
                            problem,
                            instance: p.instance.clone(),
                            account: p.account,
                            in_use: false,
                        };
                        server.extension_connected(link.id(), conn.clone(), status);
                        if bridge.is_ok() {
                            server.0.browser_pool.add_extension(link, p.instance);
                        } else {
                            tracing::warn!(?range, ?problem, "an extension her cannot use connected");
                        }
                        result = her_protocol::api::BrowserHostResult { bridge: bridge.ok(), problem };
                    }
                }
                conn.send(reply(id, Ok::<_, RpcError>(result)));
                server.browser_changed();
                server.tell_extensions();
            }
            method::DOCX_HOST => {
                let link = Arc::new(crate::docx_host::HostLink::new(conn.clone()));
                if let Some(before) = self.docx_host.lock().replace(link.clone()) {
                    before.close();
                    server.0.docx_host.remove(&before);
                }
                server.0.docx_host.set(link);
                conn.send(reply(id, Ok::<_, RpcError>(json!({}))));
            }
            method::BROWSER_REVEAL => match her_rpc::params::<her_protocol::api::BrowserRevealParams>(params) {
                Ok(p) => conn.send(reply(id, server.reveal_tab(p).map(|()| json!({})))),
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::BROWSER_CLOSE_TABS => match her_rpc::params::<her_protocol::api::BrowserCloseTabsParams>(params) {
                Ok(p) => conn.send(reply(id, server.close_tabs(p).map(|()| json!({})))),
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::BROWSER_ALLOW => match her_rpc::params::<her_protocol::api::BrowserSessionParams>(params) {
                Ok(p) => {
                    server.0.browser_pool.allow(&p.session_id);
                    server.browser_changed();
                    conn.send(reply(id, Ok::<_, RpcError>(json!({}))));
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::BROWSER_CANCEL_WAIT => match her_rpc::params::<her_protocol::api::BrowserSessionParams>(params) {
                Ok(p) => {
                    server.0.browser_pool.decline(&p.session_id);
                    conn.send(reply(id, Ok::<_, RpcError>(json!({}))));
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::BROWSER_USE_EXTENSION => match her_rpc::params::<her_protocol::api::BrowserUseExtensionParams>(params) {
                Ok(p) => {
                    server.use_extension(&p.instance);
                    conn.send(reply(id, Ok::<_, RpcError>(json!({}))));
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::BROWSER_ASSIGN_TAB => match her_rpc::params::<her_protocol::api::BrowserAssignTabParams>(params) {
                Ok(p) => conn.send(reply(id, server.assign_tab(p).map(|()| json!({})))),
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::BROWSER_STATUS => {
                server.watch_browser(conn);
                conn.send(reply(id, Ok::<_, RpcError>(server.browser_status())));
            }
            method::DESKTOP_STATUS => {
                let reply_id = id.clone();
                self.spawn(id.clone(), conn, async move { Some(reply(&reply_id, server.desktop_status().await)) });
            }
            method::BROWSER_SITE_ICON => self.call(id, params, conn, |server, p: BrowserSiteIconParams| async move { Ok::<_, RpcError>(server.site_icon(p).await) }),
            method::MCP_LOGOUT => self.call(id, params, conn, |server, p: McpLogoutParams| async move { server.mcp_logout(p).await }),
            method::SKILLS_LIST => match her_rpc::params::<SkillsListParams>(params) {
                Ok(p) => {
                    if p.session_id.is_none() {
                        server.watch_skills(conn, p.cwd.as_deref());
                    }
                    let reply_id = id.clone();
                    self.spawn(id.clone(), conn, async move { Some(reply(&reply_id, server.list_skills(p).await)) });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::TOOLS_LIST => self.call(id, params, conn, |server, p: ToolsListParams| async move { server.list_tools(p).await }),
            method::MODELS_LIST => {
                server.watch_models(conn);
                // Saved keys may be read from the keychain on the first listing.
                let (conn, id) = (conn.clone(), id.clone());
                tokio::spawn(async move {
                    server.refresh_local_models().await;
                    let result: Result<ModelsListResult, RpcError> =
                        tokio::task::spawn_blocking(move || server.list_models())
                            .await
                            .map_err(|e| RpcError::internal(e.to_string()));
                    conn.send(reply(&id, result));
                });
            }
            method::SERVER_SHUTDOWN => {
                conn.send(her_rpc::response(id, &json!({})));
                server.request_shutdown();
            }
            method::SESSIONS_SUBSCRIBE => {
                match her_rpc::params::<Option<SessionListParams>>(params) {
                    Ok(p) => {
                        let (conn, id) = (conn.clone(), id.clone());
                        tokio::task::spawn_blocking(move || {
                            server.subscribe_sessions(&conn, &p.unwrap_or_default(), |result| {
                                reply(&id, result)
                            });
                        });
                    }
                    Err(err) => conn.send_error(Some(id), &err),
                }
            }
            method::SESSIONS_UNSUBSCRIBE => {
                server.unsubscribe_sessions(conn);
                conn.send(her_rpc::response(id, &json!({})));
            }
            method::BOTS_SUBSCRIBE => {
                let (conn, id) = (conn.clone(), id.clone());
                tokio::task::spawn_blocking(move || server.bots_subscribe(&conn, |result| reply(&id, result)));
            }
            method::BOTS_UNSUBSCRIBE => {
                server.bots_unsubscribe(conn);
                conn.send(her_rpc::response(id, &json!({})));
            }
            method::BOTS_CREATE => match her_rpc::params::<BotCreateParams>(params) {
                Ok(p) => {
                    let (conn, id) = (conn.clone(), id.clone());
                    tokio::task::spawn_blocking(move || conn.send(reply(&id, server.bots_create(p))));
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::BOTS_UPDATE => match her_rpc::params::<BotUpdateParams>(params) {
                Ok(p) => {
                    let (conn, id) = (conn.clone(), id.clone());
                    tokio::task::spawn_blocking(move || conn.send(reply(&id, server.bots_update(p))));
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::BOTS_AVATAR | method::BOTS_READ | method::BOTS_UNREAD => match her_rpc::params::<BotIdParams>(params) {
                Ok(p) => {
                    let (conn, id, name) = (conn.clone(), id.clone(), method_name.to_string());
                    tokio::task::spawn_blocking(move || {
                        let result = match name.as_str() {
                            method::BOTS_AVATAR => server.bots_avatar(&p.bot_id).map(|r| json!(r)),
                            method::BOTS_READ => server.bots_set_unread(&p.bot_id, 0).map(|()| json!({})),
                            _ => server.bots_set_unread(&p.bot_id, 1).map(|()| json!({})),
                        };
                        conn.send(reply(&id, result));
                    });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::BOTS_OPEN => match her_rpc::params::<BotOpenParams>(params) {
                Ok(p) => {
                    // Answered as session/new is, whether the conversation is new or not.
                    let attach = Self::attach_request(conn, id, p.events, p.history_limit, true);
                    let reply_id = id.clone();
                    self.spawn(id.clone(), conn, async move {
                        match server.bots_open(&p.bot_id, attach).await {
                            Ok(()) => None,
                            Err(err) => Some(her_rpc::error_response(Some(&reply_id), &err)),
                        }
                    });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::BOTS_CLEAR | method::BOTS_DELETE => match her_rpc::params::<BotIdParams>(params) {
                Ok(p) => {
                    let (conn, id, clear) = (conn.clone(), id.clone(), method_name == method::BOTS_CLEAR);
                    tokio::spawn(async move {
                        let result = if clear { server.bots_clear(&p.bot_id).await } else { server.bots_delete(&p.bot_id).await };
                        conn.send(reply(&id, result.map(|()| json!({}))));
                    });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::BOTS_ROUTINE | method::BOTS_ROUTINE_RUN_NOW | method::BOTS_ROUTINE_STOP | method::BOTS_ROUTINE_DELETE => {
                match her_rpc::params::<her_protocol::bot::BotRoutineParams>(params) {
                    Ok(p) => {
                        let (conn, id, name) = (conn.clone(), id.clone(), method_name.to_string());
                        tokio::spawn(async move {
                            let (bot, routine) = (p.bot_id.as_str(), p.routine_id.as_str());
                            let result = match name.as_str() {
                                method::BOTS_ROUTINE => server.bots_routine(bot, routine).await.map(|r| json!(r)),
                                method::BOTS_ROUTINE_RUN_NOW => server.bots_routine_run_now(bot, routine).await.map(|()| json!({})),
                                method::BOTS_ROUTINE_STOP => server.bots_routine_stop(bot, routine).await.map(|()| json!({})),
                                _ => server.bots_routine_delete(bot, routine).await.map(|()| json!({})),
                            };
                            conn.send(reply(&id, result));
                        });
                    }
                    Err(err) => conn.send_error(Some(id), &err),
                }
            }
            method::BOTS_ROUTINE_RUNS => match her_rpc::params::<her_protocol::bot::BotRoutineRunsParams>(params) {
                Ok(p) => {
                    let (conn, id) = (conn.clone(), id.clone());
                    tokio::spawn(async move {
                        conn.send(reply(&id, server.bots_routine_runs(&p.bot_id, &p.routine_id, p.limit, p.before).await));
                    });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::BOTS_ROUTINE_RUN => match her_rpc::params::<her_protocol::bot::BotRoutineRunParams>(params) {
                Ok(p) => {
                    let (conn, id) = (conn.clone(), id.clone());
                    tokio::spawn(async move {
                        conn.send(reply(&id, server.bots_routine_run(&p.bot_id, &p.routine_id, p.run_id).await));
                    });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::BOTS_ROUTINE_SET_ENABLED => match her_rpc::params::<her_protocol::bot::BotRoutineSetEnabledParams>(params) {
                Ok(p) => {
                    let (conn, id) = (conn.clone(), id.clone());
                    tokio::spawn(async move {
                        let result = server.bots_routine_set_enabled(&p.bot_id, &p.routine_id, p.enabled).await;
                        conn.send(reply(&id, result.map(|()| json!({}))));
                    });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::DOC_OPEN => match her_rpc::params::<her_protocol::doc::DocOpenParams>(params) {
                Ok(p) => {
                    server.watch_docs(conn);
                    let reply_id = id.clone();
                    self.spawn(id.clone(), conn, async move { Some(reply(&reply_id, server.doc_open(p).await)) });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::DOC_CLOSE => match her_rpc::params::<her_protocol::doc::DocIdParams>(params) {
                Ok(p) => {
                    let reply_id = id.clone();
                    self.spawn(id.clone(), conn, async move { Some(reply(&reply_id, server.doc_close(p).await)) });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::DOC_VIEW => match her_rpc::params::<her_protocol::doc::DocViewParams>(params) {
                Ok(p) => conn.send(reply(id, server.doc_view(p))),
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::DOC_LOCATE => match her_rpc::params::<her_protocol::doc::DocLocateParams>(params) {
                Ok(p) => {
                    let reply_id = id.clone();
                    self.spawn(id.clone(), conn, async move { Some(reply(&reply_id, server.doc_locate(p).await)) });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::DOC_CELLS => match her_rpc::params::<her_protocol::doc::DocCellsParams>(params) {
                Ok(p) => {
                    let reply_id = id.clone();
                    self.spawn(id.clone(), conn, async move { Some(reply(&reply_id, server.doc_cells(p).await)) });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::DOC_APPLY => match her_rpc::params::<her_protocol::doc::DocApplyParams>(params) {
                Ok(p) => {
                    let reply_id = id.clone();
                    self.spawn(id.clone(), conn, async move { Some(reply(&reply_id, server.doc_apply(p).await)) });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::DOC_UNDO => match her_rpc::params::<her_protocol::doc::DocIdParams>(params) {
                Ok(p) => {
                    let reply_id = id.clone();
                    self.spawn(id.clone(), conn, async move { Some(reply(&reply_id, server.doc_undo(p).await)) });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::DOC_REDO => match her_rpc::params::<her_protocol::doc::DocIdParams>(params) {
                Ok(p) => {
                    let reply_id = id.clone();
                    self.spawn(id.clone(), conn, async move { Some(reply(&reply_id, server.doc_redo(p).await)) });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::DOC_RECALC => match her_rpc::params::<her_protocol::doc::DocRecalcParams>(params) {
                Ok(p) => {
                    let reply_id = id.clone();
                    self.spawn(id.clone(), conn, async move { Some(reply(&reply_id, server.doc_recalc(p).await)) });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::DOC_TRACE => match her_rpc::params::<her_protocol::doc::DocTraceParams>(params) {
                Ok(p) => {
                    let reply_id = id.clone();
                    self.spawn(id.clone(), conn, async move { Some(reply(&reply_id, server.doc_trace(p).await)) });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::DOC_FIND => match her_rpc::params::<her_protocol::doc::DocFindParams>(params) {
                Ok(p) => {
                    let reply_id = id.clone();
                    self.spawn(id.clone(), conn, async move { Some(reply(&reply_id, server.doc_find(p).await)) });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::DOC_FILL => match her_rpc::params::<her_protocol::doc::DocFillParams>(params) {
                Ok(p) => {
                    let reply_id = id.clone();
                    self.spawn(id.clone(), conn, async move { Some(reply(&reply_id, server.doc_fill(p).await)) });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::DOC_STRUCTURE => match her_rpc::params::<her_protocol::doc::DocStructureParams>(params) {
                Ok(p) => {
                    let reply_id = id.clone();
                    self.spawn(id.clone(), conn, async move { Some(reply(&reply_id, server.doc_structure(p).await)) });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::DOC_SORT => match her_rpc::params::<her_protocol::doc::DocSortParams>(params) {
                Ok(p) => {
                    let reply_id = id.clone();
                    self.spawn(id.clone(), conn, async move { Some(reply(&reply_id, server.doc_sort(p).await)) });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::DOC_AUTOFILL => match her_rpc::params::<her_protocol::doc::DocAutofillParams>(params) {
                Ok(p) => {
                    let reply_id = id.clone();
                    self.spawn(id.clone(), conn, async move { Some(reply(&reply_id, server.doc_autofill(p).await)) });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::DOC_SAVE => match her_rpc::params::<her_protocol::doc::DocSaveParams>(params) {
                Ok(p) => {
                    let reply_id = id.clone();
                    self.spawn(id.clone(), conn, async move { Some(reply(&reply_id, server.doc_save(p).await)) });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::DOC_RESOLVE => match her_rpc::params::<her_protocol::doc::DocResolveParams>(params) {
                Ok(p) => {
                    let reply_id = id.clone();
                    self.spawn(id.clone(), conn, async move { Some(reply(&reply_id, server.doc_resolve(p).await)) });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::SESSION_RENAME | method::SESSION_ARCHIVE | method::SESSION_UNARCHIVE => {
                let parsed = match method_name {
                    method::SESSION_RENAME => her_rpc::params::<SessionRenameParams>(params)
                        .map(|p| (p.session_id, Op::SetTitle { title: p.title })),
                    archive => her_rpc::params::<SessionIdParams>(params).map(|p| {
                        let archived = archive == method::SESSION_ARCHIVE;
                        (p.session_id, Op::SetArchived { archived })
                    }),
                };
                match parsed {
                    Ok((session_id, op)) => {
                        let (conn, id) = (conn.clone(), id.clone());
                        tokio::spawn(async move {
                            let result = server
                                .set_metadata(&session_id, op)
                                .await
                                .map(|()| json!({}));
                            conn.send(reply(&id, result));
                        });
                    }
                    Err(err) => conn.send_error(Some(id), &err),
                }
            }
            method::AUTH_SET => match her_rpc::params::<AuthSetParams>(params) {
                Ok(p) => {
                    let reply_id = id.clone();
                    self.spawn(id.clone(), conn, async move {
                        Some(reply(&reply_id, server.set_key(&p.provider, &p.api_key).await))
                    });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::AUTH_DELETE => match her_rpc::params::<AuthDeleteParams>(params) {
                Ok(p) => {
                    let (conn, id) = (conn.clone(), id.clone());
                    tokio::spawn(async move {
                        let result = tokio::task::spawn_blocking(move || server.delete_key(&p.provider))
                            .await
                            .unwrap_or_else(|e| Err(RpcError::internal(e.to_string())))
                            .map(|()| json!({}));
                        conn.send(reply(&id, result));
                    });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::MODELS_REFRESH => match her_rpc::params::<Option<ModelsRefreshParams>>(params) {
                Ok(p) => {
                    let reply_id = id.clone();
                    self.spawn(id.clone(), conn, async move {
                        Some(reply(&reply_id, server.refresh_models(p.unwrap_or_default()).await))
                    });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            method::MODELS_SHOW => match her_rpc::params::<ModelsShowParams>(params) {
                Ok(p) => {
                    let (conn, id) = (conn.clone(), id.clone());
                    tokio::spawn(async move {
                        let result = tokio::task::spawn_blocking(move || server.show_model(&p.model, p.shown))
                            .await
                            .unwrap_or_else(|e| Err(RpcError::internal(e.to_string())))
                            .map(|()| json!({}));
                        conn.send(reply(&id, result));
                    });
                }
                Err(err) => conn.send_error(Some(id), &err),
            },
            other => conn.send_error(Some(id), &RpcError::method_not_found(other)),
        }
    }
}

impl Protocol for NativeProtocol {
    fn on_line(self: Arc<Self>, line: Bytes, conn: &ConnHandle) {
        let message = match RawMessage::parse(&line) {
            Ok(message) => message,
            Err(err) => {
                conn.send_error(None, &err);
                return;
            }
        };
        match message.kind() {
            Kind::Request { id, method, params } => self.request(id, method, params, conn),
            Kind::Notification { method, params } => {
                if method == method::CANCEL_REQUEST
                    && let Ok(p) = her_rpc::params::<CancelParams>(params)
                {
                    self.cancel(&p.id, conn);
                } else if method == method::BROWSER_CDP
                    && let Some((host, _)) = self.browser_host.lock().clone()
                    && let Ok(p) = her_rpc::params::<her_protocol::api::BrowserCdpParams>(params)
                {
                    host.receive(p.stream, Box::<str>::from(p.data).into_string());
                } else if method == method::BROWSER_EXTENSION
                    && let Some((host, her_protocol::api::BrowserHostKind::Extension)) = self.browser_host.lock().clone()
                    && let Ok(p) = her_rpc::params::<her_protocol::api::ExtensionMessage>(params)
                {
                    use her_browser::EmbeddedHost;
                    self.server.extension_message(host.id(), p);
                }
            }
            Kind::Response { id, result } => {
                if let Some(host) = self.docx_host.lock().clone() {
                    host.answer(id, result);
                }
            }
            Kind::Invalid => conn.send_error(
                message.id.as_ref(),
                &RpcError::invalid_request("not a request, notification or response"),
            ),
        }
    }

    fn encoder(&self, _conn: &ConnHandle) -> Box<dyn Encoder> {
        Box::new(NativeEncoder)
    }

    fn on_closed(&self, _conn: &ConnHandle) {
        for (_, task) in self.running.lock().drain() {
            task.abort();
        }
        if let Some(host) = self.docx_host.lock().take() {
            host.close();
            self.server.0.docx_host.remove(&host);
        }
        if let Some((host, kind)) = self.browser_host.lock().take() {
            use her_browser::EmbeddedHost;
            host.close();
            match kind {
                her_protocol::api::BrowserHostKind::Embedded => self.server.0.browser_pool.remove_host(host.id()),
                her_protocol::api::BrowserHostKind::Extension => {
                    self.server.0.browser_pool.remove_extension(host.id());
                    self.server.extension_gone(host.id());
                }
            }
            self.server.browser_changed();
            self.server.tell_extensions();
        }
    }
}

struct NativeEncoder;

impl Encoder for NativeEncoder {
    fn encode(&mut self, item: Outgoing, out: &mut Vec<Bytes>) {
        match item {
            Outgoing::Event(frame) => out.push(frame.native().clone()),
            Outgoing::Frame(bytes) => out.push(bytes),
            Outgoing::Detached(params) => out.push(Bytes::from(her_rpc::notification(
                method::SESSION_DETACHED,
                &params,
            ))),
            Outgoing::Control(_) => {}
        }
    }
}
