//! In-memory view of the session log: entries by id, file order, and the current leaf.

use her_llm::Timestamp;
use std::collections::HashMap;

use crate::store::Entry;
use crate::store::EntryKind;

/// Entries are kept in file order, with an index by id: a long session has thousands, and
/// a hash table of whole entries plus a list of their ids cost about twice the memory.
#[derive(Default, Debug)]
pub struct History {
    entries: Vec<Entry>,
    /// Where each id was last seen in `entries`.
    index: HashMap<String, usize>,
    leaf: Option<String>,
    /// Positions of the entries from the root to the leaf. Every request needs the branch, and
    /// walking the parents of thousands of entries each time cost more than the request;
    /// it is worked out on load and extended by each append, which continues the leaf.
    branch: Vec<usize>,
    /// The walk was cut short by a cycle (a corrupt log): appends walk again.
    cyclic: bool,
    /// Counts the walks. Between two walks the branch only grows at the end.
    generation: u64,
}

impl History {
    /// The leaf is the last entry: branching only ever appends.
    pub fn from_entries(list: Vec<Entry>) -> Self {
        let mut history = History {
            index: HashMap::with_capacity(list.len()),
            ..History::default()
        };
        for (i, entry) in list.iter().enumerate() {
            history.index.insert(entry.id.clone(), i);
        }
        history.leaf = list.last().map(|entry| entry.id.clone());
        history.entries = list;
        (history.branch, history.cyclic) = history.walk();
        history.generation = 1;
        history
    }

    pub fn new_id(&self) -> String {
        loop {
            let id = format!("{:08x}", rand::random::<u32>());
            if !self.index.contains_key(&id) {
                return id;
            }
        }
    }

    pub fn append(&mut self, id: String, kind: EntryKind, ts: Timestamp) -> Entry {
        let entry = Entry {
            id: id.clone(),
            parent: self.leaf.clone(),
            ts,
            kind,
        };
        let position = self.entries.len();
        // A new id continues the branch. One used before changes what its children resolve
        // to, and so does one the root names as its missing parent.
        let fresh = self.index.insert(id.clone(), position).is_none();
        let reparents = self.branch.first().is_some_and(|&root| self.entries[root].parent.as_deref() == Some(id.as_str()));
        self.entries.push(entry.clone());
        self.leaf = Some(id);
        if fresh && !reparents && !self.cyclic {
            self.branch.push(position);
        } else {
            (self.branch, self.cyclic) = self.walk();
            self.generation += 1;
        }
        entry
    }

    /// Goes back to `parent`, an earlier entry of the branch, with a branch entry that continues
    /// from it: the leaf is still the last entry, and what came after `parent` stays in the log,
    /// off the branch.
    pub fn rewind(&mut self, id: String, parent: Option<String>, before_first: bool, ts: Timestamp) -> Entry {
        let entry = Entry {
            id: id.clone(),
            parent,
            ts,
            kind: EntryKind::Branch { before_first },
        };
        self.index.insert(id.clone(), self.entries.len());
        self.entries.push(entry.clone());
        self.leaf = Some(id);
        (self.branch, self.cyclic) = self.walk();
        self.generation += 1;
        entry
    }

    pub fn leaf(&self) -> Option<&str> {
        self.leaf.as_deref()
    }

    pub fn len(&self) -> usize {
        self.entries.len()
    }

    pub fn is_empty(&self) -> bool {
        self.entries.is_empty()
    }

    /// The entry with this id; with several, the last one.
    fn get(&self, id: &str) -> Option<&Entry> {
        self.index.get(id).map(|&i| &self.entries[i])
    }

    /// Entries from the root to the current leaf.
    pub fn branch(&self) -> Vec<&Entry> {
        self.branch.iter().map(|&i| &self.entries[i]).collect()
    }

    /// The entries of [`Self::branch`] from the leaf back to the root.
    pub fn branch_back(&self) -> impl Iterator<Item = &Entry> {
        self.branch.iter().rev().map(|&i| &self.entries[i])
    }

    /// Where the entries of [`Self::branch`] are, in file order, and the walk that found them:
    /// while it stays the same, the branch only grows at the end.
    pub fn branch_positions(&self) -> (&[usize], u64) {
        (&self.branch, self.generation)
    }

    /// Follows the parents from the leaf: the branch's positions, root first, and whether a
    /// cycle cut the walk short.
    fn walk(&self) -> (Vec<usize>, bool) {
        let mut out = Vec::new();
        let mut cursor = self.leaf.as_deref();
        while let Some(id) = cursor {
            let Some(&i) = self.index.get(id) else {
                break;
            };
            out.push(i);
            if out.len() > self.index.len() {
                out.reverse();
                return (out, true); // corrupt log with a cycle
            }
            cursor = self.entries[i].parent.as_deref();
        }
        out.reverse();
        (out, false)
    }

    /// All entries in file order. An id used more than once stands for its last entry.
    pub fn in_order(&self) -> impl Iterator<Item = &Entry> {
        self.entries.iter().filter_map(|entry| self.get(&entry.id))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn branch_follows_parents_from_the_last_entry() {
        let mut history = History::default();
        let a = history.append("a".into(), EntryKind::Branch { before_first: false }, 1);
        history.append("b".into(), EntryKind::Branch { before_first: false }, 2);
        // Simulate a rewind: a new entry whose parent is `a`.
        let mut rewound = History::from_entries(vec![
            a.clone(),
            Entry {
                id: "b".into(),
                parent: Some("a".into()),
                ts: 2,
                kind: EntryKind::Branch { before_first: false },
            },
            Entry {
                id: "c".into(),
                parent: Some("a".into()),
                ts: 3,
                kind: EntryKind::Branch { before_first: false },
            },
        ]);
        let ids: Vec<&str> = rewound.branch().iter().map(|e| e.id.as_str()).collect();
        assert_eq!(ids, vec!["a", "c"]);
        let d = rewound.append("d".into(), EntryKind::Branch { before_first: false }, 4);
        assert_eq!(d.parent.as_deref(), Some("c"));
        assert_eq!(history.branch().len(), 2);
        let back: Vec<&str> = rewound.branch_back().map(|e| e.id.as_str()).collect();
        assert_eq!(back, vec!["d", "c", "a"]);
    }

    #[test]
    fn a_rewind_continues_from_the_entry_it_names_and_keeps_the_rest_in_the_log() {
        let filler = || EntryKind::Branch { before_first: false };
        let mut history = History::default();
        for (ts, id) in ["a", "b", "c"].into_iter().enumerate() {
            history.append(id.into(), filler(), ts as i64);
        }
        let (_, before) = history.branch_positions();
        let marker = history.rewind("r".into(), Some("a".into()), true, 9);
        assert_eq!((marker.parent.as_deref(), marker.kind), (Some("a"), EntryKind::Branch { before_first: true }));
        assert_eq!(history.leaf(), Some("r"));
        let (_, after) = history.branch_positions();
        assert_ne!(before, after, "what measures the branch starts over");
        let ids = |h: &History| h.branch().iter().map(|e| e.id.clone()).collect::<Vec<_>>();
        assert_eq!(ids(&history), ["a", "r"]);
        // New entries continue the new branch.
        let next = history.append("d".into(), filler(), 10);
        assert_eq!(next.parent.as_deref(), Some("r"));
        assert_eq!(ids(&history), ["a", "r", "d"]);
        // The log has everything, and reads back the same.
        let order: Vec<Entry> = history.in_order().cloned().collect();
        assert_eq!(order.iter().map(|e| e.id.as_str()).collect::<Vec<_>>(), ["a", "b", "c", "r", "d"]);
        assert_eq!(ids(&History::from_entries(order)), ["a", "r", "d"]);
    }

    #[test]
    fn a_kind_from_a_later_version_does_not_cut_the_branch() {
        let lines = [
            r#"{"id":"a","parent":null,"ts":1,"type":"branch"}"#,
            r#"{"id":"b","parent":"a","ts":2,"type":"from_a_later_version","x":1}"#,
            r#"{"id":"c","parent":"b","ts":3,"type":"branch"}"#,
        ];
        let entries = lines.iter().map(|line| serde_json::from_str::<Entry>(line).unwrap()).collect();
        let history = History::from_entries(entries);
        let ids: Vec<&str> = history.branch().iter().map(|e| e.id.as_str()).collect();
        assert_eq!(ids, vec!["a", "b", "c"]);
    }

    /// The branch as it was worked out before it was kept: by following parents from the leaf.
    fn walked<'a>(entries: &'a [Entry], leaf: Option<&str>) -> Vec<&'a Entry> {
        let mut map = HashMap::new();
        for entry in entries {
            map.insert(entry.id.as_str(), entry);
        }
        let mut out = Vec::new();
        let mut cursor = leaf;
        while let Some(id) = cursor {
            let Some(entry) = map.get(id) else { break };
            out.push(*entry);
            if out.len() > map.len() {
                break;
            }
            cursor = entry.parent.as_deref();
        }
        out.reverse();
        out
    }

    #[test]
    fn the_kept_branch_matches_a_walk_of_the_parents() {
        // Logs with rewinds, missing parents, repeated ids and cycles, then appends with new
        // and repeated ids.
        let mut seed = 0x2545_f491_4f6c_dd1du64;
        let mut next = |n: u64| {
            seed ^= seed << 13;
            seed ^= seed >> 7;
            seed ^= seed << 17;
            seed % n
        };
        for _ in 0..3000 {
            let len = next(30) as usize;
            let mut list = Vec::new();
            for i in 0..len {
                let id = format!("e{}", next(len as u64 + 3));
                let parent = match next(10) {
                    0 => None,
                    1 => Some(format!("e{}", next(len as u64 + 5))),
                    _ if i > 0 => Some(list.iter().map(|e: &Entry| e.id.clone()).nth(next(i as u64) as usize).unwrap()),
                    _ => None,
                };
                list.push(Entry {
                    id,
                    parent,
                    ts: i as i64,
                    kind: EntryKind::Branch { before_first: false },
                });
            }
            let mut history = History::from_entries(list.clone());
            let ids = |b: Vec<&Entry>| b.iter().map(|e| (e.id.clone(), e.ts)).collect::<Vec<_>>();
            assert_eq!(ids(history.branch()), ids(walked(&list, history.leaf())));
            for j in 0..5 {
                let id = if next(3) == 0 { format!("e{}", next(len as u64 + 3)) } else { format!("n{j}") };
                list.push(history.append(id, EntryKind::Branch { before_first: false }, 100 + j as i64));
                assert_eq!(ids(history.branch()), ids(walked(&list, history.leaf())));
            }
        }
    }

    #[test]
    fn a_repeated_id_stands_for_its_last_entry() {
        let entry = |id: &str, parent: Option<&str>, ts| Entry {
            id: id.into(),
            parent: parent.map(Into::into),
            ts,
            kind: EntryKind::Branch { before_first: false },
        };
        let history = History::from_entries(vec![entry("a", None, 1), entry("b", Some("a"), 2), entry("a", None, 3)]);
        let order: Vec<_> = history.in_order().map(|e| (e.id.as_str(), e.ts)).collect();
        assert_eq!(order, vec![("a", 3), ("b", 2), ("a", 3)]);
        assert_eq!(history.len(), 3);
        let branch: Vec<_> = history.branch().iter().map(|e| e.ts).collect();
        assert_eq!(branch, vec![3]);
    }
}
