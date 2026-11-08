//! The document as the agent reads it: body paragraphs in reading order (table cells in row
//! order where their table stands), with comments and revisions.

use std::collections::HashMap;

use docx_edit::session::EditSessionCore;
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};

use crate::{Result, WordError, engine, parse};

/// Characters of a paragraph an outline entry shows.
const SUMMARY_CHARS: usize = 60;
/// Characters of context on each side of a match.
const CONTEXT_CHARS: usize = 20;
/// What a paragraph's text holds for an inline object (image, field, comment mark).
pub const OBJECT: char = '\u{FFFC}';

/// Where a paragraph sits in a table, all from 1.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct CellPosition {
    pub table: u32,
    pub row: u32,
    pub column: u32,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct ListItem {
    /// From 0.
    pub level: u32,
    /// The marker as drawn, e.g. `1.`.
    pub marker: String,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct OutlineEntry {
    pub id: String,
    /// Heading level from 1; `None` for body text.
    pub heading: Option<u8>,
    pub style: Option<String>,
    /// The first characters of the text, inline objects left out.
    pub summary: String,
    pub list: Option<ListItem>,
    pub table: Option<CellPosition>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct ParagraphView {
    pub id: String,
    /// With pending revisions applied; each inline object is one [`OBJECT`].
    pub text: String,
    pub heading: Option<u8>,
    pub style: Option<String>,
    pub list: Option<ListItem>,
    pub table: Option<CellPosition>,
    /// Comments anchored in this paragraph.
    pub comments: Vec<String>,
    /// Revisions starting or ending in this paragraph.
    pub revisions: Vec<String>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Found {
    pub paragraph: String,
    /// Characters before the match in the paragraph's text.
    pub offset: usize,
    pub context: String,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Comment {
    pub id: String,
    pub author: String,
    pub date: String,
    pub done: bool,
    /// The comment this one replies to.
    pub parent: Option<String>,
    pub text: String,
    /// Paragraphs its anchor covers; empty for a reply or an unanchored comment.
    pub paragraphs: Vec<String>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum RevisionKind {
    Insertion,
    Deletion,
    /// Run or paragraph properties changed.
    Format,
    /// A paragraph mark, table row or table inserted or deleted.
    Structure,
}

/// One tracked change. A replacement is two entries, a deletion and an insertion, under one id.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Revision {
    pub id: String,
    pub kind: RevisionKind,
    pub author: String,
    pub date: String,
    /// The first characters of the text it covers (the engine's preview); empty for structure.
    pub text: String,
    pub paragraph: String,
}

/// One body paragraph as the engine reads it.
pub(crate) struct Paragraph {
    pub id: String,
    pub story: String,
    pub text: String,
    pub style: Option<String>,
    pub heading: Option<u8>,
    pub list: Option<ListItem>,
    pub table: Option<CellPosition>,
    pub cell: Option<Cell>,
}

/// The cell a paragraph is in, as the engine addresses it: the table's story, its index there,
/// and the row and column from 0.
#[derive(Clone)]
pub(crate) struct Cell {
    pub parent: String,
    pub table_index: u32,
    pub row: u32,
    pub column: u32,
    pub rows: u32,
    pub columns: u32,
}

/// Body paragraphs in reading order.
pub(crate) fn paragraphs(session: &EditSessionCore) -> Result<Vec<Paragraph>> {
    let request = json!({ "stories": ["body"], "byRoot": true, "view": "accepted" });
    let read = parse(&engine(session.read_stories_json(&request.to_string()))?)?;
    let mut by_story: HashMap<String, Vec<Value>> = HashMap::new();
    for story in read["stories"].as_array().into_iter().flatten() {
        if story["ok"] == true {
            let id = story["story"].as_str().unwrap_or_default().to_owned();
            by_story.insert(
                id,
                story["paragraphs"].as_array().cloned().unwrap_or_default(),
            );
        }
    }
    let mut out = Vec::new();
    let mut tables = 0;
    walk(session, "body", None, &by_story, &mut tables, &mut out)?;
    Ok(out)
}

fn walk(
    session: &EditSessionCore,
    story: &str,
    cell: Option<&(CellPosition, Cell)>,
    read: &HashMap<String, Vec<Value>>,
    tables: &mut u32,
    out: &mut Vec<Paragraph>,
) -> Result<()> {
    let texts = read.get(story).map(Vec::as_slice).unwrap_or_default();
    let mut local = 0;
    for segment in segments(session, story)? {
        match segment["kind"].as_str() {
            Some("pilcrow") => {
                // A paragraph whose mark is a pending deletion reads as part of the next one.
                let Some(text) = texts.iter().find(|t| t["paraId"] == segment["paraId"]) else {
                    continue;
                };
                out.push(paragraph(story, text, cell.cloned()));
            }
            Some("embed") if segment["embedKind"] == "table" => {
                *tables += 1;
                let table = *tables;
                let rows = segment["payload"]["rows"]
                    .as_array()
                    .cloned()
                    .unwrap_or_default();
                for (row, cells) in rows.iter().enumerate() {
                    let cells = cells["cells"].as_array().cloned().unwrap_or_default();
                    for (column, cell) in cells.iter().enumerate() {
                        let Some(child) = cell["story"].as_str() else {
                            continue;
                        };
                        let (row, column) = (row as u32, column as u32);
                        let at = CellPosition {
                            table,
                            row: row + 1,
                            column: column + 1,
                        };
                        let cell = Cell {
                            parent: story.to_owned(),
                            table_index: local,
                            row,
                            column,
                            rows: rows.len() as u32,
                            columns: cells.len() as u32,
                        };
                        walk(session, child, Some(&(at, cell)), read, tables, out)?;
                    }
                }
                local += 1;
            }
            // A block content control (a template's "click here to type" placeholder, say): its
            // paragraphs are in a story of their own, read in place, in the same cell as it.
            Some("embed") if segment["embedKind"] == "blockSdt" => {
                if let Some(child) = segment["payload"]["story"].as_str() {
                    walk(session, child, cell, read, tables, out)?;
                }
            }
            _ => {}
        }
    }
    Ok(())
}

pub(crate) fn segments(session: &EditSessionCore, story: &str) -> Result<Vec<Value>> {
    let value = parse(&engine(session.story_segments(story))?)?;
    Ok(value.as_array().cloned().unwrap_or_default())
}

fn paragraph(story: &str, text: &Value, cell: Option<(CellPosition, Cell)>) -> Paragraph {
    let (table, cell) = cell.unzip();
    let list = text["list"].as_object().map(|list| ListItem {
        level: list.get("level").and_then(Value::as_u64).unwrap_or(0) as u32,
        marker: list
            .get("marker")
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_owned(),
    });
    Paragraph {
        id: text["paraId"].as_str().unwrap_or_default().to_owned(),
        story: story.to_owned(),
        text: text["text"].as_str().unwrap_or_default().to_owned(),
        style: text["styleId"].as_str().map(str::to_owned),
        heading: text["outlineLevel"].as_u64().map(|level| level as u8 + 1),
        list,
        table,
        cell,
    }
}

pub(crate) fn outline(session: &EditSessionCore) -> Result<Vec<OutlineEntry>> {
    Ok(paragraphs(session)?
        .into_iter()
        .map(|p| OutlineEntry {
            summary: p
                .text
                .chars()
                .filter(|c| *c != OBJECT)
                .take(SUMMARY_CHARS)
                .collect(),
            id: p.id,
            heading: p.heading,
            style: p.style,
            list: p.list,
            table: p.table,
        })
        .collect())
}

pub(crate) fn read(
    session: &EditSessionCore,
    from: &str,
    count: usize,
) -> Result<Vec<ParagraphView>> {
    let all = paragraphs(session)?;
    let start = all
        .iter()
        .position(|p| p.id == from)
        .ok_or_else(|| WordError::NoParagraph(from.to_owned()))?;
    let wanted = &all[start..all.len().min(start + count)];
    let anchored = comment_paragraphs(session, wanted)?;
    let revisions = revisions(session)?;
    Ok(wanted
        .iter()
        .map(|p| {
            let comments = anchored
                .iter()
                .filter(|(_, paragraphs)| paragraphs.contains(&p.id))
                .map(|(id, _)| id.clone())
                .collect();
            let mut ids: Vec<String> = Vec::new();
            for revision in revisions
                .iter()
                .filter(|r| r.paragraph == p.id || r.end == p.id)
            {
                if !ids.contains(&revision.id) {
                    ids.push(revision.id.clone());
                }
            }
            ParagraphView {
                id: p.id.clone(),
                text: p.text.clone(),
                heading: p.heading,
                style: p.style.clone(),
                list: p.list.clone(),
                table: p.table.clone(),
                comments,
                revisions: ids,
            }
        })
        .collect())
}

pub(crate) fn find(session: &EditSessionCore, query: &str, limit: usize) -> Result<Vec<Found>> {
    let mut found = Vec::new();
    if query.is_empty() {
        return Ok(found);
    }
    for p in paragraphs(session)? {
        // UTF-16 offsets of the paragraph's text, mapped back to characters.
        let units: Vec<usize> = p
            .text
            .chars()
            .scan(0, |at, c| {
                let here = *at;
                *at += c.len_utf16();
                Some(here)
            })
            .collect();
        let chars: Vec<char> = p.text.chars().collect();
        for offset in matches(&p.text, query) {
            if found.len() == limit {
                return Ok(found);
            }
            let start = units.iter().position(|u| *u == offset).unwrap_or(0);
            let end = start + query.chars().count();
            let context: String = chars
                [start.saturating_sub(CONTEXT_CHARS)..chars.len().min(end + CONTEXT_CHARS)]
                .iter()
                .collect();
            found.push(Found {
                paragraph: p.id.clone(),
                offset: start,
                context,
            });
        }
    }
    Ok(found)
}

/// UTF-16 offsets of each non-overlapping exact match of `query` in `text`.
pub(crate) fn matches(text: &str, query: &str) -> Vec<usize> {
    text.match_indices(query)
        .map(|(byte, _)| text[..byte].encode_utf16().count())
        .collect()
}

pub(crate) fn comments(session: &EditSessionCore) -> Result<Vec<Comment>> {
    let listed = parse(&engine(session.list_comments())?)?;
    let all = paragraphs(session)?;
    let anchored = comment_paragraphs(session, &all)?;
    Ok(listed
        .as_array()
        .into_iter()
        .flatten()
        .map(|c| {
            let id = c["id"].as_str().unwrap_or_default().to_owned();
            let mut text = String::new();
            flatten_text(&c["body"], &mut text);
            Comment {
                paragraphs: anchored.get(&id).cloned().unwrap_or_default(),
                author: c["author"].as_str().unwrap_or_default().to_owned(),
                date: c["date"].as_str().unwrap_or_default().to_owned(),
                done: c["done"].as_bool().unwrap_or(false),
                parent: c["parentId"].as_str().map(str::to_owned),
                text: text.trim_end_matches('\n').to_owned(),
                id,
            }
        })
        .collect())
}

/// The text of a comment body: document-model blocks, one paragraph per line.
fn flatten_text(value: &Value, out: &mut String) {
    match value {
        Value::Array(items) => items.iter().for_each(|item| flatten_text(item, out)),
        Value::Object(map) => {
            if let Some(Value::String(text)) = map.get("text") {
                out.push_str(text);
            }
            if let Some(content) = map.get("content") {
                flatten_text(content, out);
            }
            if map.get("type").and_then(Value::as_str) == Some("paragraph") {
                out.push('\n');
            }
        }
        Value::String(text) => out.push_str(text),
        _ => {}
    }
}

/// Each anchored comment's paragraphs among `within`.
fn comment_paragraphs(
    session: &EditSessionCore,
    within: &[Paragraph],
) -> Result<HashMap<String, Vec<String>>> {
    let listed = parse(&engine(session.list_comments())?)?;
    let mut spans: HashMap<&str, (u64, u64)> = HashMap::new();
    for p in within {
        let span = parse(&engine(session.locate_paragraph(&p.story, &p.id))?)?;
        spans.insert(
            &p.id,
            (
                span["start"].as_u64().unwrap_or(0),
                span["end"].as_u64().unwrap_or(0),
            ),
        );
    }
    let mut out = HashMap::new();
    for comment in listed.as_array().into_iter().flatten() {
        let id = comment["id"].as_str().unwrap_or_default();
        let Ok(anchors) = session.resolve_comment(id) else {
            continue;
        };
        let anchors = parse(&anchors)?;
        let mut covered = Vec::new();
        for anchor in anchors.as_array().into_iter().flatten() {
            let (story, start, end) = (
                anchor["story"].as_str().unwrap_or_default(),
                anchor["start"].as_u64().unwrap_or(0),
                anchor["end"].as_u64().unwrap_or(0),
            );
            for p in within.iter().filter(|p| p.story == story) {
                let (from, to) = spans[p.id.as_str()];
                if start <= to && end >= from && !covered.contains(&p.id) {
                    covered.push(p.id.clone());
                }
            }
        }
        out.insert(id.to_owned(), covered);
    }
    Ok(out)
}

pub(crate) struct Listed {
    pub id: String,
    pub kind: RevisionKind,
    pub author: String,
    pub date: String,
    pub text: String,
    pub paragraph: String,
    pub end: String,
}

pub(crate) fn revisions(session: &EditSessionCore) -> Result<Vec<Listed>> {
    let listed = parse(&engine(session.list_revisions())?)?;
    Ok(listed
        .as_array()
        .into_iter()
        .flatten()
        .map(|r| Listed {
            id: r["revisionId"].as_str().unwrap_or_default().to_owned(),
            kind: match r["kind"].as_str() {
                Some("insertion") => RevisionKind::Insertion,
                Some("deletion") => RevisionKind::Deletion,
                Some("rPrChange" | "pPrChange") => RevisionKind::Format,
                _ => RevisionKind::Structure,
            },
            author: r["author"].as_str().unwrap_or_default().to_owned(),
            date: r["date"].as_str().unwrap_or_default().to_owned(),
            text: r["preview"].as_str().unwrap_or_default().to_owned(),
            paragraph: r["range"]["start"]["paraId"]
                .as_str()
                .unwrap_or_default()
                .to_owned(),
            end: r["range"]["end"]["paraId"]
                .as_str()
                .unwrap_or_default()
                .to_owned(),
        })
        .collect())
}

impl From<Listed> for Revision {
    fn from(r: Listed) -> Self {
        Self {
            id: r.id,
            kind: r.kind,
            author: r.author,
            date: r.date,
            text: r.text,
            paragraph: r.paragraph,
        }
    }
}
