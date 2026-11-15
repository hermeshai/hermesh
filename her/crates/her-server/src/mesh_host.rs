//! The mesh host: a connection that called `mesh/host` gives a bot's own conversation the `mesh`
//! group of deferred tools. The kernel sends it `mesh/describe` before the user is asked about a
//! call that writes and `mesh/call` to run a tool, and it sends `mesh/news`, which wakes the
//! bot's conversation. The group's words are all the host's.

use async_trait::async_trait;
use her_core::mesh::MeshHost;
use her_protocol::api::MeshCallParams;
use her_protocol::api::MeshCallResult;
use her_protocol::api::MeshDescribeParams;
use her_protocol::api::MeshDescribeResult;
use her_protocol::api::MeshHostParams;
use her_protocol::api::MeshNewsParams;
use her_protocol::api::method;
use her_rpc::Id;
use her_rpc::RpcError;
use parking_lot::Mutex;
use serde::Serialize;
use serde::de::DeserializeOwned;
use serde_json::Value;
use serde_json::value::RawValue;
use std::collections::HashMap;
use std::sync::Arc;
use std::sync::atomic::AtomicU64;
use std::sync::atomic::Ordering;
use std::time::Duration;
use tokio::sync::oneshot;

use crate::conn::ConnHandle;
use crate::hub::HubCmd;
use crate::server::Server;

/// How long the host may take to say what a call would do: the user waits to be asked.
const DESCRIBE_TIMEOUT: Duration = Duration::from_secs(15);
/// How long the host may take to run one tool.
const CALL_TIMEOUT: Duration = Duration::from_secs(120);

type Answer = Result<Box<RawValue>, String>;

/// One `mesh/host` connection and its requests waiting for an answer. Their ids are strings, so
/// that they never meet those of `docx/serialize` on a connection that hosts both.
pub(crate) struct HostLink {
    id: u64,
    conn: ConnHandle,
    next: AtomicU64,
    pending: Mutex<HashMap<String, oneshot::Sender<Answer>>>,
}

impl HostLink {
    pub(crate) fn new(conn: ConnHandle) -> Self {
        static NEXT: AtomicU64 = AtomicU64::new(1);
        Self {
            id: NEXT.fetch_add(1, Ordering::Relaxed),
            conn,
            next: AtomicU64::new(1),
            pending: Mutex::new(HashMap::new()),
        }
    }

    /// A response from the host; one to a request no longer waited for is dropped.
    pub(crate) fn answer(&self, id: &Id, result: Result<&RawValue, &RpcError>) {
        let Id::Str(id) = id else { return };
        let Some(tx) = self.pending.lock().remove(id) else { return };
        let _ = tx.send(result.map(RawValue::to_owned).map_err(|err| err.message.clone()));
    }

    /// The host left: what waits on it fails.
    pub(crate) fn close(&self) {
        self.pending.lock().clear();
    }
}

#[derive(Default)]
struct Current {
    link: Option<Arc<HostLink>>,
    /// None while the host offers no tools.
    group: Option<Arc<MeshHostParams>>,
    /// Goes up whenever the group changes.
    number: u64,
}

/// The current host and its group, if any: the one that registered last.
#[derive(Default)]
pub(crate) struct MeshHosts {
    current: Mutex<Current>,
}

impl MeshHosts {
    /// `link` is the host from now on, with `group`; one without tools leaves no group.
    pub(crate) fn set(&self, link: Arc<HostLink>, group: MeshHostParams) {
        let mut current = self.current.lock();
        if let Some(before) = current.link.replace(link.clone())
            && before.id != link.id
        {
            before.close();
        }
        current.group = (!group.tools.is_empty()).then(|| Arc::new(group));
        current.number += 1;
    }

    /// Unregisters `link`, unless a later host has replaced it.
    pub(crate) fn remove(&self, link: &HostLink) {
        let mut current = self.current.lock();
        if current.link.as_ref().is_some_and(|c| c.id == link.id) {
            current.link = None;
            current.group = None;
            current.number += 1;
        }
    }

    fn group(&self) -> (u64, Option<Arc<MeshHostParams>>) {
        let current = self.current.lock();
        (current.number, current.group.clone())
    }

    /// Sends the host a request and waits for its answer. An error is one sentence for the model.
    async fn request<P: Serialize, R: DeserializeOwned>(&self, method: &str, params: &P, timeout: Duration) -> Result<R, String> {
        const NO_HOST: &str = "The mesh host is not connected, so these tools cannot run now.";
        let link = self.current.lock().link.clone().ok_or(NO_HOST)?;
        if link.conn.queue.is_closed() {
            return Err(NO_HOST.into());
        }
        let id = format!("mesh-{}", link.next.fetch_add(1, Ordering::Relaxed));
        let (tx, rx) = oneshot::channel();
        link.pending.lock().insert(id.clone(), tx);
        link.conn.send(her_rpc::request(&Id::Str(id.clone()), method, params));
        match tokio::time::timeout(timeout, rx).await {
            Ok(Ok(Ok(raw))) => serde_json::from_str(raw.get()).map_err(|err| format!("The mesh host gave a malformed answer: {err}.")),
            Ok(Ok(Err(message))) => Err(format!("The mesh host failed: {message}")),
            Ok(Err(_)) => Err("The mesh host left before it answered.".into()),
            Err(_) => {
                link.pending.lock().remove(&id);
                Err(format!("The mesh host did not answer within {} seconds.", timeout.as_secs()))
            }
        }
    }
}

/// The mesh host as one bot's conversation reaches it.
pub(crate) struct BotMesh {
    pub(crate) hosts: Arc<MeshHosts>,
    pub(crate) bot: String,
}

#[async_trait]
impl MeshHost for BotMesh {
    fn group(&self) -> (u64, Option<Arc<MeshHostParams>>) {
        self.hosts.group()
    }

    async fn describe(&self, name: &str, arguments: &Value) -> Result<MeshDescribeResult, String> {
        let params = MeshDescribeParams { bot: self.bot.clone(), name: name.to_string(), arguments: arguments.clone() };
        self.hosts.request(method::MESH_DESCRIBE, &params, DESCRIBE_TIMEOUT).await
    }

    async fn call(&self, call_id: &str, name: &str, arguments: Value) -> Result<MeshCallResult, String> {
        let params = MeshCallParams { bot: self.bot.clone(), call_id: call_id.to_string(), name: name.to_string(), arguments };
        self.hosts.request(method::MESH_CALL, &params, CALL_TIMEOUT).await
    }
}

impl Server {
    /// The mesh host for the conversation of the bot `id`.
    pub(crate) fn bot_mesh(&self, id: &str) -> Arc<dyn MeshHost> {
        Arc::new(BotMesh { hosts: self.0.mesh.clone(), bot: id.to_string() })
    }

    /// `mesh/news`: puts the news into the bot's conversation, loaded for it if need be, as a
    /// routine's run reaches it. Answers once the conversation has it in its history.
    pub(crate) async fn mesh_news(&self, news: MeshNewsParams) -> Result<(), RpcError> {
        self.bot_exists(&news.bot)?;
        // A conversation unloading just now cannot take it, and the next attempt loads it again.
        for _ in 0..2 {
            let hub = self.load_bot_session(&news.bot).await?;
            let (reply, taken) = oneshot::channel();
            if hub.send(HubCmd::News { news: Box::new(news.clone()), reply }).await.is_ok()
                && let Ok(Some(recorded)) = taken.await
            {
                return recorded.await.map_err(|_| RpcError::internal("the bot's conversation closed before it took the news"));
            }
            hub.shared.wait_closed().await;
        }
        Err(RpcError::internal("the bot's conversation kept closing"))
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use her_core::HerHome;
    use serde_json::json;
    use tokio::io::AsyncBufReadExt;
    use tokio::io::AsyncWriteExt;

    use crate::native::NativeProtocol;
    use crate::server::ServerOptions;

    type Lines = tokio::io::Lines<tokio::io::BufReader<tokio::io::ReadHalf<tokio::io::DuplexStream>>>;
    type Writes = tokio::io::WriteHalf<tokio::io::DuplexStream>;

    fn group() -> Value {
        json!({
            "summary": "Work in other workshops",
            "guidelines": ["Never wait for a workshop."],
            "tools": [{"name": "start", "description": "Start work.", "parameters": {"type": "object"}, "writes": true}],
        })
    }

    async fn send(writes: &mut Writes, message: Value) {
        writes.write_all(format!("{message}\n").as_bytes()).await.unwrap();
    }

    async fn next(lines: &mut Lines) -> Value {
        serde_json::from_str(&lines.next_line().await.unwrap().unwrap()).unwrap()
    }

    /// A server and a stand-in host that has called `mesh/host` with `group`.
    async fn hosted() -> (tempfile::TempDir, Server, Lines, Writes, tokio::task::JoinHandle<()>) {
        let dir = tempfile::tempdir().unwrap();
        let home = HerHome::new(dir.path().join("home"));
        home.ensure().unwrap();
        let server = Server::new(ServerOptions {
            home,
            cwd: dir.path().to_path_buf(),
            overrides: Vec::new(),
            mcp_servers: Vec::new(),
            doc_worker: her_docs::Launcher::Thread,
        })
        .unwrap();
        let (client, ours) = tokio::io::duplex(1 << 20);
        let (server_reads, server_writes) = tokio::io::split(ours);
        let connection = tokio::spawn(crate::conn::run_connection(server.clone(), server_reads, server_writes, NativeProtocol::new(server.clone())));
        let (client_reads, mut writes) = tokio::io::split(client);
        let mut lines = tokio::io::BufReader::new(client_reads).lines();
        let init = json!({"protocol_version": her_protocol::PROTOCOL_VERSION, "client_info": {"name": "test"}});
        send(&mut writes, json!({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": init})).await;
        let _init = next(&mut lines).await;
        // News comes only from a host.
        send(&mut writes, json!({"jsonrpc": "2.0", "id": 2, "method": "mesh/news", "params": {"bot": "b", "id": "n", "from": {"id": "w", "name": "W"}, "text": "t"}})).await;
        assert_eq!(next(&mut lines).await["error"]["code"], her_rpc::code::INVALID_REQUEST);
        assert_eq!(server.0.mesh.group(), (0, None));
        send(&mut writes, json!({"jsonrpc": "2.0", "id": 3, "method": "mesh/host", "params": group()})).await;
        assert_eq!(next(&mut lines).await, json!({"jsonrpc": "2.0", "id": 3, "result": {}}));
        (dir, server, lines, writes, connection)
    }

    fn call(server: &Server) -> tokio::task::JoinHandle<Result<MeshCallResult, String>> {
        let mesh = server.bot_mesh("b1");
        tokio::spawn(async move { mesh.call("c7", "start", json!({"workshop": "shed"})).await })
    }

    #[tokio::test]
    async fn the_group_is_the_hosts_and_goes_with_it() {
        let (_dir, server, mut lines, mut writes, connection) = hosted().await;
        let (number, offered) = server.0.mesh.group();
        let offered = offered.expect("the group");
        assert_eq!((offered.summary.as_str(), offered.guidelines.len(), offered.tools[0].name.as_str(), offered.tools[0].writes), ("Work in other workshops", 1, "start", true));

        // Called again, it replaces the group; without tools there is none.
        let mut other = group();
        other["summary"] = json!("Other words");
        send(&mut writes, json!({"jsonrpc": "2.0", "id": 4, "method": "mesh/host", "params": other})).await;
        next(&mut lines).await;
        let (second, offered) = server.0.mesh.group();
        assert!(second != number && offered.unwrap().summary == "Other words");
        send(&mut writes, json!({"jsonrpc": "2.0", "id": 5, "method": "mesh/host", "params": {"summary": "", "guidelines": [], "tools": []}})).await;
        next(&mut lines).await;
        let (third, offered) = server.0.mesh.group();
        assert!(third != second && offered.is_none());
        // The host is still there for what it is asked.
        let waiting = call(&server);
        let request = next(&mut lines).await;
        send(&mut writes, json!({"jsonrpc": "2.0", "id": request["id"], "result": {"output": "ok"}})).await;
        assert_eq!(waiting.await.unwrap(), Ok(MeshCallResult { output: "ok".into(), is_error: false, details: None }));

        send(&mut writes, json!({"jsonrpc": "2.0", "id": 6, "method": "mesh/host", "params": group()})).await;
        next(&mut lines).await;
        assert!(server.0.mesh.group().1.is_some());
        drop((lines, writes));
        tokio::time::timeout(Duration::from_secs(5), connection).await.unwrap().unwrap();
        let (last, offered) = server.0.mesh.group();
        assert!(last != third && offered.is_none(), "the host left with its connection");
        assert_eq!(call(&server).await.unwrap(), Err("The mesh host is not connected, so these tools cannot run now.".to_string()));
    }

    #[tokio::test]
    async fn calls_go_to_the_host_with_the_bot_and_its_answers_come_back() {
        let (_dir, server, mut lines, mut writes, _connection) = hosted().await;
        let waiting = call(&server);
        let request = next(&mut lines).await;
        assert_eq!(request["method"], "mesh/call");
        assert_eq!(request["params"], json!({"bot": "b1", "call_id": "c7", "name": "start", "arguments": {"workshop": "shed"}}));
        send(&mut writes, json!({"jsonrpc": "2.0", "id": request["id"], "result": {"output": "{\"ok\":false}", "is_error": true}})).await;
        assert_eq!(waiting.await.unwrap(), Ok(MeshCallResult { output: "{\"ok\":false}".into(), is_error: true, details: None }));
        // With what the apps read beside the text.
        let waiting = call(&server);
        let request = next(&mut lines).await;
        send(&mut writes, json!({"jsonrpc": "2.0", "id": request["id"], "result": {"output": "{\"ok\":true}", "details": {"session": {"session_id": "s_1"}}}})).await;
        assert_eq!(waiting.await.unwrap(), Ok(MeshCallResult { output: "{\"ok\":true}".into(), is_error: false, details: Some(json!({"session": {"session_id": "s_1"}})) }));

        let mesh = server.bot_mesh("b1");
        let waiting = tokio::spawn(async move { mesh.describe("start", &json!({"workshop": "shed"})).await });
        let request = next(&mut lines).await;
        assert_eq!(request["method"], "mesh/describe");
        assert_eq!(request["params"], json!({"bot": "b1", "name": "start", "arguments": {"workshop": "shed"}}));
        send(&mut writes, json!({"jsonrpc": "2.0", "id": request["id"], "result": {"summary": "Start work in shed", "rule": ["mesh", "shed"]}})).await;
        assert_eq!(waiting.await.unwrap(), Ok(MeshDescribeResult { summary: "Start work in shed".into(), rule: Some(vec!["mesh".into(), "shed".into()]) }));

        // An error answer is the host's message, in a sentence.
        let waiting = call(&server);
        let request = next(&mut lines).await;
        send(&mut writes, json!({"jsonrpc": "2.0", "id": request["id"], "error": {"code": -32000, "message": "no grant"}})).await;
        assert_eq!(waiting.await.unwrap(), Err("The mesh host failed: no grant".to_string()));
    }

    #[tokio::test]
    async fn a_host_that_does_not_answer_times_out_and_one_that_leaves_fails_the_wait() {
        let (_dir, server, mut lines, writes, connection) = hosted().await;
        let params = json!({"bot": "b1"});
        let slow: Result<MeshCallResult, String> = server.0.mesh.request(method::MESH_CALL, &params, Duration::from_millis(100)).await;
        assert_eq!(slow, Err("The mesh host did not answer within 0 seconds.".to_string()));
        let _first = next(&mut lines).await;

        let waiting = call(&server);
        let _second = next(&mut lines).await;
        drop((lines, writes));
        tokio::time::timeout(Duration::from_secs(5), connection).await.unwrap().unwrap();
        assert_eq!(waiting.await.unwrap(), Err("The mesh host left before it answered.".to_string()));
    }

    #[tokio::test]
    async fn news_for_a_bot_that_is_not_there_is_an_error() {
        let (_dir, _server, mut lines, mut writes, _connection) = hosted().await;
        send(&mut writes, json!({"jsonrpc": "2.0", "id": 9, "method": "mesh/news", "params": {"bot": "nobody", "id": "n1", "from": {"id": "w", "name": "W"}, "text": "t"}})).await;
        let answer = next(&mut lines).await;
        assert_eq!((answer["id"].as_i64(), answer["error"]["data"]["kind"].as_str()), (Some(9), Some("bot_not_found")), "{answer}");
    }
}
