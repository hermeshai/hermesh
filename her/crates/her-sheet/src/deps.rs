//! The dependency graph over formula cells (design §5.5): which cells each formula reads, which
//! formulas read a cell, the cycles, and where to cut them for iteration (§5.6.2).
//!
//! References are found in the formula text: single cells, ranges, whole rows and columns, other
//! sheets, names. Dynamic references (OFFSET, INDIRECT) are invisible here; formulas that use
//! them are marked so impact can allow for them.

use std::collections::BTreeMap;
use std::collections::BTreeSet;
use std::collections::HashMap;

use crate::book::Book;
use crate::cellref::CellRef;
use crate::cellref::MAX_COL;
use crate::cellref::MAX_ROW;
use crate::cellref::parse_col;
use crate::formula::Tok;
use crate::formula::tokenize;

/// A cell of the workbook: sheet index and address.
pub type Node = (usize, CellRef);

/// A rectangle on a sheet that a formula reads.
#[derive(Clone, Copy, Debug, PartialEq)]
pub struct Area {
    pub sheet: usize,
    pub first: CellRef,
    pub last: CellRef,
}

impl Area {
    /// Whether the area holds a cell of its sheet.
    pub fn contains(&self, at: CellRef) -> bool {
        at.row >= self.first.row && at.row <= self.last.row && at.col >= self.first.col && at.col <= self.last.col
    }
}

const DYNAMIC: &[&str] = &["OFFSET", "INDIRECT"];

#[derive(Default)]
pub struct Graph {
    /// Formula cell → the formula cells it reads.
    pub reads: HashMap<Node, Vec<Node>>,
    /// Formula cell → the formula cells that read it.
    pub read_by: HashMap<Node, Vec<Node>>,
    /// Formula cells whose references are not all visible in the text.
    pub dynamic: BTreeSet<Node>,
    /// What each formula reads, as written: for finding the formulas that read a cell that
    /// holds no formula (an input).
    pub areas: HashMap<Node, Vec<Area>>,
}

fn cell_of(s: &str) -> Option<CellRef> {
    CellRef::parse(&s.replace('$', ""))
}

fn sheet_of(prefix: &str, book: &Book) -> Option<usize> {
    let p = prefix.trim_end_matches('!');
    if p.starts_with('[') || p.starts_with("'[") {
        return None; // another workbook
    }
    let p = p.trim_matches('\'').replace("''", "'");
    book.sheets
        .iter()
        .position(|s| s.name.eq_ignore_ascii_case(&p))
}

/// The areas a formula on `sheet` reads, and whether it uses a dynamic reference.
pub fn areas(formula: &str, sheet: usize, book: &Book) -> (Vec<Area>, bool) {
    let mut out = Vec::new();
    let dynamic = areas_into(formula, sheet, book, 0, &mut out);
    (out, dynamic)
}

fn areas_into(formula: &str, sheet: usize, book: &Book, depth: u32, out: &mut Vec<Area>) -> bool {
    let Ok(toks) = tokenize(formula) else {
        return true;
    };
    let mut dynamic = false;
    let mut i = 0;
    while i < toks.len() {
        let (target, at) = match &toks[i] {
            Tok::SheetPrefix(p) => (sheet_of(p, book), i + 1),
            _ => (Some(sheet), i),
        };
        let Some(tok) = toks.get(at) else { break };
        let second = match toks.get(at + 1) {
            Some(t) if t.text() == ":" => toks.get(at + 2),
            _ => None,
        };
        let mut used = 1;
        let rect = match (tok, second) {
            (Tok::Cell(a), Some(Tok::Cell(z))) => {
                used = 3;
                cell_of(a).zip(cell_of(z))
            }
            (Tok::Cell(a), _) => cell_of(a).map(|a| (a, a)),
            (Tok::Col(a), Some(Tok::Col(z))) => {
                used = 3;
                parse_col(&a.replace('$', ""))
                    .zip(parse_col(&z.replace('$', "")))
                    .map(|(a, z)| (CellRef::new(1, a), CellRef::new(MAX_ROW, z)))
            }
            (Tok::Row(a), Some(Tok::Row(z))) => {
                used = 3;
                let (a, z) = (
                    a.replace('$', "").parse().ok(),
                    z.replace('$', "").parse().ok(),
                );
                a.zip(z)
                    .map(|(a, z)| (CellRef::new(a, 1), CellRef::new(z, MAX_COL)))
            }
            (Tok::Bracket(_), _) => {
                dynamic = true;
                None
            }
            (Tok::Func(f), _) => {
                let f = f.to_uppercase();
                dynamic |= DYNAMIC.contains(&f.trim_start_matches("_XLFN."));
                None
            }
            (Tok::Name(n), _) if depth < 8 => {
                let scoped = book
                    .info
                    .names
                    .iter()
                    .find(|d| d.name.eq_ignore_ascii_case(n) && d.scope == Some(sheet));
                let global = book
                    .info
                    .names
                    .iter()
                    .find(|d| d.name.eq_ignore_ascii_case(n) && d.scope.is_none());
                if let Some(d) = scoped.or(global) {
                    dynamic |= areas_into(&d.formula, sheet, book, depth + 1, out);
                }
                None
            }
            _ => None,
        };
        if let (Some(s), Some((a, z))) = (target, rect) {
            out.push(Area {
                sheet: s,
                first: CellRef::new(a.row.min(z.row), a.col.min(z.col)),
                last: CellRef::new(a.row.max(z.row), a.col.max(z.col)),
            });
        }
        i = at + used;
    }
    dynamic
}

impl Graph {
    pub fn build(book: &Book) -> Self {
        let mut g = Graph::default();
        for (s, sheet) in book.sheets.iter().enumerate() {
            for (at, f) in &sheet.formulas {
                let expanded = crate::tables::expand(f, s, *at, book);
                let (areas, dynamic) = areas(&expanded, s, book);
                if dynamic {
                    g.dynamic.insert((s, *at));
                }
                let mut reads = BTreeSet::new();
                for a in &areas {
                    let Some(target) = book.sheets.get(a.sheet) else {
                        continue;
                    };
                    let range = CellRef::new(a.first.row, 0)..=CellRef::new(a.last.row, u32::MAX);
                    for (cell, _) in target.formulas.range(range) {
                        if cell.col >= a.first.col && cell.col <= a.last.col {
                            reads.insert((a.sheet, *cell));
                        }
                    }
                }
                for r in &reads {
                    g.read_by.entry(*r).or_default().push((s, *at));
                }
                g.reads.insert((s, *at), reads.into_iter().collect());
                g.areas.insert((s, *at), areas);
            }
        }
        g
    }

    /// The formulas that read a cell, whether or not it holds a formula itself.
    pub fn readers(&self, n: Node) -> Vec<Node> {
        if self.reads.contains_key(&n) {
            return self.read_by.get(&n).cloned().unwrap_or_default();
        }
        let mut out: Vec<Node> = self
            .areas
            .iter()
            .filter(|(_, areas)| {
                areas.iter().any(|a| {
                    a.sheet == n.0 && a.contains(n.1)
                })
            })
            .map(|(f, _)| *f)
            .collect();
        out.sort();
        out
    }

    /// Every formula downstream of `from` (not including `from` itself unless in a cycle).
    pub fn downstream(&self, from: &[Node]) -> BTreeSet<Node> {
        let mut seen = BTreeSet::new();
        let mut todo: Vec<Node> = Vec::new();
        for n in from {
            for m in self.readers(*n) {
                if seen.insert(m) {
                    todo.push(m);
                }
            }
        }
        while let Some(n) = todo.pop() {
            for m in self.read_by.get(&n).into_iter().flatten() {
                if seen.insert(*m) {
                    todo.push(*m);
                }
            }
        }
        seen
    }

    /// The components that contain a cycle (Tarjan, iterative), limited to `within` if given.
    pub fn cycles(&self, within: Option<&BTreeSet<Node>>) -> Vec<Vec<Node>> {
        let nodes: Vec<Node> = match within {
            Some(w) => w.iter().copied().collect(),
            None => {
                let mut v: Vec<Node> = self.reads.keys().copied().collect();
                v.sort();
                v
            }
        };
        let ok = |n: &Node| within.is_none_or(|w| w.contains(n));
        let succ = |v: &Node| -> Vec<Node> {
            self.reads
                .get(v)
                .map(|s| s.iter().copied().filter(ok).collect())
                .unwrap_or_default()
        };
        let mut index: HashMap<Node, usize> = HashMap::new();
        let mut low: HashMap<Node, usize> = HashMap::new();
        let mut on = BTreeSet::new();
        let mut stack = Vec::new();
        let mut out = Vec::new();
        let mut next = 0;
        for &root in &nodes {
            if index.contains_key(&root) {
                continue;
            }
            let mut work = vec![(root, 0usize)];
            index.insert(root, next);
            low.insert(root, next);
            next += 1;
            stack.push(root);
            on.insert(root);
            while let Some((v, i)) = work.pop() {
                let s = succ(&v);
                if i < s.len() {
                    work.push((v, i + 1));
                    let w = s[i];
                    if let std::collections::hash_map::Entry::Vacant(e) = index.entry(w) {
                        e.insert(next);
                        low.insert(w, next);
                        next += 1;
                        stack.push(w);
                        on.insert(w);
                        work.push((w, 0));
                    } else if on.contains(&w) {
                        low.insert(v, low[&v].min(index[&w]));
                    }
                    continue;
                }
                if let Some(&(p, _)) = work.last() {
                    low.insert(p, low[&p].min(low[&v]));
                }
                if low[&v] == index[&v] {
                    let mut comp = Vec::new();
                    loop {
                        let w = stack.pop().unwrap();
                        on.remove(&w);
                        comp.push(w);
                        if w == v {
                            break;
                        }
                    }
                    if comp.len() > 1 || succ(&v).contains(&v) {
                        comp.sort();
                        out.push(comp);
                    }
                }
            }
        }
        out
    }

    /// A feedback set: cells whose removal leaves no cycle. Greedy: in each cycle remove the cell
    /// with the most in×out edges inside it, until none is left.
    pub fn feedback(&self, cycles: &[Vec<Node>]) -> Vec<Node> {
        let mut f = Vec::new();
        let mut todo: Vec<BTreeSet<Node>> =
            cycles.iter().map(|c| c.iter().copied().collect()).collect();
        while let Some(set) = todo.pop() {
            let mut indeg: BTreeMap<Node, usize> = BTreeMap::new();
            for v in &set {
                for w in self.reads[v].iter().filter(|w| set.contains(w)) {
                    *indeg.entry(*w).or_default() += 1;
                }
            }
            let pick = *set
                .iter()
                .max_by_key(|v| {
                    let out = self.reads[*v].iter().filter(|w| set.contains(w)).count();
                    (
                        indeg.get(*v).copied().unwrap_or(0) * out,
                        std::cmp::Reverse(**v),
                    )
                })
                .unwrap();
            f.push(pick);
            let mut rest = set;
            rest.remove(&pick);
            todo.extend(
                self.cycles(Some(&rest))
                    .into_iter()
                    .map(|c| c.into_iter().collect()),
            );
        }
        f.sort();
        f
    }
}
