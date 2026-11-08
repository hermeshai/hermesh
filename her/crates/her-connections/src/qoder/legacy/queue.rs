//! Qoder's per-model queue on the legacy route. A refused chat (10605) names the task's place;
//! the queue's status says when it may be sent again under the same task, and a task that went
//! through the queue releases its lease when its turn ends, as Qoder's App does
//! (`[ModelQueue]` in its worker log). The waiting itself is the kernel's retry: each status
//! that says "still queued" ends the attempt as overloaded, with the queue's delay.
//! Protocol reference: crates/her-connections/licenses/README.md.
use super::super::cosy;
use serde_json::{Value, json};
use std::{collections::HashMap, time::Duration};

const STATUS_PATH: &str = "/algo/api/v2/service/ask/queue/status";
const FINISH_PATH: &str = "/algo/api/v2/service/ask/finish?Encode=1";
/// Status asks failing in a row after which the place is given up and the chat sent as new.
const FAILURES: u32 = 3;
/// A waiting place nobody asked about for this long was given up: the kernel asks again
/// within the queue's delay, at most 30 s.
const ABANDONED: Duration = Duration::from_secs(90);

/// The account's queued and admitted tasks, shared by every request of the connection.
#[derive(Default)]
pub(crate) struct Queue {
    tasks: parking_lot::Mutex<HashMap<String, Place>>,
}

#[derive(Clone, Debug)]
pub(crate) struct Place {
    pub(crate) task: String,
    pub(crate) model: String,
    pub(crate) queue_type: Option<String>,
    /// The last chat sent for the task, which a status ask names.
    pub(crate) request: String,
    pub(crate) session: String,
    /// Still waiting; false once a chat of the task went through, until its turn ends.
    pub(crate) waiting: bool,
    failures: u32,
    began: std::time::Instant,
    touched: std::time::Instant,
}

/// Where a queued task stands.
#[derive(Clone, Debug, Default, PartialEq)]
pub(crate) struct State {
    pub(crate) queued: bool,
    pub(crate) queue_type: Option<String>,
    pub(crate) retry_after: Option<f64>,
}

impl Queue {
    /// The place of `task`, which is being asked about again.
    pub(crate) fn place(&self, task: &str) -> Option<Place> {
        let mut tasks = self.tasks.lock();
        let place = tasks.get_mut(task)?;
        place.touched = std::time::Instant::now();
        Some(place.clone())
    }

    /// Takes out the places that are over: waiting ones nobody asked about for a while, and
    /// any of `session` but `task`, whose turn ended when the session's next one began.
    pub(crate) fn over(&self, session: &str, task: &str) -> Vec<Place> {
        let mut tasks = self.tasks.lock();
        let gone: Vec<String> = tasks
            .iter()
            .filter(|(key, p)| {
                (p.waiting && p.touched.elapsed() > ABANDONED)
                    || (p.session == session && *key != task)
            })
            .map(|(key, _)| key.clone())
            .collect();
        gone.iter().filter_map(|key| tasks.remove(key)).collect()
    }

    /// A chat of `task` was refused with the queue code.
    pub(crate) fn enter(
        &self,
        task: &str,
        model: &str,
        queue_type: Option<String>,
        request: &str,
        session: &str,
    ) {
        let mut tasks = self.tasks.lock();
        let place = tasks.entry(task.into()).or_insert_with(|| Place {
            task: task.into(),
            model: model.into(),
            queue_type: None,
            request: String::new(),
            session: String::new(),
            waiting: true,
            failures: 0,
            began: std::time::Instant::now(),
            touched: std::time::Instant::now(),
        });
        place.waiting = true;
        place.touched = std::time::Instant::now();
        place.request = request.into();
        place.session = session.into();
        if queue_type.is_some() {
            place.queue_type = queue_type;
        }
    }

    /// A chat of `task` was answered: its lease is held until the turn ends.
    pub(crate) fn admitted(&self, task: &str) {
        if let Some(place) = self.tasks.lock().get_mut(task) {
            place.waiting = false;
            place.failures = 0;
        }
    }

    /// A status ask failed; whether the place is given up.
    pub(crate) fn status_failed(&self, task: &str) -> bool {
        let mut tasks = self.tasks.lock();
        let Some(place) = tasks.get_mut(task) else {
            return true;
        };
        place.failures += 1;
        if place.failures >= FAILURES {
            tasks.remove(task);
            return true;
        }
        false
    }

    /// The turn of `task` ended: its lease, if it holds one, to release.
    pub(crate) fn end(&self, task: &str) -> Option<Place> {
        let mut tasks = self.tasks.lock();
        if tasks.get(task).is_some_and(|p| !p.waiting) {
            return tasks.remove(task);
        }
        None
    }
}

/// The delay before asking or sending again: the queue's own, held to 0.5–30 s as the App
/// holds it, 30 s when it gives none.
pub(crate) fn pause(seconds: Option<f64>) -> Duration {
    Duration::from_secs_f64(seconds.unwrap_or(30.0).clamp(0.5, 30.0))
}

/// The queue state in a body: the object itself or one under `data`, `result`, `message` or
/// `body`, as an object or as JSON text, that says `isQueued` or `serviceAvailable`.
pub(crate) fn state_of(text: &str) -> Option<State> {
    let mut todo = vec![serde_json::from_str::<Value>(text).ok()?];
    let mut seen = 0;
    while let Some(value) = todo.pop() {
        seen += 1;
        if seen > 32 {
            break;
        }
        let flag = |v: &Value| match v {
            Value::Bool(b) => Some(*b),
            Value::String(s) if s == "true" => Some(true),
            Value::String(s) if s == "false" => Some(false),
            _ => None,
        };
        let queued = flag(&value["isQueued"]);
        let available = flag(&value["serviceAvailable"]);
        if queued.is_some() || available.is_some() {
            return Some(State {
                queued: queued == Some(true) || available == Some(false),
                queue_type: value["queueType"]
                    .as_str()
                    .filter(|s| !s.is_empty())
                    .map(str::to_owned),
                retry_after: value["retryAfterSeconds"]
                    .as_f64()
                    .or_else(|| value["retryAfterSeconds"].as_str()?.parse().ok()),
            });
        }
        for key in ["data", "result", "message", "body"] {
            match &value[key] {
                Value::Object(_) => todo.push(value[key].clone()),
                Value::String(s) => {
                    if let Ok(inner) = serde_json::from_str::<Value>(s.trim()) {
                        todo.push(inner);
                    }
                }
                _ => {}
            }
        }
    }
    None
}

/// Who signs the queue's calls.
#[derive(Clone)]
pub(crate) struct Signer {
    pub(crate) base: String,
    pub(crate) uid: String,
    pub(crate) token: String,
    pub(crate) machine: String,
}

/// Asks where `place` stands; `None` when the ask failed or said nothing of the queue.
pub(crate) async fn status(
    http: &reqwest::Client,
    signer: &Signer,
    place: &Place,
) -> Option<State> {
    let mut url = reqwest::Url::parse(&format!("{}{STATUS_PATH}", signer.base)).ok()?;
    url.query_pairs_mut()
        .append_pair("requestSetId", &place.task)
        .append_pair("modelKey", &place.model);
    if let Some(queue_type) = &place.queue_type {
        url.query_pairs_mut().append_pair("queueType", queue_type);
    }
    let mut headers = cosy::signed(
        url.as_str(),
        &signer.uid,
        &signer.token,
        &signer.machine,
        "",
        cosy::Kind::Request,
    )
    .ok()?;
    for (name, value) in [
        ("X-Request-ID", place.request.as_str()),
        ("X-Session-ID", place.session.as_str()),
    ] {
        if let Ok(value) = reqwest::header::HeaderValue::from_str(value) {
            headers.insert(name, value);
        }
    }
    let response = http
        .get(url)
        .headers(headers)
        .timeout(Duration::from_secs(30))
        .send()
        .await
        .ok()?;
    let ok = response.status().is_success();
    let code = response.status().as_u16();
    let text = response.text().await.ok()?;
    tracing::debug!(ok, code, body = %crate::adapter::scrub(&text), "qoder_queue_status");
    if !ok {
        return None;
    }
    state_of(&text)
}

/// Releases the lease of a task whose turn ended. Its failure only means the server's own
/// expiry releases it.
pub(crate) async fn finish(http: &reqwest::Client, signer: &Signer, place: &Place) {
    let url = format!("{}{FINISH_PATH}", signer.base);
    let payload = json!({
        "model_key": place.model,
        "request_set_id": place.task,
        "user_id": signer.uid,
        "time_consumed": place.began.elapsed().as_millis() as u64,
    });
    let plain = json!({"payload": payload.to_string(), "encodeVersion": "1"}).to_string();
    let wire = super::codec::encode(&plain);
    let Ok(mut headers) = cosy::signed(
        &url,
        &signer.uid,
        &signer.token,
        &signer.machine,
        &wire,
        cosy::Kind::Request,
    ) else {
        return;
    };
    headers.insert(
        reqwest::header::CONTENT_TYPE,
        reqwest::header::HeaderValue::from_static("application/json"),
    );
    if let Ok(id) = reqwest::header::HeaderValue::from_str(&uuid::Uuid::new_v4().to_string()) {
        headers.insert("X-Request-ID", id);
    }
    match http
        .post(&url)
        .headers(headers)
        .body(wire)
        .timeout(Duration::from_secs(10))
        .send()
        .await
    {
        Ok(response) => {
            let ok = response.status().is_success();
            let code = response.status().as_u16();
            let text = response.text().await.unwrap_or_default();
            tracing::debug!(ok, code, body = %crate::adapter::scrub(&text), "qoder_queue_finish");
        }
        Err(err) => tracing::debug!(%err, "qoder_queue_finish_failed"),
    }
}
