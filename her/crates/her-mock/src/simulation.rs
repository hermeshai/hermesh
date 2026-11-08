//! Versioned, evidence-driven simulation assets shared by the model and MCP tool service.
//! The model advances only after validating call IDs, arguments and observations. The tool
//! service independently enforces per-run ordering, computes numeric operations and keeps
//! virtual repository/artifact state. No network research or real filesystem tools are used.

use crate::{Part, Plan, Step, content_text};
use parking_lot::Mutex;
use serde::Deserialize;
use serde_json::{Value, json};
use std::collections::{BTreeSet, HashMap};
use std::sync::OnceLock;
use std::time::Duration;

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Scenario {
    pub version: u32,
    pub id: String,
    pub title: String,
    pub synthetic: bool,
    pub turns: usize,
    pub steps: Vec<Stage>,
    pub summary: Value,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Stage {
    pub id: String,
    pub observation: String,
    pub calls: Vec<FixtureCall>,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct FixtureCall {
    pub tool: String,
    pub input: Value,
    pub output: Value,
    pub is_error: bool,
}

pub fn scenarios() -> &'static [Scenario] {
    static CASES: OnceLock<Vec<Scenario>> = OnceLock::new();
    CASES.get_or_init(|| {
        [
            include_str!("../scenarios/macro.json"),
            include_str!("../scenarios/company.json"),
            include_str!("../scenarios/financial.json"),
            include_str!("../scenarios/coding.json"),
            include_str!("../scenarios/long-horizon.json"),
        ]
        .into_iter()
        .map(|s| {
            let case: Scenario = serde_json::from_str(s).expect("valid simulation asset");
            assert!(
                case.version == 1 && case.synthetic && case.turns > 0 && !case.steps.is_empty()
            );
            assert!(case.steps.iter().all(|s| !s.calls.is_empty()));
            case
        })
        .collect()
    })
}

pub fn scenario(id: &str) -> Result<&'static Scenario, String> {
    scenarios()
        .iter()
        .find(|s| s.id == id)
        .ok_or_else(|| format!("unknown simulation scenario: {id}"))
}

/// Fingerprint included in benchmark reports, so changing an asset changes the workload ID.
pub fn asset_digest() -> String {
    digest(
        &[
            include_str!("../scenarios/macro.json"),
            include_str!("../scenarios/company.json"),
            include_str!("../scenarios/financial.json"),
            include_str!("../scenarios/coding.json"),
            include_str!("../scenarios/long-horizon.json"),
        ]
        .join("\n"),
    )
}

fn digest(text: &str) -> String {
    sha256(text.as_bytes())
}

pub fn sha256(bytes: &[u8]) -> String {
    ring::digest::digest(&ring::digest::SHA256, bytes)
        .as_ref()
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect()
}

/// `$ref: "stage/call/json/pointer"` binds a later call to a verified tool observation.
fn resolve(value: &Value, turn: usize, outputs: &Value) -> Result<Value, String> {
    match value {
        Value::Object(map) if map.len() == 1 && map.contains_key("$ref") => {
            let path = map["$ref"].as_str().ok_or("invalid reference")?;
            outputs
                .pointer(&format!("/{path}"))
                .cloned()
                .ok_or_else(|| format!("unresolved reference: {path}"))
        }
        Value::Object(map) => map
            .iter()
            .map(|(k, v)| Ok((k.clone(), resolve(v, turn, outputs)?)))
            .collect(),
        Value::Array(items) => items.iter().map(|v| resolve(v, turn, outputs)).collect(),
        Value::String(s) => Ok(match s.as_str() {
            "$turn" => json!(turn),
            "$delta" => json!(turn + 1),
            "$opening" => json!(turn * (turn + 1) / 2),
            "$closing" => json!((turn + 1) * (turn + 2) / 2),
            _ => value.clone(),
        }),
        _ => Ok(value.clone()),
    }
}

impl Scenario {
    fn outputs(&self, turn: usize) -> Value {
        Value::Array(
            self.steps
                .iter()
                .map(|s| {
                    Value::Array(
                        s.calls
                            .iter()
                            .map(|c| {
                                resolve(&c.output, turn, &Value::Null)
                                    .expect("fixture output contains only constants")
                            })
                            .collect(),
                    )
                })
                .collect(),
        )
    }

    pub fn prompt(&self, run: &str, turn: usize) -> String {
        format!(
            "[[mock:sim:{}:{run}:{turn}]] {}；继续第 {} 轮，核实工具证据后完成。",
            self.id,
            self.title,
            turn + 1
        )
    }

    pub fn expected_reply(&self, run: &str, turn: usize) -> String {
        format!(
            "SIMULATION_OK {}",
            json!({"version": self.version, "scenario": self.id, "run": run,
            "turn": turn, "steps": self.steps.len(), "summary": resolve(&self.summary, turn, &self.outputs(turn)).unwrap()})
        )
    }

    pub fn calls_per_turn(&self) -> usize {
        self.steps.iter().map(|s| s.calls.len()).sum()
    }

    fn arguments(
        &self,
        run: &str,
        turn: usize,
        step: usize,
        call: usize,
        observed: &Value,
    ) -> Result<Value, String> {
        Ok(
            json!({"scenario": self.id, "run": run, "turn": turn, "step": step, "call": call,
            "input": resolve(&self.steps[step].calls[call].input, turn, observed)?}),
        )
    }

    fn receipt(&self, args: &Value, data: Value) -> Value {
        let step = args["step"].as_u64().unwrap() as usize;
        let call = args["call"].as_u64().unwrap() as usize;
        let mut receipt = json!({"simulation": self.version, "scenario": self.id, "run": args["run"],
            "turn": args["turn"], "step": step, "call": call, "is_error": self.steps[step].calls[call].is_error,
            "data": data});
        receipt["receipt"] = json!(digest(
            &json!({"arguments": args, "observation": receipt}).to_string()
        ));
        receipt
    }
}

const CHECKPOINT: &str = "SIM_CHECKPOINT ";

pub(crate) fn plan(messages: &[Value], argument: &str) -> Plan {
    match model_step(messages, argument) {
        Ok(step) => Plan::Step(step),
        Err(error) => Plan::Error(400, format!("simulation contract violation: {error}")),
    }
}

fn model_step(messages: &[Value], argument: &str) -> Result<Step, String> {
    let bits: Vec<&str> = argument.split(':').collect();
    if bits.len() != 3 {
        return Err("expected scenario:run:turn".into());
    }
    let (case, run) = (scenario(bits[0])?, bits[1]);
    let turn: usize = bits[2].parse().map_err(|_| "invalid turn")?;
    validate_run(run)?;
    if turn >= case.turns {
        return Err("turn beyond scenario".into());
    }
    let directive = format!("[[mock:sim:{argument}]]");
    let start = messages
        .iter()
        .rposition(|m| m["role"] == "user" && content_text(m).contains(&directive))
        .ok_or("missing scenario prompt")?;
    let mut observed = Vec::<Value>::new();
    let initial = content_text(&messages[start]);
    if let Some(line) = initial.lines().find_map(|l| l.strip_prefix(CHECKPOINT)) {
        let checkpoint: Value = serde_json::from_str(line).map_err(|_| "invalid checkpoint")?;
        if checkpoint["argument"] != argument {
            return Err("checkpoint belongs to another run".into());
        }
        observed = serde_json::from_value(checkpoint["outputs"].clone())
            .map_err(|_| "invalid checkpoint outputs")?;
        if observed.len() > case.steps.len() {
            return Err("checkpoint beyond end".into());
        }
        let expected = case.outputs(turn);
        for (i, value) in observed.iter().enumerate() {
            if *value != expected[i] {
                return Err("checkpoint evidence mismatch".into());
            }
        }
    }
    let handoff = messages
        .last()
        .is_some_and(|m| content_text(m).contains("<handoff_request>"));
    let tail = &messages[start + 1..];
    let mut cursor = 0;
    while cursor < tail.len() {
        let msg = &tail[cursor];
        if msg["role"] == "user" && content_text(msg).contains("<handoff_request>") {
            break;
        }
        if msg["role"] != "assistant" {
            return Err("orphan tool result or unexpected message".into());
        }
        // A threshold can be reached immediately after a finished task. A handoff then
        // includes its final answer as well as the tool observations.
        if observed.len() == case.steps.len()
            && content_text(msg) == case.expected_reply(run, turn)
            && msg
                .get("tool_calls")
                .is_none_or(|c| c.as_array().is_some_and(Vec::is_empty))
        {
            cursor += 1;
            continue;
        }
        let calls = msg["tool_calls"]
            .as_array()
            .ok_or("assistant has no tool calls")?;
        let step = observed.len();
        let stage = case
            .steps
            .get(step)
            .ok_or("extra model/tool step after completion")?;
        if calls.len() != stage.calls.len() {
            return Err(format!("{}: wrong call count", stage.id));
        }
        let mut ids = BTreeSet::new();
        let mut results = vec![Value::Null; calls.len()];
        for (i, call) in calls.iter().enumerate() {
            let id = call["id"].as_str().ok_or("missing call ID")?;
            if !ids.insert(id) {
                return Err("duplicate call ID".into());
            }
            let name = format!("mcp__sim__{}", stage.calls[i].tool);
            if call["function"]["name"] != name {
                return Err(format!("unexpected tool; expected {name}"));
            }
            let args: Value = serde_json::from_str(
                call["function"]["arguments"]
                    .as_str()
                    .ok_or("missing arguments")?,
            )
            .map_err(|_| "invalid arguments JSON")?;
            let expected = case.arguments(run, turn, step, i, &json!(observed))?;
            if args != expected {
                return Err(format!(
                    "{}: tool arguments do not follow evidence",
                    stage.id
                ));
            }
            let batch = tail
                .get(cursor + 1..cursor + 1 + calls.len())
                .ok_or("missing tool result")?;
            let matching: Vec<_> = batch
                .iter()
                .filter(|r| r["role"] == "tool" && r["tool_call_id"] == id)
                .collect();
            if matching.len() != 1 {
                return Err(format!("missing or duplicate result for {id}"));
            }
            let text = content_text(matching[0]);
            // The production compactor appends its instruction to the last tool/user
            // message, preserving the provider cache prefix. Validate the evidence prefix.
            let evidence = if handoff && std::ptr::eq(matching[0], messages.last().unwrap()) {
                text.split_once("<handoff_request>")
                    .map_or(text.as_str(), |(before, _)| before)
                    .trim_end()
            } else {
                text.as_str()
            };
            let result: Value =
                serde_json::from_str(evidence).map_err(|_| format!("tool returned non-JSON evidence: {}", evidence.chars().take(256).collect::<String>()))?;
            let output = resolve(&stage.calls[i].output, turn, &Value::Null)?;
            if result != case.receipt(&args, output.clone()) {
                return Err(format!(
                    "{}: observation or receipt mismatch for {id}",
                    stage.id
                ));
            }
            results[i] = output;
        }
        observed.push(json!(results));
        cursor += calls.len() + 1;
    }
    if handoff {
        return Ok(Step::text(format!(
            "{directive}\n{CHECKPOINT}{}\nContinue only after verifying subsequent tool evidence.",
            json!({"argument": argument, "outputs": observed})
        )));
    }
    let step = observed.len();
    if step == case.steps.len() {
        return Ok(Step::text(case.expected_reply(run, turn)));
    }
    let stage = &case.steps[step];
    let mut parts = vec![Part::Thinking(format!(
        "[simulated plan: {}] {}",
        stage.id, stage.observation
    ))];
    for (i, call) in stage.calls.iter().enumerate() {
        parts.push(Part::Call {
            name: format!("mcp__sim__{}", call.tool),
            args: case.arguments(run, turn, step, i, &json!(observed))?,
        });
    }
    Ok(Step {
        parts,
        finish: "tool_calls",
        delay: Duration::ZERO,
    })
}

fn validate_run(run: &str) -> Result<(), String> {
    if run.is_empty()
        || run.len() > 128
        || !run
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b"-_.".contains(&b))
    {
        Err("run must contain 1..128 ASCII letters, digits, hyphens, dots or underscores".into())
    } else {
        Ok(())
    }
}

pub fn tool_definitions() -> Vec<Value> {
    let names: BTreeSet<&str> = scenarios()
        .iter()
        .flat_map(|s| &s.steps)
        .flat_map(|s| &s.calls)
        .map(|c| c.tool.as_str())
        .collect();
    names.into_iter().map(|name| json!({"name": name, "description": format!("Deterministic simulation {name}; validates evidence and per-run ordering."),
        "annotations": {"readOnlyHint": !matches!(name, "repo_patch" | "artifact_write" | "ledger_commit")},
        "inputSchema": {"type": "object", "properties": {"scenario": {"type": "string"}, "run": {"type": "string"},
            "turn": {"type": "integer", "minimum": 0}, "step": {"type": "integer", "minimum": 0}, "call": {"type": "integer", "minimum": 0},
            "input": {"type": "object"}}, "required": ["scenario", "run", "turn", "step", "call", "input"], "additionalProperties": false}
    })).collect()
}

#[derive(Default)]
struct Run {
    turn: usize,
    step: usize,
    /// Calls in the current parallel batch; retries return the same receipt without advancing.
    done: BTreeSet<usize>,
    calls: usize,
    code: Option<String>,
    artifacts: HashMap<String, Value>,
    ledger_balance: i64,
}

#[derive(Default)]
pub struct ToolService {
    runs: Mutex<HashMap<(String, String), Run>>,
    violations: std::sync::atomic::AtomicU64,
    corrupt_next: std::sync::atomic::AtomicBool,
}

impl ToolService {
    /// Fault injection: corrupt one actual tool observation, leaving its audit intact.
    pub fn corrupt_next_result(&self) {
        self.corrupt_next
            .store(true, std::sync::atomic::Ordering::Relaxed);
    }

    pub fn call(&self, name: &str, args: &Value) -> Value {
        match self.execute(name, args) {
            Ok(mut value) => {
                if self
                    .corrupt_next
                    .swap(false, std::sync::atomic::Ordering::Relaxed)
                {
                    value["content"][0]["text"] =
                        json!("{\"simulation\":0,\"fault\":\"corrupted evidence\"}");
                }
                value
            }
            Err(error) => {
                self.violations
                    .fetch_add(1, std::sync::atomic::Ordering::Relaxed);
                json!({"content": [{"type": "text", "text": format!("simulation contract violation: {error}")}], "isError": true})
            }
        }
    }

    pub fn violations(&self) -> u64 {
        self.violations.load(std::sync::atomic::Ordering::Relaxed)
    }

    /// Release state explicitly after a run, and return its independently counted audit.
    pub fn finish(&self, scenario: &str, run: &str) -> Value {
        self.runs.lock().remove(&(scenario.into(), run.into())).map_or(Value::Null, |r|
            json!({"completed_turns": r.turn, "pending_step": r.step, "pending_calls": r.done.len(),
                "tool_calls": r.calls, "artifacts": r.artifacts, "code": r.code, "ledger_balance": r.ledger_balance}))
    }

    fn execute(&self, name: &str, args: &Value) -> Result<Value, String> {
        let case = scenario(args["scenario"].as_str().ok_or("missing scenario")?)?;
        let run = args["run"].as_str().ok_or("missing run")?;
        validate_run(run)?;
        let index = |key: &str| {
            args[key]
                .as_u64()
                .and_then(|v| usize::try_from(v).ok())
                .ok_or_else(|| format!("invalid {key}"))
        };
        let (turn, step, call) = (index("turn")?, index("step")?, index("call")?);
        if turn >= case.turns {
            return Err("turn beyond scenario".into());
        }
        let stage = case.steps.get(step).ok_or("step beyond scenario")?;
        let fixture = stage.calls.get(call).ok_or("call beyond stage")?;
        if fixture.tool != name
            || *args != case.arguments(run, turn, step, call, &case.outputs(turn))?
        {
            return Err(format!("{}: unexpected tool or input", stage.id));
        }
        let mut runs = self.runs.lock();
        if runs.len() >= 100_000 && !runs.contains_key(&(case.id.clone(), run.into())) {
            return Err("run limit reached; finish existing runs".into());
        }
        let state = runs.entry((case.id.clone(), run.into())).or_default();
        let position = turn * case.steps.len() + step;
        let next = state.turn * case.steps.len() + state.step;
        // Only the immediately preceding batch can be replayed, for transport retry.
        if position > next || position.saturating_add(1) < next {
            return Err("out-of-order tool call".into());
        }
        let expected = resolve(&fixture.output, turn, &Value::Null)?;
        let replay = position < next || state.done.contains(&call);
        let data = if replay {
            expected.clone()
        } else {
            compute(name, &args["input"], expected.clone(), state)?
        };
        if !equivalent(&data, &expected) {
            return Err(format!(
                "fixture oracle disagrees with {name}: {data} != {expected}"
            ));
        }
        let receipt = case.receipt(args, expected);
        if !replay {
            state.calls += 1;
            state.done.insert(call);
            if state.done.len() == stage.calls.len() {
                state.done.clear();
                state.step += 1;
                if state.step == case.steps.len() {
                    state.turn += 1;
                    state.step = 0;
                }
            }
        }
        Ok(
            json!({"content": [{"type": "text", "text": receipt.to_string()}], "structuredContent": receipt, "isError": fixture.is_error}),
        )
    }
}

fn equivalent(a: &Value, b: &Value) -> bool {
    match (a, b) {
        (Value::Number(a), Value::Number(b)) => {
            (a.as_f64().unwrap() - b.as_f64().unwrap()).abs() < 1e-9
        }
        (Value::Object(a), Value::Object(b)) => {
            a.len() == b.len()
                && a.iter()
                    .all(|(k, v)| b.get(k).is_some_and(|b| equivalent(v, b)))
        }
        (Value::Array(a), Value::Array(b)) => {
            a.len() == b.len() && a.iter().zip(b).all(|(a, b)| equivalent(a, b))
        }
        _ => a == b,
    }
}

fn compute(name: &str, input: &Value, mut out: Value, state: &mut Run) -> Result<Value, String> {
    let n = |k: &str| {
        input[k]
            .as_f64()
            .ok_or_else(|| format!("{k} must be numeric"))
    };
    let array = |k: &str| -> Result<Vec<f64>, String> {
        input[k]
            .as_array()
            .ok_or_else(|| format!("{k} must be an array"))?
            .iter()
            .map(|v| v.as_f64().ok_or_else(|| format!("{k} contains non-number")))
            .collect()
    };
    match name {
        "finance_calculate" => match input["operation"].as_str().unwrap_or("") {
            "difference" => out["value"] = json!(n("a")? - n("b")?),
            "product" => out["value"] = json!(n("a")? * n("b")?),
            "ratio" | "growth" => {
                let growth = input["operation"] == "growth";
                let denominator = n(if growth { "prior" } else { "denominator" })?;
                if denominator == 0.0 {
                    return Err("zero denominator".into());
                }
                out["value"] = json!(
                    n(if growth { "current" } else { "numerator" })? / denominator
                        - if growth { 1.0 } else { 0.0 }
                );
            }
            "weighted" => {
                let (v, w) = (array("values")?, array("weights")?);
                if v.len() != w.len() {
                    return Err("weights length mismatch".into());
                }
                out["value"] = json!(v.iter().zip(&w).map(|(v, w)| v * w).sum::<f64>());
                out["weight_sum"] = json!(w.iter().sum::<f64>());
            }
            "balance_check" => {
                let d = n("assets")? - n("liabilities")? - n("equity")?;
                out = json!({"difference": d, "balanced": d.abs() < 1e-9});
            }
            _ => return Err("unknown calculation".into()),
        },
        "equity_valuation" => {
            let equity = n("enterprise_value")? - n("debt")? + n("cash")?;
            out = json!({"equity_value": equity, "per_share": equity / n("shares")?});
        }
        "equity_sensitivity" => {
            out["per_share"] = json!(
                array("multiples")?
                    .into_iter()
                    .map(|m| Ok((n("ebitda")? * m - n("net_debt")?) / n("shares")?))
                    .collect::<Result<Vec<f64>, String>>()?
            );
        }
        "cash_reconcile" => {
            let flow = array("flows")?.iter().sum::<f64>();
            let d = n("closing")? - n("opening")? - flow;
            out = json!({"net_flow": flow, "difference": d, "balanced": d.abs() < 1e-9});
        }
        "cash_stress" => {
            let cash = n("operating")? * (1.0 - n("decline")?);
            let fcf = cash - n("capex")?;
            out = json!({"stressed_operating": cash, "free_cashflow": fcf, "positive": fcf > 0.0});
        }
        "repo_read" if input["path"] == "src/page.py" => {
            state.code = out["content"].as_str().map(str::to_string)
        }
        "repo_patch" => {
            let code = state.code.as_mut().ok_or("source was not read")?;
            let old = input["old"].as_str().ok_or("missing old source")?;
            let new = input["new"].as_str().ok_or("missing new source")?;
            if code.matches(old).count() != 1 {
                return Err("patch precondition failed".into());
            }
            *code = code.replacen(old, new, 1);
        }
        "repo_test" => {
            let code = state.code.as_ref().ok_or("source was not read")?;
            let offset = usize::from(code.contains("size + 1"));
            let cases = [
                (vec![0, 1, 2], 1, 2, vec![0, 1]),
                (vec![0, 1, 2], 2, 2, vec![2]),
                (vec![], 1, 2, vec![]),
                (vec![0, 1, 2, 3, 4], 3, 2, vec![4]),
            ];
            let passed = cases.into_iter().all(|(items, page, size, expected)| {
                let start = (page - 1) * size + offset;
                items.into_iter().skip(start).take(size).collect::<Vec<_>>() == expected
            });
            out["passed"] = json!(passed);
            out["exit_code"] = json!(if passed { 0 } else { 1 });
        }
        "artifact_write" => {
            state.artifacts.insert(
                input["path"]
                    .as_str()
                    .ok_or("missing artifact path")?
                    .into(),
                input.clone(),
            );
        }
        "ledger_read" => out["opening"] = json!(state.ledger_balance),
        "ledger_reconcile" => out["closing"] = json!(n("opening")? + n("delta")?),
        "ledger_commit" => {
            let closing = input["closing"].as_i64().ok_or("invalid closing balance")?;
            if input["balanced"] != true || closing != state.ledger_balance + state.turn as i64 + 1
            {
                return Err("ledger does not reconcile with the previous committed balance".into());
            }
            state.ledger_balance = closing;
        }
        _ => {} // Recorded source documents and deterministic domain-service responses.
    }
    Ok(out)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn advance(
        case: &Scenario,
        run: &str,
        turn: usize,
        messages: &mut Vec<Value>,
        service: &ToolService,
    ) -> bool {
        let arg = format!("{}:{run}:{turn}", case.id);
        let step = model_step(messages, &arg).unwrap();
        let calls: Vec<_> = step
            .parts
            .into_iter()
            .filter_map(|p| match p {
                Part::Call { name, args } => Some((name, args)),
                _ => None,
            })
            .collect();
        if calls.is_empty() {
            return false;
        }
        let ids: Vec<_> = (0..calls.len())
            .map(|i| format!("id-{}-{i}", messages.len()))
            .collect();
        messages.push(json!({"role":"assistant", "tool_calls": calls.iter().zip(&ids).map(|((name,args),id)|
            json!({"id":id,"type":"function","function":{"name":name,"arguments":args.to_string()}})).collect::<Vec<_>>()}));
        // Return concurrent results in reverse order to test ID-based matching.
        for ((name, args), id) in calls.iter().zip(&ids).rev() {
            let result = service.call(name.strip_prefix("mcp__sim__").unwrap(), args);
            assert!(result["structuredContent"].is_object(), "{result}");
            messages.push(
                json!({"role":"tool","tool_call_id":id,"content":result["content"][0]["text"]}),
            );
        }
        true
    }

    #[test]
    fn all_assets_execute_with_real_calculations_and_220_dependent_turns() {
        let service = ToolService::default();
        for case in scenarios() {
            let mut messages = Vec::new();
            for turn in 0..case.turns {
                messages.push(json!({"role":"user","content":case.prompt("run",turn)}));
                let mut steps = 0;
                while advance(case, "run", turn, &mut messages, &service) {
                    steps += 1;
                }
                assert_eq!(steps, case.steps.len());
            }
            let audit = service.finish(&case.id, "run");
            assert_eq!(audit["completed_turns"], case.turns);
            assert_eq!(audit["tool_calls"], case.turns * case.calls_per_turn());
            if case.id == "coding" {
                assert!(!audit["code"].as_str().unwrap().contains("size + 1"));
            }
            if case.id == "long-horizon" {
                assert_eq!(audit["ledger_balance"], 24310);
            }
            if case.id != "long-horizon" {
                assert_eq!(audit["artifacts"].as_object().unwrap().len(), 1);
            }
        }
        assert_eq!(service.violations(), 0);
    }

    #[test]
    fn wrong_results_missing_ids_arguments_and_checkpoints_fail_closed() {
        let case = scenario("macro").unwrap();
        let service = ToolService::default();
        let mut messages = vec![json!({"role":"user","content":case.prompt("negative",0)})];
        advance(case, "negative", 0, &mut messages, &service);
        let good = messages.clone();
        messages.last_mut().unwrap()["content"] = json!("made up research");
        assert!(model_step(&messages, "macro:negative:0").is_err());
        messages = good.clone();
        messages.last_mut().unwrap()["tool_call_id"] = json!("wrong");
        assert!(model_step(&messages, "macro:negative:0").is_err());
        messages = good.clone();
        messages[1]["tool_calls"][0]["function"]["arguments"] = json!("{}");
        assert!(model_step(&messages, "macro:negative:0").is_err());
        messages = good;
        messages.pop();
        assert!(model_step(&messages, "macro:negative:0").is_err());
    }

    #[test]
    fn tool_service_checks_order_isolates_runs_and_retries_are_idempotent() {
        let case = scenario("coding").unwrap();
        let service = ToolService::default();
        let future = case.arguments("a", 0, 2, 0, &case.outputs(0)).unwrap();
        assert_eq!(service.call("repo_patch", &future)["isError"], true);
        let a = case.arguments("a", 0, 0, 0, &case.outputs(0)).unwrap();
        assert_eq!(service.call("repo_read", &a), service.call("repo_read", &a));
        let b = case.arguments("b", 0, 0, 0, &case.outputs(0)).unwrap();
        assert_ne!(service.call("repo_read", &a), service.call("repo_read", &b));
        assert_eq!(service.finish("coding", "a")["tool_calls"], 1);
        assert_eq!(service.finish("coding", "b")["tool_calls"], 1);
        assert_eq!(service.violations(), 1);
    }

    #[test]
    fn a_compaction_carries_only_verified_progress() {
        let case = scenario("financial").unwrap();
        let service = ToolService::default();
        let mut messages = vec![json!({"role":"user","content":case.prompt("compact",0)})];
        advance(case, "compact", 0, &mut messages, &service);
        // Match the real compactor: the instruction is appended to the last tool result.
        let content = messages.last().unwrap()["content"]
            .as_str()
            .unwrap()
            .to_string();
        messages.last_mut().unwrap()["content"] = json!(format!(
            "{content}\n\n<handoff_request>summarize</handoff_request>"
        ));
        let mut step = model_step(&messages, "financial:compact:0").unwrap();
        let Part::Text(summary) = step.parts.remove(0) else {
            panic!("expected summary")
        };
        messages = vec![json!({"role":"user","content":summary})];
        while advance(case, "compact", 0, &mut messages, &service) {}
        assert_eq!(
            service.finish("financial", "compact")["tool_calls"],
            case.calls_per_turn()
        );
        let mut bad = summary.replace("1500", "1501");
        assert_ne!(bad, summary);
        bad.push('\n');
        assert!(
            model_step(
                &[json!({"role":"user","content":bad})],
                "financial:compact:0"
            )
            .is_err()
        );
    }
}
