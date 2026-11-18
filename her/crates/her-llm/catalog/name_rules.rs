// The rules of docs/kernel/custom-endpoint-design.md §6.3: spellings of a model name to look up
// in the public catalog. Included by src/names.rs and by build.rs, which indexes the alias table
// with the same rules, so they live in one place.

/// Routing suffixes of aggregators, which may stack (`:nitro:exacto`).
const ROUTER_SUFFIXES: &[&str] =
    &["free", "nitro", "exacto", "floor", "batch", "online", "thinking", "extended", "beta", "latest", "cheapest"];
/// Cross-region prefixes of Bedrock inference profiles.
const REGIONS: &[&str] = &["us", "eu", "apac", "ap", "au", "jp", "ca", "global", "us-gov", "in"];
/// Makers in Bedrock model ids (`anthropic.claude-…`).
const BEDROCK_MAKERS: &[&str] = &[
    "anthropic", "meta", "amazon", "mistral", "deepseek", "openai", "qwen", "cohere", "ai21", "writer", "moonshotai",
    "minimax", "google", "zai", "nvidia",
];
/// Quantization and file-format suffixes of self-hosted models.
const QUANT: &[&str] =
    &["fp8", "fp4", "int4", "int8", "nvfp4", "mxfp4", "awq", "gptq", "gguf", "mlx", "bf16", "fp16", "4bit", "8bit"];
/// Maker words some services glue to the front of a name (`openai-gpt-61-terra`).
const SQUASH_MAKERS: &[&str] = &[
    "openai", "anthropic", "google", "meta", "qwen", "alibaba", "deepseek", "zai", "zhipu", "moonshotai", "mistralai",
    "mistral", "xai", "x-ai", "minimax", "nvidia", "microsoft", "ibm", "amazon",
];
/// Suffixes that pick a variant of a model on a gateway rather than name another model. Only
/// tried after the name as given finds nothing, and only for a weak match.
const DECORATIONS: &[&str] = &[
    "thinking", "think", "nothinking", "nothink", "no-thinking", "search", "high", "low", "medium", "minimal", "xhigh",
    "max", "turbo", "tput", "versatile", "instant", "fast", "ultrafast", "batch", "all", "free", "tee", "preview",
    "latest", "exp",
];
/// Prefixes relays put on plan variants (`coding-glm-5.3`).
const RELAY_PREFIXES: &[&str] = &["coding-", "cc-"];
const CLAUDE_FAMILIES: &[&str] = &["sonnet", "opus", "haiku", "fable", "mythos"];

fn push(out: &mut Vec<String>, v: String) {
    if !v.is_empty() && !out.contains(&v) {
        out.push(v);
    }
}

fn strip_router_suffixes(mut s: &str) -> &str {
    while let Some((head, tail)) = s.rsplit_once(':') {
        if head.is_empty() || !ROUTER_SUFFIXES.contains(&tail) {
            break;
        }
        s = head;
    }
    s
}

/// The name before an `@` (Vertex dates, LM Studio quantizations, regions), and whether what
/// follows is a setting (`@effort:high`, `@thinking:on`), which only a weak match may drop.
fn split_at(s: &str) -> Option<(&str, bool)> {
    let (head, tail) = s.split_once('@')?;
    if head.is_empty() {
        return None;
    }
    Some((head, tail.starts_with("thinking") || tail.starts_with("effort")))
}

fn strip_prefix_word<'a>(s: &'a str, words: &[&str], sep: char) -> &'a str {
    for word in words {
        if let Some(rest) = s.strip_prefix(word).and_then(|r| r.strip_prefix(sep))
            && !rest.is_empty()
        {
            return rest;
        }
    }
    s
}

fn all_digits(s: &str) -> bool {
    !s.is_empty() && s.bytes().all(|b| b.is_ascii_digit())
}

/// `-v1:0`, `-1:0`, `:0`; a bare `-v2` only on a Bedrock-style name, since `deepseek-v3` is a
/// model's own name.
fn strip_version(s: &str, bedrock: bool) -> &str {
    if let Some((head, tail)) = s.rsplit_once(':')
        && all_digits(tail)
    {
        if let Some((h, v)) = head.rsplit_once('-')
            && (all_digits(v) || v.strip_prefix('v').is_some_and(all_digits))
        {
            return h;
        }
        return head;
    }
    if bedrock
        && let Some((head, v)) = s.rsplit_once("-v")
        && all_digits(v)
        && !head.is_empty()
    {
        return head;
    }
    s
}

/// `q4`, `q4_k`, `q4_k_m`, `q8_0`.
fn is_q_quant(s: &str) -> bool {
    let Some(rest) = s.strip_prefix('q') else { return false };
    let mut parts = rest.split('_');
    let Some(first) = parts.next() else { return false };
    if first.len() != 1 || !all_digits(first) {
        return false;
    }
    match (parts.next(), parts.next(), parts.next()) {
        (None, _, _) => true,
        (Some(d), None, _) if all_digits(d) && d.len() == 1 => true,
        (Some("k"), None, _) => true,
        (Some("k"), Some("s" | "m"), None) => true,
        _ => false,
    }
}

fn strip_quant(mut s: &str) -> &str {
    'next: loop {
        for (at, c) in s.char_indices().rev() {
            if !matches!(c, '-' | '_' | '@' | '.') || at == 0 {
                continue;
            }
            let tail = &s[at + 1..];
            if QUANT.contains(&tail) || is_q_quant(tail) {
                s = &s[..at];
                continue 'next;
            }
            // Only `q4_k_m`-style tokens hold a separator, an underscore.
            if c != '_' {
                break;
            }
        }
        return s;
    }
}

fn between_digits(s: &str, from: u8, to: u8, keep: impl Fn(&[u8], usize) -> bool) -> String {
    let bytes = s.as_bytes();
    let mut out = Vec::with_capacity(bytes.len());
    for (i, &b) in bytes.iter().enumerate() {
        let swap = b == from
            && i > 0
            && i + 1 < bytes.len()
            && bytes[i - 1].is_ascii_digit()
            && bytes[i + 1].is_ascii_digit()
            && keep(bytes, i);
        out.push(if swap { to } else { b });
    }
    String::from_utf8(out).unwrap_or_else(|_| s.to_string())
}

/// `-` between digits becomes `.` unless four or more digits follow (a date).
fn dash_to_dot(s: &str) -> String {
    between_digits(s, b'-', b'.', |bytes, i| bytes[i + 1..].iter().take_while(|b| b.is_ascii_digit()).count() < 4)
}

/// `claude-4.5-sonnet` → `claude-sonnet-4.5`.
fn claude_reorder(s: &str) -> Option<String> {
    let at = s.find("claude-")?;
    let (before, after) = (&s[..at], &s[at + "claude-".len()..]);
    let version_len = after.bytes().take_while(|b| b.is_ascii_digit() || *b == b'.' || *b == b'-').count();
    let version = after[..version_len].trim_end_matches('-');
    if version.is_empty() || !version.as_bytes()[0].is_ascii_digit() {
        return None;
    }
    let rest = &after[version.len()..];
    let rest = rest.strip_prefix('-')?;
    let family = CLAUDE_FAMILIES.iter().find(|f| rest.starts_with(**f))?;
    let tail = &rest[family.len()..];
    Some(format!("{before}claude-{family}-{version}{tail}"))
}

/// The spellings of a name to look up, most specific first (steps 1–10 of §6.3).
pub fn variants(id: &str) -> Vec<String> {
    let lower = id.trim().to_ascii_lowercase();
    let mut out = Vec::new();
    let parts: Vec<&str> = lower.split('/').collect();
    for i in 0..parts.len() {
        push(&mut out, parts[i..].join("/"));
    }
    let tails = out.clone();
    for tail in tails {
        let mut t = strip_router_suffixes(&tail).to_string();
        push(&mut out, t.clone());
        if let Some((head, false)) = split_at(&t) {
            t = head.to_string();
            push(&mut out, t.clone());
        }
        let before = t.clone();
        t = strip_prefix_word(&t, REGIONS, '.').to_string();
        push(&mut out, t.clone());
        t = strip_prefix_word(&t, BEDROCK_MAKERS, '.').to_string();
        push(&mut out, t.clone());
        t = strip_version(&t, t != before).to_string();
        push(&mut out, t.clone());
        t = strip_quant(&t).to_string();
        push(&mut out, t.clone());
        if let Some((name, tag)) = t.split_once(':')
            && !name.is_empty()
            && !tag.is_empty()
            && !tag.contains(':')
        {
            push(&mut out, if tag == "latest" { name.to_string() } else { format!("{name}-{tag}") });
        }
        push(&mut out, between_digits(&t, b'p', b'.', |_, _| true));
        if let Some(reordered) = claude_reorder(&t) {
            push(&mut out, between_digits(&reordered, b'.', b'-', |_, _| true));
            push(&mut out, reordered);
        }
        push(&mut out, between_digits(&t, b'.', b'-', |_, _| true));
        push(&mut out, dash_to_dot(&t));
    }
    out
}

/// A name without its maker word and its punctuation (step 11): `claude-opus4-7` and
/// `claude-opus-4-7` meet here. Only a unique result counts.
pub fn squash(id: &str) -> String {
    let lower = id.trim().to_ascii_lowercase();
    let mut s = lower.rsplit('/').next().unwrap_or(&lower);
    s = strip_prefix_word(s, REGIONS, '.');
    for sep in ['.', '-', '_'] {
        s = strip_prefix_word(s, SQUASH_MAKERS, sep);
    }
    s = strip_version(s, true);
    s.chars().filter(char::is_ascii_alphanumeric).collect()
}

/// The name before a tail shaped like `shape`: `D` is a digit, anything else itself.
fn strip_shape<'a>(s: &'a str, shape: &str) -> Option<&'a str> {
    let at = s.len().checked_sub(shape.len()).filter(|at| *at > 0)?;
    let tail = s.get(at..)?;
    let fits = tail.bytes().zip(shape.bytes()).all(|(b, p)| if p == b'D' { b.is_ascii_digit() } else { b == p });
    fits.then(|| &s[..at])
}

/// Date shapes a name may end with: `-2024-08-06`, `-10-2025`, `-05-20`, `-20250929`, `-260428`,
/// `-0613`.
const DATE_SHAPES: &[&str] = &["-20DD-DD-DD", "-DD-DDDD", "-DD-DD", "-20DDDDDD", "-DDDDDD", "-DDDD"];

/// Weak spellings (step 12): without a date, a decoration, a relay's prefix, a setting after
/// `@` or any `:tag`. A match through one of these lends only what the model can see and hold.
pub fn loose(id: &str) -> Vec<String> {
    let mut out = Vec::new();
    for v in variants(id) {
        let mut add = |w: &str| {
            if w != v {
                push(&mut out, w.to_string());
            }
        };
        for shape in DATE_SHAPES {
            if let Some(head) = strip_shape(&v, shape) {
                add(head);
            }
        }
        if let Some((head, tail)) = v.rsplit_once('-')
            && !head.is_empty()
            && DECORATIONS.contains(&tail)
        {
            add(head);
        }
        if let Some((head, tail)) = v.rsplit_once("-no-")
            && tail == "thinking"
        {
            add(head);
        }
        for prefix in RELAY_PREFIXES {
            if let Some(rest) = v.strip_prefix(prefix) {
                add(rest);
            }
        }
        if let Some((head, _)) = split_at(&v) {
            add(head);
        }
        if let Some((head, tail)) = v.rsplit_once(':')
            && !head.is_empty()
            && !tail.contains('/')
        {
            add(head);
        }
    }
    out
}
