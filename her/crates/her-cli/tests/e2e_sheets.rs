//! End-to-end: a workbook over the native protocol (spreadsheet-design §6, §7), with the real
//! `her doc-worker` behind `her serve`. A person opens a DCF model, looks at the inputs, tries
//! a higher WACC, keeps it, undoes and redoes it, saves, and meets Excel having changed the file.

mod common;

use common::Demux;
use common::SchemaCheck;
use common::TestEnv;
use common::native_init;
use common::native_prompt;
use her_sheet::book::Book;
use her_sheet::book::Value;
use her_sheet::cellref::CellRef;
use serde_json::Value as Json;
use serde_json::json;
use std::path::Path;

fn fixture() -> Vec<u8> {
    std::fs::read(Path::new(env!("CARGO_MANIFEST_DIR")).join("../her-sheet/tests/fixtures/gen/dcf_settled.xlsx")).unwrap()
}

fn cell<'a>(cells: &'a Json, at: &str) -> &'a Json {
    cells["cells"].as_array().unwrap().iter().find(|c| c["cell"] == at).unwrap_or_else(|| panic!("no {at} in {cells}"))
}

fn on_disk(path: &Path, sheet: &str, at: &str) -> Value {
    let book = Book::open(&std::fs::read(path).unwrap()).unwrap();
    book.value(book.sheet_index(sheet).unwrap(), CellRef::parse(at).unwrap())
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn a_workbook_is_read_edited_and_saved_over_the_protocol() {
    let env = TestEnv::new("manual", "").await;
    let schema = SchemaCheck::new(&["DocOpenResult", "DocCellsResult", "DocApplyResult", "DocInfo"]);
    let (_child, client, mut incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    client.request("initialize", json!({"protocol_version": 2, "client_info": {"name": "e2e"}})).await.unwrap();
    let cwd = env.workspace("a");
    let session = client.request("session/new", json!({"cwd": cwd})).await.unwrap()["session_id"].as_str().unwrap().to_string();
    let file = cwd.join("模型.xlsx");
    std::fs::write(&file, fixture()).unwrap();

    let opened = client.request("doc/open", json!({"session_id": session, "path": file})).await.unwrap();
    schema.check("DocOpenResult", &opened);
    let info = &opened["info"];
    assert_eq!(info["format"], "xlsx");
    assert_eq!(info["layout"], "grid");
    let sheets: Vec<&str> = info["sheets"].as_array().unwrap().iter().map(|s| s["name"].as_str().unwrap()).collect();
    assert_eq!(sheets, ["假设", "利润表", "估值", "可比公司"]);
    assert_eq!(info["sheets"][1]["frozen"], json!([2, 1]));
    assert!(info["capabilities"].as_array().unwrap().contains(&json!("calc")));
    let doc = info["doc_id"].as_str().unwrap().to_string();

    // The inputs as the grid shows them: formats applied, the input font blue.
    let inputs = client.request("doc/cells", json!({"session_id": session, "doc_id": doc, "sheet": "假设", "range": "A1:C9"})).await.unwrap();
    schema.check("DocCellsResult", &inputs);
    assert_eq!(cell(&inputs, "A6")["text"], "WACC");
    assert_eq!(cell(&inputs, "B6")["text"], "8.5%");
    assert_eq!(cell(&inputs, "B6")["value"], json!({"type": "number", "value": 0.085}));
    assert_eq!(cell(&inputs, "B6")["look"]["color"], "0000FF");
    let valuation = client.request("doc/cells", json!({"session_id": session, "doc_id": doc, "sheet": "估值", "range": "A15:D15"})).await.unwrap();
    assert_eq!(cell(&valuation, "D15")["text"], "6,121.33");
    assert_eq!(cell(&valuation, "D15")["formula"], "=SUM(D3:D12)+D14");

    // Recalculation is a comparison: no working-copy write, revision, dirty flag or history.
    for circular_only in [false, true] {
        let result = client.request("doc/recalc", json!({"session_id": session, "doc_id": doc, "circular_only": circular_only})).await.unwrap();
        schema.check("DocApplyResult", &result);
        assert_eq!(result["revision"], 0);
        assert_eq!(result["dirty"], false);
        assert_eq!(std::fs::read(&file).unwrap(), fixture());
    }

    // Trying 9% changes nothing yet, and says what would move.
    let wacc = json!([{"op": "set_value", "sheet": "假设", "cell": "B6", "value": 0.09}]);
    let tried = client.request("doc/apply", json!({"session_id": session, "doc_id": doc, "ops": wacc, "preview": true})).await.unwrap();
    schema.check("DocApplyResult", &tried);
    assert_eq!(tried["revision"], 0);
    assert_eq!(tried["dirty"], false);
    let outputs = tried["impact"]["key_outputs"].as_array().unwrap();
    let ev = outputs.iter().find(|o| o["sheet"] == "估值" && o["cell"] == "D15").unwrap_or_else(|| panic!("{outputs:?}"));
    assert_eq!(ev["label"], "企业价值");
    assert_eq!(ev["before_text"], "6,121.33");
    assert!(ev["after"]["value"].as_f64().unwrap() < 6121.33);
    assert_eq!(tried["impact"]["edited"][0]["before_text"], "8.5%");
    assert_eq!(tried["impact"]["edited"][0]["after_text"], "9.0%");
    let unchanged = client.request("doc/cells", json!({"session_id": session, "doc_id": doc, "sheet": "假设", "range": "B6"})).await.unwrap();
    assert_eq!(cell(&unchanged, "B6")["text"], "8.5%");

    // Kept: a revision, the grid shows the new values, the file is untouched until saved.
    let kept = client.request("doc/apply", json!({"session_id": session, "doc_id": doc, "ops": wacc, "base_revision": 0})).await.unwrap();
    assert_eq!(kept["revision"], 1);
    assert_eq!(kept["dirty"], true);
    // Every connection that opened the document hears of it, whichever session edited.
    loop {
        if let her_rpc::Incoming::Notification { method, params, .. } = common::recv(&mut incoming).await
            && method == "doc/changed"
        {
            assert_eq!(params["info"]["doc_id"], doc.as_str());
            assert_eq!(params["info"]["revision"], 1);
            assert_eq!(params["info"]["dirty"], true);
            break;
        }
    }
    assert_eq!(kept["impact"], tried["impact"]);
    let new_ev = ev["after_text"].clone();
    let valuation = client.request("doc/cells", json!({"session_id": session, "doc_id": doc, "sheet": "估值", "range": "D15"})).await.unwrap();
    assert_eq!(cell(&valuation, "D15")["text"], new_ev);
    assert_eq!(on_disk(&file, "假设", "B6"), Value::Number(0.085));

    // An edit made against an older revision, and one that would break a merged title, are refused.
    let stale = client.request("doc/apply", json!({"session_id": session, "doc_id": doc, "ops": wacc, "base_revision": 0})).await.unwrap_err();
    assert_eq!(stale.code, -32035);
    let merged = json!([{"op": "set_value", "sheet": "利润表", "cell": "C1", "value": "x"}]);
    let refused = client.request("doc/apply", json!({"session_id": session, "doc_id": doc, "ops": merged})).await.unwrap_err();
    assert_eq!(refused.code, -32034);
    assert!(refused.message.contains("merged"), "{}", refused.message);

    // Undo and redo, by revision.
    let undone = client.request("doc/undo", json!({"session_id": session, "doc_id": doc})).await.unwrap();
    assert_eq!(undone["dirty"], false);
    let back = client.request("doc/cells", json!({"session_id": session, "doc_id": doc, "sheet": "估值", "range": "D15"})).await.unwrap();
    assert_eq!(cell(&back, "D15")["text"], "6,121.33");
    client.request("doc/redo", json!({"session_id": session, "doc_id": doc})).await.unwrap();

    // Saved: the file now holds 9% and the new valuation, with everything else as it was.
    let saved = client.request("doc/save", json!({"session_id": session, "doc_id": doc})).await.unwrap();
    schema.check("DocInfo", &saved);
    assert_eq!(saved["dirty"], false);
    assert_eq!(on_disk(&file, "假设", "B6"), Value::Number(0.09));
    let Value::Number(saved_ev) = on_disk(&file, "估值", "D15") else { panic!() };
    assert!(saved_ev < 6121.33);

    // Excel changes the file meanwhile: an unsaved edit survives, saving says so, force writes over.
    std::fs::write(&file, fixture()).unwrap();
    let growth = json!([{"op": "set_value", "sheet": "假设", "cell": "B2", "value": 0.1}]);
    client.request("doc/apply", json!({"session_id": session, "doc_id": doc, "ops": growth})).await.unwrap();
    let conflict = client.request("doc/save", json!({"session_id": session, "doc_id": doc})).await.unwrap_err();
    assert_eq!(conflict.code, -32036);
    assert_eq!(on_disk(&file, "假设", "B6"), Value::Number(0.085));
    client.request("doc/save", json!({"session_id": session, "doc_id": doc, "force": true})).await.unwrap();
    assert_eq!(on_disk(&file, "假设", "B2"), Value::Number(0.1));
    assert_eq!(on_disk(&file, "假设", "B6"), Value::Number(0.09));

    // Save as leaves the original alone.
    let copy = cwd.join("模型-副本.xlsx");
    client.request("doc/save", json!({"session_id": session, "doc_id": doc, "path": copy})).await.unwrap();
    assert_eq!(on_disk(&copy, "假设", "B2"), Value::Number(0.1));
}

/// The agent's side (spreadsheet-design §9): the scripted model opens the model, reads the
/// inputs, tries 9%, makes the change, traces what reads WACC and saves. In manual mode the
/// edit is asked about; approving it for the session moves the session to accept-edits, so the
/// save inside the workspace is not asked again. The tool results say what a model needs to know.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn an_agent_reads_tries_edits_and_saves_a_workbook() {
    let env = TestEnv::new("manual", "").await;
    let (_child, client, incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    native_init(&client).await;
    let demux = Demux::spawn(incoming);
    let cwd = env.workspace("agent");
    let file = cwd.join("模型.xlsx");
    std::fs::write(&file, fixture()).unwrap();
    let session = client.request("session/new", json!({"cwd": cwd})).await.unwrap()["session_id"].as_str().unwrap().to_string();
    let mut events = demux.subscribe(&session);

    let turn = native_prompt(&client, &mut events, &session, "[[mock:sheet:模型.xlsx]] 把 WACC 调到 9% 看看估值", true).await;
    assert_eq!(turn.end["type"], "task_completed", "{:#?}", turn.events);
    assert_eq!(turn.approvals, 1, "doc_edit is asked about; after approve_for_session, doc_save is not");
    let results: Vec<String> = turn
        .events
        .iter()
        .filter(|e| e["type"] == "tool_completed")
        .map(|e| e["result"]["content"].as_array().unwrap().iter().filter_map(|c| c["text"].as_str()).collect::<String>())
        .collect();
    assert_eq!(results.len(), 6, "{results:#?}");
    let [opened, read, tried, edited, traced, saved] = &results[..] else { unreachable!() };
    assert!(opened.contains("a workbook: 假设, 利润表, 估值, 可比公司"), "{opened}");
    assert!(opened.contains("估值!D15 企业价值 = 6,121.33"), "{opened}");
    assert!(opened.contains("WSS("), "the Wind call is said to stand at its saved value: {opened}");
    assert!(read.contains("| 6 | WACC | 8.5% |"), "{read}");
    assert!(tried.contains("Tried, nothing was changed") && tried.contains("估值!D15 企业价值: 6,121.33 →"), "{tried}");
    assert!(edited.contains("Revision 1") && edited.contains("Note: WACC 上调到 9%") && edited.contains("Changed 假设!B6: 8.5% → 9.0%"), "{edited}");
    assert!(traced.contains("假设!B6 is read by:") && traced.contains("估值!C3"), "{traced}");
    assert!(saved.starts_with("Saved"), "{saved}");
    assert_eq!(on_disk(&file, "假设", "B6"), Value::Number(0.09));
}

/// A CSV saved by Excel on a Chinese system (GBK, CRLF) and a legacy .xls (spreadsheet-design §10).
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn a_csv_is_edited_in_its_own_encoding_and_an_xls_stays_read_only() {
    let env = TestEnv::new("manual", "").await;
    let (_child, client, _incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    client.request("initialize", json!({"protocol_version": 2, "client_info": {"name": "e2e"}})).await.unwrap();
    let cwd = env.workspace("csv");
    let session = client.request("session/new", json!({"cwd": cwd})).await.unwrap()["session_id"].as_str().unwrap().to_string();

    let file = cwd.join("行情.csv");
    let (gbk, _, _) = encoding_rs::GB18030.encode("代码,名称,收盘\r\n000001,平安银行,12.50\r\n600519,\"贵州茅台, 白酒\",1688\r\n");
    std::fs::write(&file, &gbk).unwrap();
    let opened = client.request("doc/open", json!({"session_id": session, "path": file})).await.unwrap();
    assert_eq!(opened["info"]["format"], "csv");
    assert!(!opened["info"]["capabilities"].as_array().unwrap().contains(&json!("edit_formulas")));
    let doc = opened["info"]["doc_id"].as_str().unwrap().to_string();
    let sheet = opened["info"]["sheets"][0]["name"].as_str().unwrap().to_string();
    let cells = client.request("doc/cells", json!({"session_id": session, "doc_id": doc, "sheet": sheet, "range": "A1:C3"})).await.unwrap();
    assert_eq!(cell(&cells, "A2")["text"], "000001");
    assert_eq!(cell(&cells, "B3")["text"], "贵州茅台, 白酒");
    assert_eq!(cell(&cells, "C2")["text"], "12.50");

    let formula = json!([{"op": "set_formula", "sheet": sheet, "cell": "D2", "formula": "=C2*2"}]);
    let refused = client.request("doc/apply", json!({"session_id": session, "doc_id": doc, "ops": formula})).await.unwrap_err();
    assert_eq!(refused.code, -32034);
    assert!(client.request("doc/apply", json!({"session_id": session, "doc_id": doc, "ops": [{"op": "set_value", "sheet": "missing", "cell": "A1", "value": "wrong"}]})).await.is_err());
    for (method, params) in [
        ("doc/recalc", json!({"session_id": session, "doc_id": doc})),
        ("doc/save", json!({"session_id": session, "doc_id": doc, "path": cwd.join("wrong.xlsx")})),
    ] {
        assert_eq!(client.request(method, params).await.unwrap_err().code, -32034);
    }
    assert!(!cwd.join("wrong.xlsx").exists());
    let edit = json!([{"op": "set_value", "sheet": sheet, "cell": "C2", "value": 12.8}, {"op": "set_value", "sheet": sheet, "cell": "A4", "value": "000858"}]);
    client.request("doc/apply", json!({"session_id": session, "doc_id": doc, "ops": edit})).await.unwrap();
    client.request("doc/save", json!({"session_id": session, "doc_id": doc})).await.unwrap();
    let saved_bytes = std::fs::read(&file).unwrap();
    let (text, _, _) = encoding_rs::GB18030.decode(&saved_bytes);
    assert_eq!(text, "代码,名称,收盘\r\n000001,平安银行,12.8\r\n600519,\"贵州茅台, 白酒\",1688\r\n000858\r\n");

    let xls = cwd.join("旧模型.xls");
    std::fs::copy(Path::new(env!("CARGO_MANIFEST_DIR")).join("../her-sheet/tests/fixtures/legacy.xls"), &xls).unwrap();
    let opened = client.request("doc/open", json!({"session_id": session, "path": xls})).await.unwrap();
    assert_eq!(opened["info"]["format"], "xls");
    assert_eq!(opened["info"]["save_in_place"], false);
    let doc = opened["info"]["doc_id"].as_str().unwrap().to_string();
    let sheet = opened["info"]["sheets"][0]["name"].as_str().unwrap().to_string();
    let cells = client.request("doc/cells", json!({"session_id": session, "doc_id": doc, "sheet": sheet, "range": "A1:Z50"})).await.unwrap();
    assert!(!cells["cells"].as_array().unwrap().is_empty());
    let edit = json!([{"op": "set_value", "sheet": sheet, "cell": "A1", "value": 1}]);
    let refused = client.request("doc/apply", json!({"session_id": session, "doc_id": doc, "ops": edit})).await.unwrap_err();
    assert_eq!(refused.code, -32034);
    assert!(refused.message.contains(".xlsx"), "{}", refused.message);
    let before = std::fs::read(&xls).unwrap();
    assert_eq!(client.request("doc/save", json!({"session_id": session, "doc_id": doc})).await.unwrap_err().code, -32034);
    assert_eq!(std::fs::read(&xls).unwrap(), before);

    let tsv = cwd.join("table.tsv");
    std::fs::write(&tsv, "名称\t金额\r\n项目\t100\r\n").unwrap();
    let opened = client.request("doc/open", json!({"session_id": session, "path": tsv})).await.unwrap();
    let id = opened["info"]["doc_id"].as_str().unwrap();
    let name = opened["info"]["sheets"][0]["name"].as_str().unwrap();
    let cells = client.request("doc/cells", json!({"session_id": session, "doc_id": id, "sheet": name, "range": "A1:B2"})).await.unwrap();
    assert_eq!(cell(&cells, "B2")["text"], "100");
    client.request("doc/apply", json!({"session_id": session, "doc_id": id, "ops": [{"op": "set_value", "sheet": name, "cell": "B2", "value": 120}]})).await.unwrap();
    client.request("doc/save", json!({"session_id": session, "doc_id": id})).await.unwrap();
    assert_eq!(std::fs::read_to_string(&tsv).unwrap(), "名称\t金额\r\n项目\t120\r\n");
}

/// The grid's tools (spreadsheet-design §8.7): trace a cell's precedents and dependents, find
/// cells, and fill down or right as Excel's ⌘D and ⌘R do, as one undoable edit.
#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn the_grid_traces_finds_and_fills() {
    let env = TestEnv::new("manual", "").await;
    let schema = SchemaCheck::new(&["DocFoundResult", "DocApplyResult"]);
    let (_child, client, _incoming) = env.spawn_rpc(&["serve", "--stdio"]);
    client.request("initialize", json!({"protocol_version": 2, "client_info": {"name": "e2e"}})).await.unwrap();
    let cwd = env.workspace("t");
    let session = client.request("session/new", json!({"cwd": cwd})).await.unwrap()["session_id"].as_str().unwrap().to_string();
    let file = cwd.join("模型.xlsx");
    std::fs::write(&file, fixture()).unwrap();
    let opened = client.request("doc/open", json!({"session_id": session, "path": file})).await.unwrap();
    let doc = opened["info"]["doc_id"].as_str().unwrap().to_string();
    let found = |r: &Json| -> Vec<String> { r["cells"].as_array().unwrap().iter().map(|c| format!("{}!{}", c["sheet"].as_str().unwrap(), c["cell"].as_str().unwrap())).collect() };

    // Precedents, one step: what the enterprise value adds up, each reached from D15.
    let up = client.request("doc/trace", json!({"session_id": session, "doc_id": doc, "sheet": "估值", "cell": "D15", "direction": "up"})).await.unwrap();
    schema.check("DocFoundResult", &up);
    let cells = found(&up);
    assert!(cells.contains(&"估值!D3".to_string()) && cells.contains(&"估值!D14".to_string()), "{cells:?}");
    assert!(up["cells"].as_array().unwrap().iter().all(|c| c["depth"] == 1 && c["via"] == "估值!D15"), "{up}");
    // Two steps out: the second ring says which cell it came through.
    let deeper = client.request("doc/trace", json!({"session_id": session, "doc_id": doc, "sheet": "估值", "cell": "D15", "direction": "up", "depth": 2})).await.unwrap();
    assert!(deeper["cells"].as_array().unwrap().iter().any(|c| c["depth"] == 2 && c["via"].as_str().is_some_and(|v| v != "估值!D15")), "{deeper}");
    // Dependents: WACC is read somewhere.
    let down = client.request("doc/trace", json!({"session_id": session, "doc_id": doc, "sheet": "假设", "cell": "B6", "direction": "down"})).await.unwrap();
    assert!(!found(&down).is_empty());

    // Find: by shown text.
    let hits = client.request("doc/find", json!({"session_id": session, "doc_id": doc, "query": "wacc"})).await.unwrap();
    schema.check("DocFoundResult", &hits);
    assert!(found(&hits).contains(&"假设!A6".to_string()), "{hits}");
    let one = client.request("doc/find", json!({"session_id": session, "doc_id": doc, "query": "0", "limit": 1})).await.unwrap();
    assert_eq!(one["cells"].as_array().unwrap().len(), 1);
    assert_eq!(one["more"], true);

    // Fill right: the formula moves as Excel moves it; one revision, undone as one.
    let before = client.request("doc/cells", json!({"session_id": session, "doc_id": doc, "sheet": "利润表", "range": "B4:D4"})).await.unwrap();
    let b4 = cell(&before, "B4")["formula"].as_str().unwrap().to_string();
    assert!(b4.contains("B3"), "{b4}");
    client.request("doc/apply", json!({"session_id": session, "doc_id": doc, "ops": [{"op": "set_value", "sheet": "假设", "cell": "K1", "value": 5}]})).await.unwrap();
    let filled = client.request("doc/fill", json!({"session_id": session, "doc_id": doc, "sheet": "利润表", "range": "B4:D4", "direction": "right", "base_revision": 1})).await.unwrap();
    schema.check("DocApplyResult", &filled);
    assert_eq!(filled["revision"], 2);
    let after = client.request("doc/cells", json!({"session_id": session, "doc_id": doc, "sheet": "利润表", "range": "B4:D4"})).await.unwrap();
    assert_eq!(cell(&after, "C4")["formula"], b4.replace("B3", "C3"));
    assert_eq!(cell(&after, "D4")["formula"], b4.replace("B3", "D3"));
    // Fill down: a value copies; the empty cell under an empty source stays empty.
    client.request("doc/fill", json!({"session_id": session, "doc_id": doc, "sheet": "假设", "range": "K1:K3", "direction": "down"})).await.unwrap();
    let k = client.request("doc/cells", json!({"session_id": session, "doc_id": doc, "sheet": "假设", "range": "K1:K3"})).await.unwrap();
    assert_eq!(cell(&k, "K3")["value"], json!({"type": "number", "value": 5.0}));
    let undone = client.request("doc/undo", json!({"session_id": session, "doc_id": doc})).await.unwrap();
    assert_eq!(undone["revision"], 4);
    let k = client.request("doc/cells", json!({"session_id": session, "doc_id": doc, "sheet": "假设", "range": "K2:K3"})).await.unwrap();
    assert!(k["cells"].as_array().unwrap().iter().all(|c| c["value"].is_null() || c["text"] == ""), "{k}");
    // Nothing to fill into: refused, nothing written.
    let refused = client.request("doc/fill", json!({"session_id": session, "doc_id": doc, "sheet": "假设", "range": "K1", "direction": "down"})).await;
    assert!(refused.is_err());

    // The fill handle: 1…4 dragged down carries on to 8, Q1 counts on, a formula moves; one edit.
    let ops = json!([
        {"op": "set_value", "sheet": "假设", "cell": "M1", "value": 1}, {"op": "set_value", "sheet": "假设", "cell": "M2", "value": 2},
        {"op": "set_value", "sheet": "假设", "cell": "M3", "value": 3}, {"op": "set_value", "sheet": "假设", "cell": "M4", "value": 4},
        {"op": "set_value", "sheet": "假设", "cell": "N1", "value": "Q1"}, {"op": "set_formula", "sheet": "假设", "cell": "O1", "formula": "=M1*2"},
    ]);
    client.request("doc/apply", json!({"session_id": session, "doc_id": doc, "ops": ops})).await.unwrap();
    // Previewed as the drag goes: what the last cell would show, nothing written.
    let preview = client.request("doc/autofill", json!({"session_id": session, "doc_id": doc, "sheet": "假设", "source": "M1:M4", "target": "M1:M8", "preview": true})).await.unwrap();
    assert!(preview["impact"]["edited"].as_array().unwrap().iter().any(|e| e["cell"] == "M8" && e["after_text"] == "8"), "{preview}");
    let untouched = client.request("doc/cells", json!({"session_id": session, "doc_id": doc, "sheet": "假设", "range": "M8"})).await.unwrap();
    assert!(untouched["cells"].as_array().unwrap().iter().all(|c| c["value"].is_null()), "{untouched}");
    let dragged = client.request("doc/autofill", json!({"session_id": session, "doc_id": doc, "sheet": "假设", "source": "M1:M4", "target": "M1:M8"})).await.unwrap();
    schema.check("DocApplyResult", &dragged);
    let m = client.request("doc/cells", json!({"session_id": session, "doc_id": doc, "sheet": "假设", "range": "M5:M8"})).await.unwrap();
    assert_eq!(cell(&m, "M8")["value"], json!({"type": "number", "value": 8.0}));
    client.request("doc/autofill", json!({"session_id": session, "doc_id": doc, "sheet": "假设", "source": "N1:O1", "target": "N1:O3"})).await.unwrap();
    let n = client.request("doc/cells", json!({"session_id": session, "doc_id": doc, "sheet": "假设", "range": "N3:O3"})).await.unwrap();
    assert_eq!(cell(&n, "N3")["value"], json!({"type": "text", "value": "Q3"}));
    assert_eq!(cell(&n, "O3")["formula"], "=M3*2");
    // Dragged up from M5:M8 (5…8): 4 above it, written over the old one.
    client.request("doc/autofill", json!({"session_id": session, "doc_id": doc, "sheet": "假设", "source": "M5:M8", "target": "M4:M8"})).await.unwrap();
    let m4 = client.request("doc/cells", json!({"session_id": session, "doc_id": doc, "sheet": "假设", "range": "M4"})).await.unwrap();
    assert_eq!(cell(&m4, "M4")["value"], json!({"type": "number", "value": 4.0}));
    // Not along one side: refused.
    assert!(client.request("doc/autofill", json!({"session_id": session, "doc_id": doc, "sheet": "假设", "source": "M1:M4", "target": "M1:N8"})).await.is_err());
}
