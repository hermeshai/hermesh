//! Provider-neutral fixes to a tool's input schema. Provider limits are handled by the model
//! adapters, in one place for every tool.

use serde_json::Map;
use serde_json::Value;
use serde_json::json;
use std::collections::BTreeSet;

/// The schema as sent to the model, or why the tool cannot be offered: a provider that rejects
/// one tool definition rejects every request that carries it.
pub fn normalize(schema: &Value) -> Result<Value, String> {
    let Value::Object(map) = schema else {
        return Err("the input schema is not an object".into());
    };
    let mut map = map.clone();
    match map.get("type") {
        None => {
            map.insert("type".into(), json!("object"));
        }
        Some(Value::String(t)) if t == "object" => {}
        Some(other) => return Err(format!("the input schema has type {other}, not \"object\"")),
    }
    match map.get("properties") {
        None | Some(Value::Null) => {
            map.insert("properties".into(), json!({}));
        }
        Some(Value::Object(_)) => {}
        Some(_) => return Err("`properties` is not an object".into()),
    }
    map.remove("$schema");
    map.remove("$id");
    let mut value = Value::Object(map);
    replace_true_subschemas(&mut value);
    prune_definitions(&mut value);
    Ok(value)
}

/// `true` as a subschema (anything goes) becomes `{}`, which some providers accept where they
/// reject booleans. `additionalProperties: true` is a keyword value, not a subschema here.
fn replace_true_subschemas(value: &mut Value) {
    let Value::Object(map) = value else { return };
    let fix = |v: &mut Value| {
        if *v == Value::Bool(true) {
            *v = json!({});
        }
    };
    for key in ["properties", "$defs", "definitions", "patternProperties"] {
        if let Some(Value::Object(children)) = map.get_mut(key) {
            for child in children.values_mut() {
                fix(child);
                replace_true_subschemas(child);
            }
        }
    }
    for key in ["anyOf", "oneOf", "allOf", "prefixItems"] {
        if let Some(Value::Array(children)) = map.get_mut(key) {
            for child in children.iter_mut() {
                fix(child);
                replace_true_subschemas(child);
            }
        }
    }
    for key in ["items", "not", "if", "then", "else"] {
        if let Some(child) = map.get_mut(key) {
            fix(child);
            replace_true_subschemas(child);
        }
    }
    if let Some(child @ Value::Object(_)) = map.get_mut("additionalProperties") {
        replace_true_subschemas(child);
    }
}

/// Drops `$defs` and `definitions` entries no `$ref` reaches: every request pays for them.
fn prune_definitions(root: &mut Value) {
    let Value::Object(map) = root else { return };
    let tables: Vec<&str> = ["$defs", "definitions"]
        .into_iter()
        .filter(|key| matches!(map.get(*key), Some(Value::Object(_))))
        .collect();
    if tables.is_empty() {
        return;
    }
    // References from outside the tables, then transitively from the definitions they reach.
    let mut outside = Map::new();
    for (key, value) in map.iter() {
        if !tables.contains(&key.as_str()) {
            outside.insert(key.clone(), value.clone());
        }
    }
    let mut reached: BTreeSet<(String, String)> = BTreeSet::new();
    let mut queue: Vec<(String, String)> = Vec::new();
    collect_refs(&Value::Object(outside), &mut queue);
    while let Some(target) = queue.pop() {
        if !reached.insert(target.clone()) {
            continue;
        }
        if let Some(def) = map.get(&target.0).and_then(|t| t.get(&target.1)) {
            collect_refs(def, &mut queue);
        }
    }
    for table in tables {
        if let Some(Value::Object(defs)) = map.get_mut(table) {
            defs.retain(|name, _| reached.contains(&(table.to_string(), name.clone())));
            if defs.is_empty() {
                map.remove(table);
            }
        }
    }
}

fn collect_refs(value: &Value, out: &mut Vec<(String, String)>) {
    match value {
        Value::Object(map) => {
            if let Some(Value::String(reference)) = map.get("$ref") {
                for table in ["$defs", "definitions"] {
                    if let Some(name) = reference.strip_prefix(&format!("#/{table}/")) {
                        let name = name.split('/').next().unwrap_or(name).replace("~1", "/").replace("~0", "~");
                        out.push((table.to_string(), name));
                    }
                }
            }
            map.values().for_each(|v| collect_refs(v, out));
        }
        Value::Array(items) => items.iter().for_each(|v| collect_refs(v, out)),
        _ => {}
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn fills_in_the_top_level() {
        assert_eq!(normalize(&json!({})).unwrap(), json!({"type": "object", "properties": {}}));
        let schema = json!({"$schema": "https://json-schema.org/draft/2020-12/schema", "type": "object",
            "properties": {"q": {"type": "string", "format": "uri"}}, "required": ["q"]});
        let out = normalize(&schema).unwrap();
        assert!(out.get("$schema").is_none());
        assert_eq!(out["properties"]["q"]["format"], "uri", "other keywords are kept");
        assert!(normalize(&json!({"type": "array"})).is_err());
        assert!(normalize(&json!("nope")).is_err());
    }

    #[test]
    fn fixes_true_subschemas_and_prunes_unused_definitions() {
        let schema = json!({
            "type": "object",
            "properties": {"any": true, "item": {"$ref": "#/$defs/Item"}},
            "additionalProperties": true,
            "$defs": {
                "Item": {"type": "object", "properties": {"tag": {"$ref": "#/$defs/Tag"}}},
                "Tag": {"type": "string"},
                "Unused": {"type": "number"}
            }
        });
        let out = normalize(&schema).unwrap();
        assert_eq!(out["properties"]["any"], json!({}));
        assert_eq!(out["additionalProperties"], json!(true));
        let defs = out["$defs"].as_object().unwrap();
        assert!(defs.contains_key("Item") && defs.contains_key("Tag") && !defs.contains_key("Unused"));
        let none_used = normalize(&json!({"type": "object", "definitions": {"X": {}}})).unwrap();
        assert!(none_used.get("definitions").is_none());
    }
}
