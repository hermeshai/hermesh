//! BM25 over tool metadata, for `tool_search`. Exact names rank first.

use std::collections::HashMap;

const K1: f64 = 1.2;
const B: f64 = 0.75;

/// Words that say nothing about what a tool does.
const STOP_WORDS: &[&str] = &[
    "a", "an", "and", "any", "are", "as", "at", "be", "by", "can", "do", "for", "from", "how", "i", "in", "is", "it",
    "me", "my", "of", "on", "or", "please", "that", "the", "this", "to", "use", "using", "via", "want", "what",
    "with", "you", "your",
];

fn is_stop_word(word: &str) -> bool {
    STOP_WORDS.contains(&word)
}

/// What is searched for one tool.
pub struct SearchDoc {
    /// The name the model uses, e.g. `mcp__github__create_issue`.
    pub qualified: String,
    pub server: String,
    /// The name on the server.
    pub tool: String,
    pub title: Option<String>,
    pub description: String,
    /// Parameter names and descriptions.
    pub params: Vec<(String, String)>,
}

pub struct SearchIndex {
    docs: Vec<Indexed>,
    /// Documents containing each term.
    df: HashMap<String, usize>,
    avg_len: f64,
}

struct Indexed {
    terms: HashMap<String, u32>,
    len: usize,
    /// Lowercased names a query word may name exactly.
    names: Vec<String>,
}

/// Lowercase words: split on anything but letters and digits, and at camelCase boundaries.
pub fn tokenize(text: &str) -> Vec<String> {
    let mut out = Vec::new();
    let mut word = String::new();
    let mut prev_lower = false;
    for c in text.chars() {
        if c.is_alphanumeric() {
            if c.is_uppercase() && prev_lower && !word.is_empty() {
                out.push(std::mem::take(&mut word));
            }
            prev_lower = c.is_lowercase() || c.is_ascii_digit();
            word.extend(c.to_lowercase());
        } else {
            if !word.is_empty() {
                out.push(std::mem::take(&mut word));
            }
            prev_lower = false;
        }
    }
    if !word.is_empty() {
        out.push(word);
    }
    out
}

impl SearchIndex {
    pub fn new(docs: &[SearchDoc]) -> SearchIndex {
        let mut indexed = Vec::with_capacity(docs.len());
        let mut df: HashMap<String, usize> = HashMap::new();
        for doc in docs {
            let mut text = format!("{} {} {} {}", doc.qualified, doc.tool, doc.server, doc.description);
            if let Some(title) = &doc.title {
                text.push(' ');
                text.push_str(title);
            }
            for (name, description) in &doc.params {
                text.push(' ');
                text.push_str(name);
                text.push(' ');
                text.push_str(description);
            }
            let words: Vec<String> = tokenize(&text).into_iter().filter(|w| !is_stop_word(w)).collect();
            let mut terms: HashMap<String, u32> = HashMap::new();
            for word in &words {
                *terms.entry(word.clone()).or_default() += 1;
            }
            for term in terms.keys() {
                *df.entry(term.clone()).or_default() += 1;
            }
            let server_tool = format!("{}__{}", doc.server, doc.tool);
            indexed.push(Indexed {
                terms,
                len: words.len(),
                names: vec![
                    doc.qualified.to_lowercase(),
                    doc.tool.to_lowercase(),
                    server_tool.to_lowercase(),
                ],
            });
        }
        let avg_len = if indexed.is_empty() {
            1.0
        } else {
            indexed.iter().map(|d| d.len as f64).sum::<f64>() / indexed.len() as f64
        };
        SearchIndex {
            docs: indexed,
            df,
            avg_len: avg_len.max(1.0),
        }
    }

    pub fn len(&self) -> usize {
        self.docs.len()
    }

    pub fn is_empty(&self) -> bool {
        self.docs.is_empty()
    }

    /// Indices of the best matches, best first, at most `limit`.
    pub fn search(&self, query: &str, limit: usize) -> Vec<usize> {
        let words: Vec<String> = query.split_whitespace().map(str::to_lowercase).collect();
        let terms: Vec<String> = tokenize(query).into_iter().filter(|w| !is_stop_word(w)).collect();
        let n = self.docs.len() as f64;
        let mut scored: Vec<(usize, f64)> = Vec::new();
        for (i, doc) in self.docs.iter().enumerate() {
            let exact = words.iter().any(|w| doc.names.contains(w));
            let mut score = 0.0;
            for term in &terms {
                let Some(&tf) = doc.terms.get(term) else { continue };
                let df = *self.df.get(term).unwrap_or(&0) as f64;
                let idf = ((n - df + 0.5) / (df + 0.5) + 1.0).ln();
                let tf = f64::from(tf);
                score += idf * tf * (K1 + 1.0) / (tf + K1 * (1.0 - B + B * doc.len as f64 / self.avg_len));
            }
            if exact {
                score += 1_000.0;
            }
            if score > 0.0 {
                scored.push((i, score));
            }
        }
        scored.sort_by(|a, b| b.1.total_cmp(&a.1).then(a.0.cmp(&b.0)));
        scored.into_iter().take(limit).map(|(i, _)| i).collect()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn doc(server: &str, tool: &str, description: &str) -> SearchDoc {
        SearchDoc {
            qualified: format!("mcp__{server}__{tool}"),
            server: server.into(),
            tool: tool.into(),
            title: None,
            description: description.into(),
            params: vec![],
        }
    }

    #[test]
    fn tokenizes_identifiers() {
        assert_eq!(tokenize("getIssueByID mcp__github__create_issue"), vec![
            "get", "issue", "by", "id", "mcp", "github", "create", "issue"
        ]);
    }

    #[test]
    fn ranks_relevant_tools_and_exact_names_first() {
        let docs = vec![
            doc("github", "create_issue", "Create a new issue in a repository"),
            doc("github", "search_code", "Search code across repositories"),
            doc("calendar", "create_event", "Create a calendar event"),
            doc("weather", "forecast", "Weather forecast for a city"),
        ];
        let index = SearchIndex::new(&docs);
        assert_eq!(index.search("weather in paris", 5), vec![3]);
        let issue = index.search("open an issue", 5);
        assert_eq!(issue.first(), Some(&0));
        // A full name wins even against better keyword matches.
        assert_eq!(index.search("mcp__calendar__create_event issue", 5)[0], 2);
        assert_eq!(index.search("search_code", 1), vec![1]);
        assert!(index.search("zzz", 5).is_empty());
    }
}
