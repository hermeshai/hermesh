//! Calculation (design §5.6): IronCalc, fed from our own reading of the file (never its xlsx
//! reader, never its writer), with iterative calculation added on top, and the impact of edits.
//!
//! - **Constants**: formulas we cannot compute stand as their stored value: links to other
//!   workbooks, and functions IronCalc does not know (data-vendor add-ins such as Wind's `WSS`).
//! - **Iteration** (§5.6.2): IronCalc has none. Each cycle is cut at a feedback set; those cells
//!   hold a value, their formulas move to a hidden probe sheet at the same address (references
//!   qualified with the original sheet), and evaluate → read probes → update repeats until the
//!   values settle.
//! - **Trust** (§5.6.4): right after opening, a formula whose result differs from what Excel
//!   saved — and everything downstream of it — is not trusted: impact names it "recalculate in
//!   Excel" instead of giving a number, and its saved value is not replaced.

use std::collections::BTreeMap;
use std::collections::BTreeSet;
use std::collections::HashMap;

use ironcalc_base::Model;
use ironcalc_base::cell::CellValue;
use ironcalc_base::types::CellType;

use crate::Cached;
use crate::CellEdit;
use crate::Error;
use crate::NewValue;
use crate::book::Book;
use crate::book::Value;
use crate::cellref::CellRef;
use crate::deps::Graph;
use crate::deps::Node;
use crate::formula::Tok;
use crate::formula::display;
use crate::formula::is_excel_error;
use crate::formula::normalize_for_write;
use crate::formula::tokenize;
use crate::package::SheetKind;

/// Values closer than this (relative) are the same result.
const SAME: f64 = 1e-9;
/// Iteration stops once no cut cell moves more than this (relative), or Excel's own delta if
/// that is tighter.
const SETTLED: f64 = 1e-12;

/// A cell whose value an edit changed.
#[derive(Clone, Debug, PartialEq)]
pub struct Change {
    pub node: Node,
    pub before: Value,
    pub after: Value,
}

#[derive(Clone, Debug, Default, PartialEq)]
pub struct Impact {
    /// The edited cells.
    pub edited: Vec<Change>,
    /// Formula cells whose value changed (trusted ones).
    pub changed: Vec<Change>,
    /// Of those, the ones no formula reads: the model's outputs.
    pub key_outputs: Vec<Node>,
    /// Formula cells downstream of the edit whose value we cannot vouch for.
    pub needs_excel: Vec<Node>,
    /// Rounds of iteration, when cycles were involved.
    pub iterations: Option<u32>,
    /// False when iteration hit its limit without settling.
    pub converged: bool,
}

pub struct Engine {
    pub book: Book,
    model: Model<'static>,
    /// Book sheet → IronCalc sheet.
    sheet: Vec<Option<u32>>,
    pub graph: Graph,
    /// Formula cells held as their stored value.
    constants: BTreeSet<Node>,
    /// Formula cells whose result we do not vouch for (§5.6.4).
    untrusted: BTreeSet<Node>,
    /// Iteration: the cut cells, their current values, and each sheet's probe sheet.
    cut: Vec<Node>,
    cut_values: Vec<Value>,
    probe: HashMap<usize, u32>,
    pub iterations: Option<u32>,
    pub converged: bool,
}

fn unavailable(e: String) -> Error {
    Error::Unsupported(format!(
        "calculation is not available for this workbook: {e}"
    ))
}

fn has_external_link(formula: &str) -> bool {
    tokenize(formula).is_ok_and(|t| {
        t.iter()
            .any(|t| matches!(t, Tok::SheetPrefix(p) if p.starts_with('[') || p.starts_with("'[")))
    })
}

/// The formula as seen from another sheet: references without a sheet name get `sheet`'s.
fn qualify(formula: &str, sheet: &str) -> String {
    let q = format!("'{}'!", sheet.replace('\'', "''"));
    let Ok(toks) = tokenize(formula) else {
        return formula.to_string();
    };
    let mut out = String::with_capacity(formula.len() + 16);
    for (i, t) in toks.iter().enumerate() {
        let prev = i.checked_sub(1).map(|p| &toks[p]);
        let qualified = matches!(prev, Some(Tok::SheetPrefix(_)));
        let range_end = prev.is_some_and(|p| p.text() == ":");
        if matches!(t, Tok::Cell(_) | Tok::Col(_) | Tok::Row(_)) && !qualified && !range_end {
            out.push_str(&q);
        }
        out.push_str(t.text());
    }
    out
}

fn relative_change(a: &Value, b: &Value) -> f64 {
    match (a, b) {
        (Value::Number(x), Value::Number(y)) => (x - y).abs() / x.abs().max(1.0),
        _ if a == b => 0.0,
        _ => f64::INFINITY,
    }
}

impl Engine {
    pub fn new(book: Book) -> Result<Self, Error> {
        let mut model = Model::new_empty("her", "en", "UTC", "en").map_err(unavailable)?;
        let mut sheet = Vec::with_capacity(book.sheets.len());
        let mut first = true;
        for s in &book.sheets {
            if s.kind != SheetKind::Worksheet {
                sheet.push(None);
                continue;
            }
            if first {
                model
                    .rename_sheet_by_index(0, &s.name)
                    .map_err(unavailable)?;
                first = false;
            } else {
                model.add_sheet(&s.name).map_err(unavailable)?;
            }
            sheet.push(Some(model.workbook.worksheets.len() as u32 - 1));
        }
        if first {
            return Err(unavailable("no worksheets".into()));
        }
        for n in book
            .info
            .names
            .iter()
            .filter(|n| !n.hidden && !n.name.starts_with("_xlnm."))
        {
            let scope = n.scope.and_then(|s| sheet.get(s).copied().flatten());
            // A name IronCalc cannot take only makes formulas using it #NAME?, which trust handles.
            let _ = model.new_defined_name(&n.name, scope, &display(&n.formula));
        }
        let mut constants = BTreeSet::new();
        for (s, sh) in book.sheets.iter().enumerate() {
            let (Some(ix), Some(i)) = (&sh.index, sheet[s]) else {
                continue;
            };
            for row in &ix.rows {
                if row.hidden {
                    model
                        .set_row_hidden(i, row.r as i32, true)
                        .map_err(unavailable)?;
                }
            }
            for c in ix.cells() {
                let (r, col) = (c.at.row as i32, c.at.col as i32);
                match sh.formulas.get(&c.at) {
                    Some(f) if !has_external_link(f) => {
                        model
                            .update_cell_with_formula(
                                i,
                                r,
                                col,
                                format!("={}", display(&crate::tables::expand(f, s, c.at, &book))),
                            )
                            .map_err(unavailable)?;
                    }
                    other => {
                        if other.is_some() {
                            constants.insert((s, c.at));
                        }
                        set_constant(
                            &mut model,
                            i,
                            r,
                            col,
                            &crate::book::value_of(c, &book.strings),
                        )?;
                    }
                }
            }
        }
        let mut graph = Graph::build(&book);
        for c in &constants {
            graph.reads.insert(*c, Vec::new());
        }
        let mut e = Engine {
            book,
            model,
            sheet,
            graph,
            constants,
            untrusted: BTreeSet::new(),
            cut: Vec::new(),
            cut_values: Vec::new(),
            probe: HashMap::new(),
            iterations: None,
            converged: true,
        };
        e.setup_cycles(false)?;
        e.recalc();
        // Functions IronCalc does not know: hold them at the value Excel saved.
        let unknown: Vec<Node> = e
            .formula_cells()
            .filter(|n| !e.constants.contains(n))
            .filter(|n| {
                e.value(*n) == Value::Error("#NAME?".into())
                    && e.book.value(n.0, n.1) != Value::Error("#NAME?".into())
            })
            .collect();
        if !unknown.is_empty() {
            for n in &unknown {
                let v = e.book.value(n.0, n.1);
                let i = e.sheet[n.0].unwrap();
                set_constant(&mut e.model, i, n.1.row as i32, n.1.col as i32, &v)?;
                e.constants.insert(*n);
                e.graph.reads.insert(*n, Vec::new());
            }
            e.restore_cuts()?;
            e.setup_cycles(false)?;
            e.recalc();
        }
        let roots: Vec<Node> = e
            .formula_cells()
            .filter(|n| !e.constants.contains(n))
            .filter(|n| !e.value(*n).close_to(&e.book.value(n.0, n.1), SAME))
            .collect();
        e.untrusted = e.graph.downstream(&roots);
        e.untrusted.extend(roots);
        Ok(e)
    }

    fn formula_cells(&self) -> impl Iterator<Item = Node> + '_ {
        self.book
            .sheets
            .iter()
            .enumerate()
            .flat_map(|(s, sh)| sh.formulas.keys().map(move |at| (s, *at)))
    }

    /// The current value of a cell.
    pub fn value(&self, n: Node) -> Value {
        if let Some(j) = self.cut.iter().position(|c| *c == n) {
            return self.cut_values[j].clone();
        }
        let Some(i) = self.sheet.get(n.0).copied().flatten() else {
            return Value::Empty;
        };
        self.ironcalc_value(i, n.1)
    }

    fn ironcalc_value(&self, i: u32, at: CellRef) -> Value {
        let (r, c) = (at.row as i32, at.col as i32);
        let v = self
            .model
            .get_cell_value_by_index(i, r, c)
            .unwrap_or(CellValue::None);
        match v {
            CellValue::None => Value::Empty,
            CellValue::Number(n) => Value::Number(n),
            CellValue::Boolean(b) => Value::Bool(b),
            CellValue::String(s) => match self.model.get_cell_type(i, r, c) {
                Ok(CellType::ErrorValue) => Value::Error(s),
                _ => Value::Text(s),
            },
        }
    }

    /// Where the cycles are cut for iteration.
    pub fn cut(&self) -> &[Node] {
        &self.cut
    }

    /// Formula cells held at their stored value.
    pub fn constants(&self) -> &BTreeSet<Node> {
        &self.constants
    }

    pub fn trusted(&self, n: Node) -> bool {
        !self.untrusted.contains(&n)
    }

    /// Puts the formulas of cut cells back where they were and drops the probes.
    fn restore_cuts(&mut self) -> Result<(), Error> {
        for &(s, at) in &self.cut {
            let i = self.sheet[s].unwrap();
            if let Some(f) = self.book.sheets[s].formulas.get(&at) {
                self.model
                    .update_cell_with_formula(
                        i,
                        at.row as i32,
                        at.col as i32,
                        format!("={}", display(&crate::tables::expand(f, s, at, &self.book))),
                    )
                    .map_err(unavailable)?;
            }
            let p = self.probe[&s];
            self.model
                .set_user_input(p, at.row as i32, at.col as i32, String::new())
                .map_err(unavailable)?;
        }
        self.cut.clear();
        self.cut_values.clear();
        Ok(())
    }

    /// Cuts every cycle at a feedback set, if the workbook iterates. `from_zero` starts the cut
    /// cells at 0 instead of the value the file holds (§5.6.2, "recalculate cycles").
    fn setup_cycles(&mut self, from_zero: bool) -> Result<(), Error> {
        if !self.book.info.calc.iterate {
            return Ok(());
        }
        let cycles = self.graph.cycles(None);
        self.cut = self.graph.feedback(&cycles);
        self.cut_values.clear();
        for k in 0..self.cut.len() {
            let (s, at) = self.cut[k];
            if !self.probe.contains_key(&s) {
                let name = format!("__her_probe_{s}");
                self.model.add_sheet(&name).map_err(unavailable)?;
                self.probe
                    .insert(s, self.model.workbook.worksheets.len() as u32 - 1);
            }
            let f = &self.book.sheets[s].formulas[&at];
            let q = qualify(
                &display(&crate::tables::expand(f, s, at, &self.book)),
                &self.book.sheets[s].name,
            );
            self.model
                .update_cell_with_formula(
                    self.probe[&s],
                    at.row as i32,
                    at.col as i32,
                    format!("={q}"),
                )
                .map_err(unavailable)?;
            let start = if from_zero {
                Value::Number(0.0)
            } else {
                self.book.value(s, at)
            };
            let start = if start == Value::Empty {
                Value::Number(0.0)
            } else {
                start
            };
            set_constant(
                &mut self.model,
                self.sheet[s].unwrap(),
                at.row as i32,
                at.col as i32,
                &start,
            )?;
            self.cut_values.push(start);
        }
        Ok(())
    }

    /// Evaluates everything, iterating cycles until they settle.
    pub fn recalc(&mut self) {
        self.model.evaluate();
        if self.cut.is_empty() {
            self.iterations = None;
            self.converged = true;
            return;
        }
        let calc = self.book.info.calc;
        let limit = calc.iterate_count.max(100);
        let tol = SETTLED.min(calc.iterate_delta);
        let mut change = f64::INFINITY;
        let mut rounds = 0;
        while rounds < limit && change >= tol {
            rounds += 1;
            change = 0.0;
            let next: Vec<Value> = self
                .cut
                .iter()
                .map(|(s, at)| self.ironcalc_value(self.probe[s], *at))
                .collect();
            for (k, v) in next.into_iter().enumerate() {
                change = change.max(relative_change(&self.cut_values[k], &v));
                if v != self.cut_values[k] {
                    let (s, at) = self.cut[k];
                    let _ = set_constant(
                        &mut self.model,
                        self.sheet[s].unwrap(),
                        at.row as i32,
                        at.col as i32,
                        &v,
                    );
                    self.cut_values[k] = v;
                }
            }
            self.model.evaluate();
        }
        self.iterations = Some(rounds);
        self.converged = change < calc.iterate_delta.max(tol);
    }

    /// From 0 again: for cycles stuck on an error value (§5.6.2).
    pub fn recalc_cycles_from_zero(&mut self) -> Result<(), Error> {
        self.restore_cuts()?;
        self.setup_cycles(true)?;
        self.recalc();
        Ok(())
    }

    /// Explicit recalculation compares with the working copy. This reports discrepancies;
    /// it never promotes untrusted results or writes cached values to the file.
    pub fn check_recalculation(&mut self, circular_only: bool) -> Result<Impact, Error> {
        if circular_only {
            self.recalc_cycles_from_zero()?;
        } else {
            self.recalc();
        }
        let mut impact = Impact {
            iterations: self.iterations,
            converged: self.converged,
            ..Default::default()
        };
        let circular = self
            .graph
            .cycles(None)
            .into_iter()
            .flatten()
            .collect::<Vec<_>>();
        let selected = self.graph.downstream(&circular);
        for n in self.formula_cells() {
            if circular_only && !circular.contains(&n) && !selected.contains(&n) {
                continue;
            }
            let before = self.book.value(n.0, n.1);
            let after = self.value(n);
            if before.close_to(&after, SAME) {
                continue;
            }
            if !self.trusted(n) || !self.converged || self.constants.contains(&n) {
                impact.needs_excel.push(n);
            } else {
                impact.changed.push(Change {
                    node: n,
                    before,
                    after,
                });
            }
        }
        impact.key_outputs = impact
            .changed
            .iter()
            .map(|c| c.node)
            .filter(|n| self.graph.read_by.get(n).is_none_or(|r| r.is_empty()))
            .collect();
        Ok(impact)
    }

    fn snapshot(&self) -> BTreeMap<Node, Value> {
        self.formula_cells().map(|n| (n, self.value(n))).collect()
    }

    fn set(&mut self, n: Node, v: &NewValue) -> Result<bool, Error> {
        let i = self
            .sheet
            .get(n.0)
            .copied()
            .flatten()
            .ok_or_else(|| Error::NotFound("not a worksheet".into()))?;
        let (r, c) = (n.1.row as i32, n.1.col as i32);
        let was_formula = self.book.sheets[n.0].formulas.contains_key(&n.1);
        let formulas_change = was_formula || matches!(v, NewValue::Formula { .. });
        if formulas_change {
            self.restore_cuts()?;
        }
        self.constants.remove(&n);
        match v {
            NewValue::Formula { text, .. } => {
                let stored = normalize_for_write(text)?;
                self.model
                    .update_cell_with_formula(
                        i,
                        r,
                        c,
                        format!(
                            "={}",
                            display(&crate::tables::expand(&stored, n.0, n.1, &self.book))
                        ),
                    )
                    .map_err(unavailable)?;
                self.book.sheets[n.0].formulas.insert(n.1, stored);
            }
            other => {
                self.book.sheets[n.0].formulas.remove(&n.1);
                let value = match other {
                    NewValue::Number { value } => Value::Number(*value),
                    NewValue::Text { value } => Value::Text(value.clone()),
                    NewValue::Bool { value } => Value::Bool(*value),
                    _ => Value::Empty,
                };
                set_constant(&mut self.model, i, r, c, &value)?;
            }
        }
        Ok(formulas_change)
    }

    fn rebuild_graph(&mut self) -> Result<(), Error> {
        self.graph = Graph::build(&self.book);
        for c in &self.constants {
            self.graph.reads.insert(*c, Vec::new());
        }
        self.setup_cycles(false)
    }

    /// Applies edits and returns what they changed.
    pub fn apply(&mut self, edits: &[(Node, NewValue)]) -> Result<Impact, Error> {
        let before = self.snapshot();
        let edited_before: Vec<Value> = edits.iter().map(|(n, _)| self.value(*n)).collect();
        let mut rebuild = false;
        for (n, v) in edits {
            rebuild |= self.set(*n, v)?;
        }
        if rebuild {
            self.rebuild_graph()?;
            // A cell the edit turned into a formula is trusted unless it reads something untrusted.
            let roots: Vec<Node> = self
                .untrusted
                .iter()
                .copied()
                .filter(|n| self.book.sheets[n.0].formulas.contains_key(&n.1))
                .collect();
            self.untrusted = self.graph.downstream(&roots);
            self.untrusted.extend(roots);
        }
        self.recalc();
        let after = self.snapshot();
        let edited_nodes: BTreeSet<Node> = edits.iter().map(|(n, _)| *n).collect();
        let mut impact = Impact {
            iterations: self.iterations,
            converged: self.converged,
            ..Default::default()
        };
        for ((n, _), b) in edits.iter().zip(edited_before) {
            impact.edited.push(Change {
                node: *n,
                before: b,
                after: self.value(*n),
            });
        }
        for (n, a) in &after {
            if edited_nodes.contains(n) {
                continue;
            }
            let b = before.get(n).cloned().unwrap_or(Value::Empty);
            if b.close_to(a, 1e-15) {
                continue;
            }
            if self.untrusted.contains(n) || !self.converged && self.cut.iter().any(|c| c == n) {
                impact.needs_excel.push(*n);
            } else {
                impact.changed.push(Change {
                    node: *n,
                    before: b,
                    after: a.clone(),
                });
            }
        }
        // Untrusted cells downstream of the edit stay unknown even if our number did not move.
        for n in self
            .graph
            .downstream(&edited_nodes.iter().copied().collect::<Vec<_>>())
        {
            if self.untrusted.contains(&n) && !impact.needs_excel.contains(&n) {
                impact.needs_excel.push(n);
            }
        }
        impact.needs_excel.sort();
        impact.key_outputs = impact
            .changed
            .iter()
            .map(|c| c.node)
            .filter(|n| self.graph.read_by.get(n).is_none_or(|r| r.is_empty()))
            .collect();
        Ok(impact)
    }

    /// What edits would change, without keeping them.
    pub fn preview(&mut self, edits: &[(Node, NewValue)]) -> Result<Impact, Error> {
        let undo: Vec<(Node, NewValue)> = edits
            .iter()
            .map(|(n, _)| {
                let old = match self.book.sheets[n.0].formulas.get(&n.1) {
                    Some(f) => NewValue::Formula {
                        text: f.clone(),
                        cached: None,
                    },
                    None => match self.value(*n) {
                        Value::Number(v) => NewValue::Number { value: v },
                        Value::Text(v) => NewValue::Text { value: v },
                        Value::Bool(v) => NewValue::Bool { value: v },
                        _ => NewValue::Empty,
                    },
                };
                (*n, old)
            })
            .collect();
        let untrusted = self.untrusted.clone();
        let constants = self.constants.clone();
        let impact = self.apply(edits)?;
        self.apply(&undo.into_iter().rev().collect::<Vec<_>>())?;
        // Undoing restores what the cells were; keep the trust and constants we had.
        self.untrusted = untrusted;
        for c in constants {
            if self.book.sheets[c.0].formulas.contains_key(&c.1) {
                let v = self.book.value(c.0, c.1);
                set_constant(
                    &mut self.model,
                    self.sheet[c.0].unwrap(),
                    c.1.row as i32,
                    c.1.col as i32,
                    &v,
                )?;
                self.constants.insert(c);
                self.graph.reads.insert(c, Vec::new());
            }
        }
        self.recalc();
        Ok(impact)
    }
}

impl Engine {
    fn cached(&self, n: Node) -> Option<Cached> {
        if !self.trusted(n) {
            return None;
        }
        Some(match self.value(n) {
            // An error of IronCalc's own (an unsupported reference or function): Excel has no
            // such value and would reject the file.
            Value::Error(v) if !is_excel_error(&v) => return None,
            Value::Number(v) => Cached::Number(v),
            Value::Text(v) => Cached::Text(v),
            Value::Bool(v) => Cached::Bool(v),
            Value::Error(v) => Cached::Error(v),
            Value::Empty => Cached::Text(String::new()),
        })
    }

    fn cell_edit(&self, n: Node, value: NewValue) -> CellEdit {
        CellEdit {
            sheet: self.book.sheets[n.0].name.clone(),
            cell: n.1.to_string(),
            value,
        }
    }

    /// What to write for edits just applied (§5.6.4): the edits, formulas with the result we
    /// computed when we vouch for it, and new results for the trusted cells downstream.
    pub fn writes(&self, edits: &[(Node, NewValue)], impact: &Impact) -> Vec<CellEdit> {
        let mut out: Vec<CellEdit> = edits
            .iter()
            .map(|(n, v)| {
                let v = match v {
                    NewValue::Formula { text, .. } => NewValue::Formula {
                        text: text.clone(),
                        cached: self.cached(*n),
                    },
                    other => other.clone(),
                };
                self.cell_edit(*n, v)
            })
            .collect();
        for c in &impact.changed {
            if let Some(value) = self.cached(c.node) {
                out.push(self.cell_edit(c.node, NewValue::Result { value }));
            }
        }
        out
    }
}

fn set_constant(
    model: &mut Model<'static>,
    i: u32,
    r: i32,
    c: i32,
    v: &Value,
) -> Result<(), Error> {
    match v {
        Value::Number(n) => model.update_cell_with_number(i, r, c, *n),
        Value::Text(t) => model.update_cell_with_text(i, r, c, t),
        Value::Bool(b) => model.update_cell_with_bool(i, r, c, *b),
        Value::Error(e) => model.set_user_input(i, r, c, e.clone()),
        Value::Empty => model.set_user_input(i, r, c, String::new()),
    }
    .map_err(unavailable)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn qualifies_unqualified_references() {
        assert_eq!(
            qualify("A1+Sheet2!B2+SUM(C1:C3)", "利润表"),
            "'利润表'!A1+Sheet2!B2+SUM('利润表'!C1:C3)"
        );
        assert_eq!(qualify("SUM(A:A)", "it's"), "SUM('it''s'!A:A)");
    }
}
