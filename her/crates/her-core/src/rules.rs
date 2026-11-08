//! Conservative shell command analysis for the permission policy.
//!
//! [`analyze`] finds every simple command a command line may run, including those behind
//! wrappers such as `sudo` or `xargs` and inside `sh -c`, `eval`, `trap` and `find -exec`
//! scripts, together with the directories each may run in. It works on the text alone and
//! errs towards finding more.

use her_protocol::CommandRule;
use her_protocol::RuleDecision;
use her_tools::env::normalize_lexically;
use std::path::Path;
use std::path::PathBuf;

/// A command split into simple commands, or `Complex` when it contains constructs whose
/// effect cannot be judged from the words (redirections, substitutions, variables, ...).
#[derive(Debug, PartialEq, Eq)]
pub enum Parsed {
    Simple(Vec<Vec<String>>),
    Complex,
}

pub fn parse_command(command: &str) -> Parsed {
    match parse_chain(command) {
        Some(chain) => Parsed::Simple(chain.into_iter().map(|(words, _)| words).collect()),
        None => Parsed::Complex,
    }
}

/// How a simple command is joined to the one after it.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Joiner {
    /// `&&`: the next command runs only if this one succeeded.
    And,
    /// `||`: the next command runs only if this one failed.
    Or,
    /// `|`: both run at once, each in a subshell of its own.
    Pipe,
    /// `;`, a newline or the end of the line.
    End,
}

/// The simple commands of a command line with the operator after each, or `None` when the
/// line is complex (see [`Parsed`]).
pub fn parse_chain(command: &str) -> Option<Vec<(Vec<String>, Joiner)>> {
    let mut segments: Vec<(Vec<String>, Joiner)> = Vec::new();
    let mut words: Vec<String> = Vec::new();
    let mut word = String::new();
    let mut in_word = false;
    let mut chars = command.chars().peekable();

    fn end_word(words: &mut Vec<String>, word: &mut String, in_word: &mut bool) {
        if *in_word {
            words.push(std::mem::take(word));
            *in_word = false;
        }
    }
    fn end_segment(segments: &mut Vec<(Vec<String>, Joiner)>, words: &mut Vec<String>, joiner: Joiner) {
        if !words.is_empty() {
            segments.push((std::mem::take(words), joiner));
        }
    }

    while let Some(c) = chars.next() {
        match c {
            '\'' => {
                in_word = true;
                loop {
                    match chars.next() {
                        Some('\'') => break,
                        Some(ch) => word.push(ch),
                        None => return None,
                    }
                }
            }
            '"' => {
                in_word = true;
                loop {
                    match chars.next() {
                        Some('"') => break,
                        Some('\\') => word.push(chars.next()?),
                        Some('$') | Some('`') => return None,
                        Some(ch) => word.push(ch),
                        None => return None,
                    }
                }
            }
            '\\' => match chars.next() {
                Some('\n') => {}
                Some(ch) => {
                    word.push(ch);
                    in_word = true;
                }
                None => return None,
            },
            '$' | '`' | '>' | '<' | '(' | ')' | '{' | '}' => return None,
            ';' | '\n' => {
                end_word(&mut words, &mut word, &mut in_word);
                end_segment(&mut segments, &mut words, Joiner::End);
            }
            '&' => {
                if chars.peek() == Some(&'&') {
                    chars.next();
                    end_word(&mut words, &mut word, &mut in_word);
                    end_segment(&mut segments, &mut words, Joiner::And);
                } else {
                    return None; // background job
                }
            }
            '|' => {
                let joiner = if chars.peek() == Some(&'|') {
                    chars.next();
                    Joiner::Or
                } else {
                    Joiner::Pipe
                };
                end_word(&mut words, &mut word, &mut in_word);
                end_segment(&mut segments, &mut words, joiner);
            }
            c if c.is_whitespace() => end_word(&mut words, &mut word, &mut in_word),
            other => {
                word.push(other);
                in_word = true;
            }
        }
    }
    end_word(&mut words, &mut word, &mut in_word);
    end_segment(&mut segments, &mut words, Joiner::End);
    if segments.iter().any(|(segment, _)| segment.first().is_some_and(|w| is_assignment(w))) {
        return None;
    }
    Some(segments)
}

fn is_assignment(word: &str) -> bool {
    match word.split_once('=') {
        Some((name, _)) => {
            !name.is_empty()
                && !name.starts_with(|c: char| c.is_ascii_digit())
                && name.chars().all(|c| c.is_ascii_alphanumeric() || c == '_')
        }
        None => false,
    }
}

/// Placeholder for a backtick substitution in a rough split.
pub const SUBSTITUTION: &str = "`…`";

/// Rough split of a complex command, used to look for rules, deletions and scripts. A
/// backtick substitution becomes segments of its own and leaves [`SUBSTITUTION`] behind,
/// so that `` rm -rf `pwd` `` keeps an operand.
pub fn rough_segments(command: &str) -> Vec<Vec<String>> {
    let mut outside = String::new();
    let mut inside: Option<String> = None;
    let mut parts = Vec::new();
    for c in command.chars() {
        match (c, inside.as_mut()) {
            ('`', None) => {
                inside = Some(String::new());
                outside.push_str(&format!(" {SUBSTITUTION} "));
            }
            ('`', Some(text)) => {
                parts.push(std::mem::take(text));
                inside = None;
            }
            (c, Some(text)) => text.push(c),
            (c, None) => outside.push(c),
        }
    }
    parts.extend(inside);
    parts.insert(0, outside);
    parts
        .iter()
        .flat_map(|part| part.split([';', '&', '|', '\n', '(', ')']))
        .map(|part| {
            part.split_whitespace()
                .map(|w| w.trim_matches(|c| c == '\'' || c == '"').to_string())
                .collect::<Vec<_>>()
        })
        .filter(|words| !words.is_empty())
        .collect()
}

pub fn rule_matches(rule: &CommandRule, words: &[String]) -> bool {
    !rule.prefix.is_empty() && rule.prefix.len() <= words.len() && rule.prefix[..] == words[..rule.prefix.len()]
}

pub fn first_match<'a>(rules: &'a [CommandRule], words: &[String], decision: RuleDecision) -> Option<&'a CommandRule> {
    rules.iter().find(|r| r.decision == decision && rule_matches(r, words))
}

const FIND_UNSAFE: &[&str] = &[
    "-exec", "-execdir", "-ok", "-okdir", "-delete", "-fprint", "-fprint0", "-fprintf", "-fls",
];

/// Commands that only read. Kept deliberately short; anything not listed needs approval.
/// `cd` changes nothing by itself; the analysis follows it for the commands after it.
pub fn is_read_only(words: &[String], trusted: bool) -> bool {
    let Some(name) = words.first() else {
        return false;
    };
    if name.contains('/') {
        return false; // ./ls could be anything
    }
    let args = &words[1..];
    match name.as_str() {
        "ls" | "pwd" | "cat" | "head" | "tail" | "wc" | "file" | "stat" | "du" | "echo" | "which" | "basename"
        | "dirname" | "realpath" | "uname" | "grep" | "cd" | "pushd" | "popd" => true,
        "date" => args.iter().all(|a| a.starts_with('+') || a == "-u" || a == "-R"),
        "tree" => !args.iter().any(|a| a.starts_with("-o")),
        "rg" => !args
            .iter()
            .any(|a| a == "--pre" || a.starts_with("--pre=") || a == "-z" || a == "--search-zip"),
        "find" => !args.iter().any(|a| FIND_UNSAFE.contains(&a.as_str())),
        "git" => trusted && git_read_only(args),
        _ => false,
    }
}

fn git_read_only(args: &[String]) -> bool {
    let Some(sub) = args.first() else {
        return false;
    };
    if args.iter().any(|a| a.starts_with("--output") || a == "--ext-diff") {
        return false;
    }
    match sub.as_str() {
        "status" | "diff" | "log" | "show" | "rev-parse" | "ls-files" | "blame" => true,
        "branch" => args[1..]
            .iter()
            .all(|a| matches!(a.as_str(), "-a" | "-r" | "-v" | "-vv" | "--list" | "--show-current")),
        _ => false,
    }
}

/// The program's name without its directory: `/usr/bin/git` gives `git`.
pub fn program_name(word: &str) -> &str {
    word.rsplit('/').next().filter(|name| !name.is_empty()).unwrap_or(word)
}

/// Scripts nested deeper than this are not followed; the command then cannot be checked.
const MAX_DEPTH: usize = 8;
/// More possible working directories than this count as unknown.
const MAX_CWDS: usize = 16;

/// Where commands run, for resolving the paths they name.
pub struct ShellContext<'a> {
    pub cwd: &'a Path,
    pub home: Option<&'a Path>,
    /// `CDPATH` is set, so `cd name` may go somewhere other than `./name`.
    pub cdpath: bool,
}

/// The directories a command may run in. A `cd` adds a possibility instead of replacing
/// one: it may fail, and it does not reach past a pipeline element or a subshell.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Cwds {
    Known(Vec<PathBuf>),
    Unknown,
}

impl Cwds {
    /// Where a successful change to `target` leads; `None` stands for a target only known
    /// when the command runs. `cdpath`: the shell's `cd` may consult `CDPATH`.
    fn moved_to(&self, target: Option<&str>, home: Option<&Path>, cdpath: bool) -> Cwds {
        let Some(target) = target.filter(|t| !(cdpath && !t.starts_with(['.', '/', '~']))) else {
            return Cwds::Unknown;
        };
        let anchored = target.starts_with('/') || target == "~" || target.starts_with("~/");
        match self {
            Cwds::Known(dirs) => {
                let mut moved = Vec::new();
                for dir in dirs {
                    let dir = resolve_in(dir, target, home);
                    if !moved.contains(&dir) {
                        moved.push(dir);
                    }
                }
                Cwds::Known(moved)
            }
            Cwds::Unknown if anchored => Cwds::Known(vec![resolve_in(Path::new("/"), target, home)]),
            Cwds::Unknown => Cwds::Unknown,
        }
    }

    fn add(&mut self, other: &Cwds) {
        match (&mut *self, other) {
            (Cwds::Known(dirs), Cwds::Known(more)) => {
                for dir in more {
                    if !dirs.contains(dir) {
                        dirs.push(dir.clone());
                    }
                }
                if dirs.len() > MAX_CWDS {
                    *self = Cwds::Unknown;
                }
            }
            _ => *self = Cwds::Unknown,
        }
    }
}

/// `raw` as a shell in `cwd` sees it: `~` expanded, relative paths joined to `cwd`, `.` and
/// `..` removed lexically.
pub fn resolve_in(cwd: &Path, raw: &str, home: Option<&Path>) -> PathBuf {
    let expanded = match home {
        Some(home) if raw == "~" => home.to_path_buf(),
        Some(home) if raw.starts_with("~/") => home.join(&raw[2..]),
        _ => PathBuf::from(raw),
    };
    normalize_lexically(&cwd.join(expanded))
}

/// The word itself when the shell does not change it: no `$`, backticks or `~user`.
fn literal(word: &str) -> Option<&str> {
    let tilde_user = word.starts_with('~') && word != "~" && !word.starts_with("~/");
    (!word.contains(['$', '`']) && !tilde_user).then_some(word)
}

/// A simple command that a command line may run: one of its own, one behind a wrapper
/// such as `sudo`, or one inside an `sh -c`, `eval`, `trap` or `find -exec` script.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Found {
    /// The words as written.
    pub words: Vec<String>,
    /// The command behind the wrappers: `sudo -u root rm x` gives `rm x`.
    pub inner: Vec<String>,
    /// Where `inner` may run.
    pub cwds: Cwds,
    /// `inner` gets more operands from its input, as behind `xargs`.
    pub reads_operands: bool,
    /// One of the simple commands of the command line itself.
    pub top_level: bool,
}

#[derive(Debug, Default)]
pub struct Analysis {
    /// The command line splits into simple commands (see [`parse_command`]).
    pub simple: bool,
    pub found: Vec<Found>,
    /// Scripts nest more deeply than the analysis follows.
    pub too_deep: bool,
}

pub fn analyze(command: &str, ctx: &ShellContext<'_>) -> Analysis {
    let mut analysis = Analysis::default();
    let mut cwds = Cwds::Known(vec![normalize_lexically(ctx.cwd)]);
    walk(command, &mut cwds, 0, ctx, &mut analysis);
    analysis
}

/// Walks a command line. `cwds` holds where it starts and, afterwards, everywhere it may
/// have moved to.
///
/// A command reached from a `cd` through `&&` alone runs only where that `cd` went. After
/// `;`, `||` or `|`, or in a rough split, the `cd` may have failed, been skipped or stayed
/// in a subshell, so the earlier directories remain possible too.
fn walk(command: &str, cwds: &mut Cwds, depth: usize, ctx: &ShellContext<'_>, out: &mut Analysis) {
    if depth > MAX_DEPTH {
        out.too_deep = true;
        return;
    }
    let Some(chain) = parse_chain(command) else {
        for words in &rough_segments(command) {
            match walk_segment(words, true, false, cwds, depth, ctx, out) {
                Moves::To(moved) | Moves::Maybe(moved) => cwds.add(&moved),
                Moves::Back | Moves::Stay => {}
            }
        }
        return;
    };
    out.simple |= depth == 0;
    // Where the next command runs if every command of this and-or list so far succeeded.
    let mut definite = cwds.clone();
    let mut only_and = true;
    for (words, joiner) in &chain {
        let here = if only_and { definite.clone() } else { cwds.clone() };
        match walk_segment(words, false, depth == 0, &here, depth, ctx, out) {
            Moves::To(moved) => {
                cwds.add(&moved);
                if only_and {
                    definite = moved;
                }
            }
            Moves::Maybe(moved) => {
                cwds.add(&moved);
                definite.add(&moved);
            }
            // Any directory visited so far.
            Moves::Back => definite = cwds.clone(),
            Moves::Stay => {}
        }
        match joiner {
            Joiner::And => {}
            Joiner::Or | Joiner::Pipe => only_and = false,
            Joiner::End => {
                only_and = true;
                definite = cwds.clone();
            }
        }
    }
}

/// Whether a script one level further down may be followed.
fn descend(depth: usize, out: &mut Analysis) -> bool {
    let allowed = depth < MAX_DEPTH;
    out.too_deep |= !allowed;
    allowed
}

/// How a simple command moves the shell that runs the commands after it.
enum Moves {
    Stay,
    /// To these directories, if it succeeds (`cd`, `pushd`).
    To(Cwds),
    /// Possibly to these directories (`eval`, `trap` and scripts in a rough split).
    Maybe(Cwds),
    /// Back to an earlier directory (`popd`).
    Back,
}

/// Records one simple command and follows the scripts it runs.
fn walk_segment(
    words: &[String],
    rough: bool,
    top_level: bool,
    cwds: &Cwds,
    depth: usize,
    ctx: &ShellContext<'_>,
    out: &mut Analysis,
) -> Moves {
    let unwrapped = unwrap(words);
    let mut here = cwds.clone();
    // A wrapper's directory option applies for sure: the command does not run otherwise.
    for dir in &unwrapped.chdirs {
        here = here.moved_to(dir.as_deref(), ctx.home, false);
    }
    let inner = unwrapped.words;
    out.found.push(Found {
        words: words.to_vec(),
        inner: inner.clone(),
        cwds: here.clone(),
        reads_operands: unwrapped.reads_operands,
        top_level,
    });
    let Some(first) = inner.first() else {
        return Moves::Stay;
    };
    match program_name(first) {
        "cd" | "pushd" => Moves::To(here.moved_to(cd_target(&inner, ctx.home).as_deref(), ctx.home, ctx.cdpath)),
        "popd" => Moves::Back,
        // A shell runs the script in a process of its own. In a rough split, the script's
        // later commands also show up as segments after this one, so its moves count.
        "sh" | "bash" | "zsh" | "dash" | "ksh" | "mksh" | "ash" | "fish" => match shell_script_index(&inner) {
            Some(index) => {
                let after = follow(&inner[index..], false, rough, &here, depth, ctx, out);
                if rough { Moves::Maybe(after) } else { Moves::Stay }
            }
            None => Moves::Stay,
        },
        // `eval` runs its arguments, joined, in this shell.
        "eval" => Moves::Maybe(follow(&inner[1..], true, rough, &here, depth, ctx, out)),
        // The action runs in this shell later: on exit, on a signal, or before each command
        // for `DEBUG`.
        "trap" => match trap_action_index(&inner) {
            Some(index) => Moves::Maybe(follow(&inner[index..], false, rough, &here, depth, ctx, out)),
            None => Moves::Stay,
        },
        "find" => {
            let (_, expression) = find_parts(&inner);
            for command in find_exec_commands(expression) {
                if descend(depth, out) {
                    walk_segment(command, rough, false, &here, depth + 1, ctx, out);
                }
            }
            Moves::Stay
        }
        _ => Moves::Stay,
    }
}

/// Follows a script that starts at `rest[0]` and returns every directory it may end in.
/// In a simple command line the script is that one word, or for `eval` all words joined;
/// in a rough split its words follow as they are.
fn follow(
    rest: &[String],
    joined: bool,
    rough: bool,
    cwds: &Cwds,
    depth: usize,
    ctx: &ShellContext<'_>,
    out: &mut Analysis,
) -> Cwds {
    let mut after = cwds.clone();
    if !descend(depth, out) {
        return after;
    }
    if rough {
        if let Moves::To(moved) | Moves::Maybe(moved) = walk_segment(rest, true, false, cwds, depth + 1, ctx, out) {
            after.add(&moved);
        }
    } else if joined {
        walk(&rest.join(" "), &mut after, depth + 1, ctx, out);
    } else if let Some(script) = rest.first() {
        walk(script, &mut after, depth + 1, ctx, out);
    }
    after
}

/// Where `cd` or `pushd` goes; `None` when only known at run time (`cd -`, `cd $X`).
fn cd_target(words: &[String], home: Option<&Path>) -> Option<String> {
    let mut args = words[1..].iter().map(String::as_str);
    let mut operands = Vec::new();
    let mut options_done = false;
    for arg in args.by_ref() {
        if !options_done && arg == "--" {
            options_done = true;
        } else if !options_done && arg.starts_with('-') && arg.len() > 1 {
            // -L, -P, -e, -@; `pushd -N` rotates the stack.
            if program_name(&words[0]) == "pushd" && arg[1..].chars().all(|c| c.is_ascii_digit()) {
                return None;
            }
        } else {
            operands.push(arg);
        }
    }
    match operands.as_slice() {
        [] if program_name(&words[0]) == "cd" => home.map(|home| home.display().to_string()),
        [target] if *target != "-" && !target.starts_with('+') => literal(target).map(str::to_string),
        _ => None,
    }
}

/// Index of the script in `sh -c SCRIPT` (also `-lc`, `-ec`, ...).
fn shell_script_index(words: &[String]) -> Option<usize> {
    let mut command_option = false;
    let mut i = 1;
    while let Some(word) = words.get(i) {
        if matches!(word.as_str(), "-o" | "+o" | "-O" | "+O" | "--rcfile" | "--init-file") {
            i += 2;
            continue;
        }
        if word.starts_with("--") {
            i += 1;
            continue;
        }
        if (word.starts_with('-') || word.starts_with('+')) && word.len() > 1 {
            command_option |= word.starts_with('-') && word.contains('c');
            i += 1;
            continue;
        }
        return command_option.then_some(i);
    }
    None
}

/// Index of the action in `trap [--] ACTION SIGNAL...`.
fn trap_action_index(words: &[String]) -> Option<usize> {
    let index = if words.get(1).is_some_and(|w| w == "--") { 2 } else { 1 };
    let action = words.get(index)?;
    // `trap -p`, `trap -l`, `trap - SIGNAL` (reset) and `trap 0 1` (reset) run nothing.
    let runs = !action.is_empty() && !action.starts_with('-') && !action.chars().all(|c| c.is_ascii_digit());
    runs.then_some(index)
}

/// The starting points and the expression of `find [-H|-L|-P] [POINT...] [EXPRESSION]`.
fn find_parts(words: &[String]) -> (Vec<&str>, &[String]) {
    let mut i = 1;
    while let Some(word) = words.get(i) {
        match word.as_str() {
            "-H" | "-L" | "-P" => i += 1,
            "-D" => i += 2,
            w if w.starts_with("-O") => i += 1,
            _ => break,
        }
    }
    let start = i;
    while words
        .get(i)
        .is_some_and(|w| !w.starts_with('-') && !matches!(w.as_str(), "(" | ")" | "!" | ","))
    {
        i += 1;
    }
    let points = if i > start {
        words[start..i].iter().map(String::as_str).collect()
    } else {
        vec!["."]
    };
    (points, words.get(i..).unwrap_or(&[]))
}

/// The commands of `-exec CMD ... ;` and of `-execdir`, `-ok` and `-okdir`.
fn find_exec_commands(expression: &[String]) -> Vec<&[String]> {
    let mut commands = Vec::new();
    let mut i = 0;
    while i < expression.len() {
        if matches!(expression[i].as_str(), "-exec" | "-execdir" | "-ok" | "-okdir") {
            let start = i + 1;
            let end = expression[start..]
                .iter()
                .position(|w| matches!(w.as_str(), ";" | "+" | r"\;"))
                .map_or(expression.len(), |p| start + p);
            commands.push(&expression[start..end]);
            i = end;
        }
        i += 1;
    }
    commands
}

struct Wrapper {
    name: &'static str,
    /// Options that take a separate value.
    with_value: &'static [&'static str],
    /// Options that set the directory the command runs in.
    chdir: &'static [&'static str],
    /// Options whose value is split into the command's first words (`env -S`).
    split: &'static [&'static str],
}

const fn wrapper(name: &'static str, with_value: &'static [&'static str]) -> Wrapper {
    Wrapper {
        name,
        with_value,
        chdir: &[],
        split: &[],
    }
}

/// Commands that run the command given in their arguments.
const WRAPPERS: &[Wrapper] = &[
    Wrapper {
        name: "sudo",
        with_value: &[
            "-u", "-g", "-h", "-p", "-C", "-D", "-R", "-r", "-T", "-t", "-U", "--user", "--group", "--host",
            "--prompt", "--close-from", "--chdir", "--chroot", "--role", "--type", "--command-timeout",
            "--other-user",
        ],
        chdir: &["-D", "--chdir"],
        split: &[],
    },
    Wrapper {
        name: "env",
        with_value: &["-u", "-C", "-S", "--unset", "--chdir", "--split-string"],
        chdir: &["-C", "--chdir"],
        split: &["-S", "--split-string"],
    },
    wrapper("doas", &["-u", "-C"]),
    wrapper("nice", &["-n", "--adjustment"]),
    wrapper("time", &["-f", "-o", "--format", "--output"]),
    wrapper("timeout", &["-s", "-k", "--signal", "--kill-after"]),
    wrapper("exec", &["-a"]),
    wrapper("stdbuf", &["-i", "-o", "-e"]),
    wrapper("watch", &["-n", "--interval"]),
    wrapper(
        "xargs",
        &[
            "-a", "-d", "-E", "-I", "-L", "-n", "-P", "-s", "--arg-file", "--delimiter", "--eof", "--replace",
            "--max-lines", "--max-args", "--max-procs", "--max-chars", "--process-slot-var",
        ],
    ),
    wrapper("nohup", &[]),
    wrapper("command", &[]),
    wrapper("builtin", &[]),
    wrapper("busybox", &[]),
    wrapper("toybox", &[]),
];

struct Unwrapped {
    words: Vec<String>,
    /// Directories the wrappers change to; `None` for one only known at run time.
    chdirs: Vec<Option<String>>,
    reads_operands: bool,
}

/// The command behind wrappers such as `sudo -u root`, `env A=1` or `xargs -0`, behind
/// leading variable assignments and behind shell keywords such as `then`.
fn unwrap(words: &[String]) -> Unwrapped {
    let mut words = words.to_vec();
    let mut chdirs = Vec::new();
    let mut reads_operands = false;
    while let Some(first) = words.first() {
        let keyword = matches!(
            first.as_str(),
            "!" | "{" | "if" | "then" | "else" | "elif" | "do" | "while" | "until"
        );
        if keyword || is_assignment(first) {
            words.remove(0);
            continue;
        }
        let Some(wrapper) = WRAPPERS.iter().find(|w| w.name == program_name(first)) else {
            break;
        };
        let mut split = None;
        let mut i = 1;
        while let Some(word) = words.get(i) {
            if word == "--" {
                i += 1;
                break;
            }
            if !word.starts_with('-') || word.len() == 1 {
                if wrapper.name == "env" && is_assignment(word) {
                    i += 1;
                    continue;
                }
                break;
            }
            if let Some((option, value)) = word.split_once('=').filter(|(option, _)| option.starts_with("--")) {
                if wrapper.chdir.contains(&option) {
                    chdirs.push(literal(value).map(str::to_string));
                }
                if wrapper.split.contains(&option) {
                    split = Some(value.to_string());
                }
                i += 1;
            } else if wrapper.with_value.contains(&word.as_str()) {
                let value = words.get(i + 1).map(String::as_str);
                if wrapper.chdir.contains(&word.as_str()) {
                    chdirs.push(value.and_then(literal).map(str::to_string));
                }
                if wrapper.split.contains(&word.as_str()) {
                    split = value.map(str::to_string);
                }
                i += 2;
            } else {
                // `env -C/tmp`: a short option with its value attached.
                if let Some(option) = wrapper.chdir.iter().find(|o| o.len() == 2 && word.starts_with(**o)) {
                    chdirs.push(literal(&word[option.len()..]).map(str::to_string));
                }
                i += 1;
            }
        }
        if wrapper.name == "timeout" {
            i += 1; // the duration
        }
        reads_operands |= wrapper.name == "xargs";
        let rest = words.get(i..).map(<[String]>::to_vec).unwrap_or_default();
        words = match split {
            Some(split) => split.split_whitespace().map(str::to_string).chain(rest).collect(),
            None => rest,
        };
    }
    Unwrapped {
        words,
        chdirs,
        reads_operands,
    }
}

/// The forms that deny and ask rules are matched against: the words as written, the command
/// behind its wrappers, that command without the program's directory (`/usr/bin/git` gives
/// `git`), and for git also without its global options such as `-C dir`.
pub fn rule_forms(found: &Found) -> Vec<Vec<String>> {
    let mut forms = vec![found.words.clone(), found.inner.clone()];
    if let Some(first) = found.inner.first() {
        let mut bare = found.inner.clone();
        bare[0] = program_name(first).to_string();
        if bare[0] == "git" {
            forms.push(strip_git_options(&bare));
        }
        forms.push(bare);
    }
    let mut unique: Vec<Vec<String>> = Vec::with_capacity(forms.len());
    for form in forms {
        if !unique.contains(&form) {
            unique.push(form);
        }
    }
    unique
}

/// Global options of git that take a separate value.
const GIT_VALUE_OPTIONS: &[&str] = &[
    "-C",
    "-c",
    "--git-dir",
    "--work-tree",
    "--namespace",
    "--super-prefix",
    "--config-env",
];

/// `git` and its subcommand with arguments, without the global options before the subcommand.
fn strip_git_options(words: &[String]) -> Vec<String> {
    let mut i = 1;
    while let Some(word) = words.get(i) {
        if !word.starts_with('-') {
            break;
        }
        i += if GIT_VALUE_OPTIONS.contains(&word.as_str()) { 2 } else { 1 };
    }
    std::iter::once("git".to_string())
        .chain(words.iter().skip(i).cloned())
        .collect()
}

/// An operand of a deletion.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Target {
    /// A path as written, relative to any of `cwds`.
    Path { word: String, cwds: Vec<PathBuf> },
    /// Everything below a directory, which `find DIR -delete` removes without a name test.
    Contents { word: String, cwds: Vec<PathBuf> },
    /// A word only known when the command runs: `$VAR`, `` `cmd` ``, `~user`, or a relative
    /// path after `cd` to an unknown place.
    Unknown(String),
    /// What `xargs` reads from its input.
    Input,
}

/// What the found commands delete with `rm`, `rmdir`, `find -delete` or `find -exec rm`.
pub fn deletion_targets(analysis: &Analysis) -> Vec<Target> {
    let mut targets = Vec::new();
    for found in &analysis.found {
        let Some(first) = found.inner.first() else {
            continue;
        };
        match program_name(first) {
            "rm" | "rmdir" => {
                // `{}` stands for what find or `xargs -I {}` passes in; those are judged there.
                for word in operands(&found.inner[1..]).into_iter().filter(|w| !w.contains("{}")) {
                    targets.push(target(word, &found.cwds, false));
                }
                if found.reads_operands {
                    targets.push(Target::Input);
                }
            }
            "find" => find_targets(&found.inner, &found.cwds, &mut targets),
            _ => {}
        }
    }
    targets
}

/// The words that are neither options nor redirections (which a rough split leaves in);
/// everything after `--` counts.
fn operands(args: &[String]) -> Vec<&String> {
    let mut operands = Vec::new();
    let mut options_done = false;
    let mut redirect_target = false;
    for word in args {
        if std::mem::take(&mut redirect_target) {
            continue;
        }
        if let Some(attached) = redirection(word) {
            redirect_target = !attached;
        } else if !options_done && word == "--" {
            options_done = true;
        } else if options_done || !word.starts_with('-') || word.len() == 1 {
            operands.push(word);
        }
    }
    operands
}

/// Whether `word` is a redirection such as `>`, `2>>` or `&>log`, and if so whether its
/// target is attached (`>log`) rather than the next word.
fn redirection(word: &str) -> Option<bool> {
    let rest = word.trim_start_matches(|c: char| c.is_ascii_digit());
    let rest = rest.strip_prefix('&').unwrap_or(rest);
    let operator = [">>", "<<<", "<<", ">&", "<&", ">|", ">", "<"]
        .iter()
        .find(|operator| rest.starts_with(**operator))?;
    Some(rest.len() > operator.len())
}

fn target(word: &str, cwds: &Cwds, contents: bool) -> Target {
    let anchored = word.starts_with('/') || word == "~" || word.starts_with("~/");
    let cwds = match cwds {
        _ if literal(word).is_none() => return Target::Unknown(word.to_string()),
        Cwds::Known(dirs) => dirs.clone(),
        Cwds::Unknown if anchored => vec![PathBuf::from("/")],
        Cwds::Unknown => return Target::Unknown(word.to_string()),
    };
    let word = word.to_string();
    if contents {
        Target::Contents { word, cwds }
    } else {
        Target::Path { word, cwds }
    }
}

/// `find` deletes what it finds with `-delete` or `-exec rm`. Unless a name test narrows
/// that down, it is everything below the starting points.
fn find_targets(words: &[String], cwds: &Cwds, targets: &mut Vec<Target>) {
    let (points, expression) = find_parts(words);
    let deletes = expression.iter().any(|w| w == "-delete")
        || find_exec_commands(expression).iter().any(|command| {
            unwrap(command)
                .words
                .first()
                .is_some_and(|name| matches!(program_name(name), "rm" | "rmdir"))
        });
    if deletes && !selects_by_name(expression) {
        for point in points {
            targets.push(target(point, cwds, true));
        }
    }
}

/// Whether a name test such as `-name '*.o'` limits what `find` matches. A test that
/// matches everything (`-name '*'`), negation and alternatives do not count.
fn selects_by_name(expression: &[String]) -> bool {
    if expression.iter().any(|w| matches!(w.as_str(), "!" | "-not" | "-o" | "-or")) {
        return false;
    }
    expression.windows(2).any(|pair| {
        let pattern = pair[1].as_str();
        match pair[0].as_str() {
            "-name" | "-iname" | "-path" | "-ipath" | "-wholename" | "-iwholename" | "-lname" | "-ilname" => {
                !pattern.chars().all(|c| matches!(c, '*' | '?'))
            }
            "-regex" | "-iregex" => !pattern.chars().all(|c| matches!(c, '.' | '*' | '+' | '?' | '^' | '$')),
            _ => false,
        }
    })
}

/// A `mkdir`, `touch`, `cp` or `mv` with only options that do not change where it writes.
#[derive(Debug, PartialEq, Eq)]
pub struct FileCommand {
    pub name: String,
    pub operands: Vec<String>,
}

pub fn file_command(words: &[String]) -> Option<FileCommand> {
    let (name, rest) = words.split_first()?;
    let (short, long): (&str, &[&str]) = match name.as_str() {
        "mkdir" => ("pv", &["--parents", "--verbose"]),
        "touch" => ("acm", &["--no-create"]),
        "cp" => (
            "RrafinpvLPH",
            &["--recursive", "--archive", "--force", "--interactive", "--no-clobber", "--verbose"],
        ),
        "mv" => ("finv", &["--force", "--interactive", "--no-clobber", "--verbose"]),
        _ => return None,
    };
    let mut operands = Vec::new();
    let mut options_done = false;
    for word in rest {
        if options_done || !word.starts_with('-') || word.len() == 1 {
            operands.push(word.clone());
        } else if word == "--" {
            options_done = true;
        } else if word.starts_with("--") {
            if !long.contains(&word.as_str()) {
                return None;
            }
        } else if !word[1..].chars().all(|c| short.contains(c)) {
            return None;
        }
    }
    (!operands.is_empty()).then(|| FileCommand {
        name: name.clone(),
        operands,
    })
}

const SHELLS: &[&str] = &["sh", "bash", "zsh", "dash", "ksh", "mksh", "ash", "fish", "csh", "tcsh"];
const INTERPRETERS: &[&str] = &[
    "python", "python2", "python3", "pypy", "pypy3", "py", "node", "nodejs", "deno", "bun", "ruby", "perl",
    "php", "lua", "luajit", "Rscript", "julia", "osascript", "pwsh", "powershell",
];
/// Commands that start a package given as the second word: `npx create-vite`.
const LAUNCHERS: &[&str] = &["npx", "pnpx", "bunx", "uvx"];
/// Commands whose second word is a verb and whose third word is what runs: `npm run build`.
const RUNNERS: &[(&str, &[&str])] = &[
    ("npm", &["run", "run-script", "exec", "x"]),
    ("pnpm", &["run", "exec", "dlx"]),
    ("yarn", &["run", "exec", "dlx"]),
    ("bun", &["run", "x", "exec"]),
    ("deno", &["run", "task"]),
    ("uv", &["run"]),
    ("pipx", &["run"]),
    ("poetry", &["run"]),
    ("pipenv", &["run"]),
    ("pdm", &["run"]),
    ("hatch", &["run"]),
    ("go", &["run"]),
];
/// Commands no rule is proposed for: they run other commands, delete files, or are `find`,
/// whose `-exec` and `-delete` a rule could not tell apart.
const NEVER_PROPOSED: &[&str] = &[
    "sudo", "doas", "su", "env", "xargs", "eval", "exec", "command", "builtin", "nohup", "nice", "time",
    "timeout", "watch", "stdbuf", "busybox", "toybox", "chroot", "nsenter", "source", ".", "trap", "rm",
    "rmdir", "find",
];

/// The rule offered as "always allow in this session" for one simple command: normally its
/// first two words, such as `cargo test`. `None` when every such rule would also allow
/// arbitrary code: inline code for a shell or interpreter (`python3 -c`), wrappers, deletion.
/// Runners get their third word too: `npm run build`, `python3 -m pytest`.
pub fn propose_rule(words: &[String]) -> Option<CommandRule> {
    let name = program_name(words.first()?);
    let second = words.get(1).map(String::as_str);
    let third = words.get(2).map(String::as_str);
    let plain = |word: Option<&str>| word.is_some_and(|w| !w.starts_with('-'));
    let len = if NEVER_PROPOSED.contains(&name) {
        return None;
    } else if let Some((_, verbs)) = RUNNERS.iter().find(|(runner, _)| *runner == name)
        && second.is_some_and(|s| verbs.contains(&s))
    {
        if !plain(third) {
            return None;
        }
        3
    } else if INTERPRETERS.contains(&name) {
        match second {
            Some("-m") if plain(third) => 3,
            Some("eval" | "repl") => return None,
            _ if plain(second) => 2,
            _ => return None,
        }
    } else if SHELLS.contains(&name) || LAUNCHERS.contains(&name) || name == "git" {
        if !plain(second) {
            return None;
        }
        2
    } else {
        2
    };
    Some(CommandRule {
        prefix: words.iter().take(len).cloned().collect(),
        decision: RuleDecision::Allow,
        reason: None,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    fn words(s: &str) -> Vec<String> {
        s.split_whitespace().map(str::to_string).collect()
    }

    #[test]
    fn splits_simple_commands() {
        assert_eq!(
            parse_command("cargo test && git status | head -5; echo 'a b'"),
            Parsed::Simple(vec![
                words("cargo test"),
                words("git status"),
                words("head -5"),
                vec!["echo".into(), "a b".into()],
            ])
        );
    }

    #[test]
    fn flags_complex_commands() {
        for cmd in [
            "echo hi > out.txt",
            "cat $(ls)",
            "echo `whoami`",
            "FOO=1 make",
            "sleep 10 &",
            "echo \"$HOME\"",
            "(cd x && ls)",
            "echo 'unterminated",
            "cargo test 2>&1",
        ] {
            assert_eq!(parse_command(cmd), Parsed::Complex, "{cmd}");
        }
    }

    #[test]
    fn read_only_list_is_conservative() {
        assert!(is_read_only(&words("ls -la"), false));
        assert!(is_read_only(&words("rg -n foo src"), false));
        assert!(!is_read_only(&words("rg --pre cat foo"), false));
        assert!(!is_read_only(&words("find . -name x -delete"), false));
        assert!(is_read_only(&words("find . -name *.rs"), false));
        assert!(!is_read_only(&words("./ls"), false));
        assert!(!is_read_only(&words("rm -rf x"), false));
        assert!(!is_read_only(&words("git status"), false), "git only in trusted projects");
        assert!(is_read_only(&words("git status"), true));
        assert!(!is_read_only(&words("git -c core.pager=x log"), true));
        assert!(!is_read_only(&words("git branch -D main"), true));
        assert!(is_read_only(&words("git branch -a"), true));
        assert!(!is_read_only(&words("git diff --output=x"), true));
        assert!(!is_read_only(&words("date 0101"), false));
        assert!(is_read_only(&words("cd .."), false));
        assert!(is_read_only(&words("pushd src"), false));
    }

    // ---- analysis ----

    const CWD: &str = "/work/proj";
    const HOME: &str = "/home/me";

    fn analyze_in(command: &str, cdpath: bool) -> Analysis {
        let ctx = ShellContext {
            cwd: Path::new(CWD),
            home: Some(Path::new(HOME)),
            cdpath,
        };
        analyze(command, &ctx)
    }

    /// The inner commands found, as text.
    fn inner(command: &str) -> Vec<String> {
        analyze_in(command, false).found.iter().map(|f| f.inner.join(" ")).collect()
    }

    /// Deletion targets as `word@cwd1,cwd2`, `contents:word@...` or `?word`.
    fn targets(command: &str) -> Vec<String> {
        targets_with(command, false)
    }

    fn targets_with(command: &str, cdpath: bool) -> Vec<String> {
        let dirs = |cwds: &[PathBuf]| cwds.iter().map(|c| c.display().to_string()).collect::<Vec<_>>().join(",");
        deletion_targets(&analyze_in(command, cdpath))
            .into_iter()
            .map(|t| match t {
                Target::Path { word, cwds } => format!("{word}@{}", dirs(&cwds)),
                Target::Contents { word, cwds } => format!("contents:{word}@{}", dirs(&cwds)),
                Target::Unknown(word) => format!("?{word}"),
                Target::Input => "?input".to_string(),
            })
            .collect()
    }

    #[test]
    fn finds_commands_behind_wrappers() {
        assert_eq!(inner("sudo -u root rm -rf /"), vec!["rm -rf /"]);
        assert_eq!(inner("sudo -E rm x"), vec!["rm x"]);
        assert_eq!(inner("sudo -- rm x"), vec!["rm x"]);
        assert_eq!(inner("doas -u root rm x"), vec!["rm x"]);
        assert_eq!(inner("env A=1 B=2 rm x"), vec!["rm x"]);
        assert_eq!(inner("env -i -u PATH rm x"), vec!["rm x"]);
        assert_eq!(inner("nice -n 5 nohup time -p rm x"), vec!["rm x"]);
        assert_eq!(inner("timeout -s KILL 10 rm x"), vec!["rm x"]);
        assert_eq!(inner("timeout 5s rm x"), vec!["rm x"]);
        assert_eq!(inner("exec -a name rm x"), vec!["rm x"]);
        assert_eq!(inner("command rm x"), vec!["rm x"]);
        assert_eq!(inner("builtin cd x"), vec!["cd x"]);
        assert_eq!(inner("busybox rm -rf x"), vec!["rm -rf x"]);
        assert_eq!(inner("stdbuf -o L rm x"), vec!["rm x"]);
        assert_eq!(inner("watch -n 1 rm x"), vec!["rm x"]);
        assert_eq!(inner("/usr/bin/sudo /bin/rm x"), vec!["/bin/rm x"]);
        assert_eq!(inner("env -S 'rm -rf x' y"), vec!["rm -rf x y"]);
        assert_eq!(inner("env --split-string='rm -rf x'"), vec!["rm -rf x"]);
        assert_eq!(inner("xargs -0 -n 1 rm -f"), vec!["rm -f"]);
        assert_eq!(inner("if true; then rm x; fi"), vec!["true", "rm x", "fi"]);
        assert_eq!(inner("! rm x"), vec!["rm x"]);
        assert_eq!(inner("FOO=1 BAR=2 rm x"), vec!["rm x"]);
        assert_eq!(inner("grep -r rm ."), vec!["grep -r rm ."]);
    }

    #[test]
    fn follows_scripts() {
        assert_eq!(inner("sh -c 'rm -rf /'"), vec!["sh -c rm -rf /", "rm -rf /"]);
        assert_eq!(inner("bash -lc 'cd x; git push'"), vec!["bash -lc cd x; git push", "cd x", "git push"]);
        assert_eq!(inner("bash -o pipefail -c 'git push'")[1], "git push");
        assert_eq!(inner("eval 'git push'"), vec!["eval git push", "git push"]);
        assert_eq!(inner("eval git push --force"), vec!["eval git push --force", "git push --force"]);
        assert_eq!(inner("trap 'rm -rf x' EXIT"), vec!["trap rm -rf x EXIT", "rm -rf x"]);
        assert_eq!(inner("trap -- 'rm x' INT")[1], "rm x");
        assert_eq!(inner("trap - EXIT"), vec!["trap - EXIT"]);
        assert_eq!(inner("trap -p"), vec!["trap -p"]);
        assert_eq!(inner("find . -exec rm -rf {} +"), vec!["find . -exec rm -rf {} +", "rm -rf {}"]);
        // `{}` makes the line complex, so the script's words follow as they are.
        assert_eq!(inner("find . -name x -execdir sh -c 'rm $1' _ {} ;")[1..], ["sh -c rm $1 _ {}", "rm $1 _ {}"]);
        // Rough splits of complex commands keep following scripts.
        assert!(inner("sudo sh -c \"rm -rf $HOME\"").contains(&"rm -rf $HOME".to_string()));
        assert!(inner("echo $(rm -rf /)").contains(&"rm -rf /".to_string()));
        assert!(inner("bash <<'EOF'\nrm -rf /\nEOF").contains(&"rm -rf /".to_string()));
        assert!(inner("f() { rm -rf /; }; f").contains(&"rm -rf /".to_string()));
        // Not scripts.
        assert_eq!(inner("bash script.sh /"), vec!["bash script.sh /"]);
        assert_eq!(inner("echo rm -rf /"), vec!["echo rm -rf /"]);
    }

    #[test]
    fn deep_nesting_cannot_be_checked() {
        let mut command = "rm -rf x".to_string();
        for _ in 0..MAX_DEPTH {
            command = format!("eval {}", shell_quote(&command));
        }
        assert!(!analyze_in(&command, false).too_deep, "{MAX_DEPTH} levels are followed");
        let deeper = format!("eval {}", shell_quote(&command));
        assert!(analyze_in(&deeper, false).too_deep);
        let mut nested_find = "rm x".to_string();
        for _ in 0..=MAX_DEPTH {
            nested_find = format!("find . -exec {nested_find} ;");
        }
        assert!(analyze_in(&nested_find, false).too_deep);
    }

    fn shell_quote(s: &str) -> String {
        format!("'{}'", s.replace('\'', r"'\''"))
    }

    #[test]
    fn deletion_targets_by_kind() {
        assert_eq!(targets("rm -rf build dist"), vec!["build@/work/proj", "dist@/work/proj"]);
        assert_eq!(targets("rm -- -weird"), vec!["-weird@/work/proj"]);
        assert_eq!(targets("/bin/rm -f a"), vec!["a@/work/proj"]);
        assert_eq!(targets("rmdir -p a/b"), vec!["a/b@/work/proj"]);
        assert_eq!(targets("sudo rm -rf ~"), vec!["~@/work/proj"]);
        assert_eq!(targets("rm -rf $DIR/build"), vec!["?$DIR/build"]);
        assert_eq!(targets("rm -rf \"$TARGET\""), vec!["?$TARGET"]);
        assert_eq!(targets("rm -rf `pwd`"), vec!["?`…`"]);
        assert_eq!(targets("rm -rf $(pwd)"), vec!["?$"]);
        // Redirections left in by a rough split are not operands.
        assert_eq!(targets("rm -rf / > /dev/null 2>&1"), vec!["/@/work/proj"]);
        assert_eq!(targets("rm -r x 2>/dev/null >>log <in"), vec!["x@/work/proj"]);
        assert_eq!(targets("rm -rf ~other"), vec!["?~other"]);
        assert_eq!(targets("find . -name '*.pyc' | xargs rm -f"), vec!["?input"]);
        assert_eq!(targets("ls | xargs -I {} rm -rf {} keep"), vec!["keep@/work/proj", "?input"]);
        assert!(targets("grep -r rm .").is_empty());
        assert!(targets("git rm -r src").is_empty());
        assert!(targets("echo rm -rf /").is_empty());
    }

    #[test]
    fn find_deletes_everything_below_its_starting_points_without_a_name_test() {
        assert_eq!(targets("find . -delete"), vec!["contents:.@/work/proj"]);
        assert_eq!(targets("find ~ /tmp -type f -delete"), vec!["contents:~@/work/proj", "contents:/tmp@/work/proj"]);
        assert_eq!(targets("find -L -mindepth 1 -delete"), vec!["contents:.@/work/proj"]);
        assert_eq!(targets("find build -exec rm -rf {} +"), vec!["contents:build@/work/proj"]);
        assert_eq!(targets("find . -exec sudo rm {} ;"), vec!["contents:.@/work/proj"]);
        assert_eq!(targets("find . -name '*' -delete"), vec!["contents:.@/work/proj"]);
        assert_eq!(targets("find . ! -name keep -delete"), vec!["contents:.@/work/proj"]);
        assert_eq!(targets("find . -name a -o -type f -delete"), vec!["contents:.@/work/proj"]);
        assert_eq!(targets("find . -regex '.*' -delete"), vec!["contents:.@/work/proj"]);
        // A name test narrows the deletion down.
        assert!(targets("find . -name '*.pyc' -delete").is_empty());
        assert!(targets("find / -path '*/node_modules' -exec rm -rf {} +").is_empty());
        assert!(targets("find . -regex '.*\\.o' -delete").is_empty());
        // Explicit operands of the executed rm still count.
        assert_eq!(targets("find . -name x -exec rm -rf {} / ;"), vec!["/@/work/proj"]);
        // Without a deleting action find only reads.
        assert!(targets("find . -type f").is_empty());
        assert!(targets("find . -exec cat {} +").is_empty());
    }

    #[test]
    fn parses_operators_between_commands() {
        let joiners = |s: &str| parse_chain(s).unwrap().into_iter().map(|(_, j)| j).collect::<Vec<_>>();
        assert_eq!(
            joiners("a && b || c | d; e\nf"),
            vec![Joiner::And, Joiner::Or, Joiner::Pipe, Joiner::End, Joiner::End, Joiner::End]
        );
        assert_eq!(parse_chain("a > b"), None);
    }

    #[test]
    fn follows_cd_through_and_chains() {
        // Behind `&&` the command runs only where the cd went.
        assert_eq!(targets("cd .. && rm -r proj"), vec!["proj@/work"]);
        assert_eq!(targets("cd /tmp && rm -r x"), vec!["x@/tmp"]);
        assert_eq!(targets("cd && rm -r x"), vec!["x@/home/me"]);
        assert_eq!(targets("cd ~/a && rm -r x"), vec!["x@/home/me/a"]);
        assert_eq!(targets("cd -P -- sub && rm -r x"), vec!["x@/work/proj/sub"]);
        assert_eq!(targets("cd a && cd b && make && rm -r x"), vec!["x@/work/proj/a/b"]);
        assert_eq!(targets("pushd .. && rm -r x && popd"), vec!["x@/work"]);
        assert_eq!(targets("pushd .. && popd && rm -r x"), vec!["x@/work/proj,/work"]);
        assert_eq!(targets("cd - && cd /tmp && rm -r x"), vec!["x@/tmp"]);
        // Wrappers that change directory do so for sure.
        assert_eq!(targets("env -C .. rm -r proj"), vec!["proj@/work"]);
        assert_eq!(targets("env --chdir=/tmp rm -r x"), vec!["x@/tmp"]);
        assert_eq!(targets("env -C/tmp rm -r x"), vec!["x@/tmp"]);
        assert_eq!(targets("sudo -D / rm -r x"), vec!["x@/"]);
    }

    #[test]
    fn keeps_every_possible_directory_otherwise() {
        // The cd may fail before `;`, be skipped before `||`, or stay in a pipeline's subshell.
        assert_eq!(targets("cd build; rm -r *"), vec!["*@/work/proj,/work/proj/build"]);
        assert_eq!(targets("cd ..; cd ..; rm -r x"), vec!["x@/work/proj,/work,/"]);
        assert_eq!(targets("cd x && a || rm -r y"), vec!["y@/work/proj,/work/proj/x"]);
        assert_eq!(targets("true || cd x && rm -r y"), vec!["y@/work/proj,/work/proj/x"]);
        assert_eq!(targets("cd .. | rm -r x"), vec!["x@/work/proj,/work"]);
        assert_eq!(targets("(cd .. && rm -r x)"), vec!["x@/work/proj,/work"]);
        // A script starts where it is run, and its cd stays inside it.
        assert_eq!(targets("sh -c 'cd ..; rm -r x'"), vec!["x@/work/proj,/work"]);
        assert_eq!(targets("sh -c 'cd .. && rm -r x'"), vec!["x@/work"]);
        assert_eq!(targets("sh -c 'cd ..'; rm -r x"), vec!["x@/work/proj"]);
        // eval and trap actions run in this shell, so their cd counts.
        assert_eq!(targets("eval 'cd ..'; rm -r proj"), vec!["proj@/work/proj,/work"]);
        assert_eq!(targets("eval cd .. && rm -r proj"), vec!["proj@/work/proj,/work"]);
        assert_eq!(targets("trap 'cd ..' DEBUG; rm -r proj"), vec!["proj@/work/proj,/work"]);
        // In a rough split the rest of a quoted script shows up as later segments.
        assert_eq!(targets("sh -c \"cd ..; rm -r proj\" > log"), vec!["proj@/work/proj,/work"]);
        // Absolute targets do not depend on the directory.
        assert_eq!(targets("cd $X && rm -r /tmp/x"), vec!["/tmp/x@/"]);
    }

    #[test]
    fn unknown_directories_make_relative_targets_unknown() {
        assert_eq!(targets("cd $X && rm -r build"), vec!["?build"]);
        assert_eq!(targets("cd - && rm -r build"), vec!["?build"]);
        assert_eq!(targets("cd a b && rm -r build"), vec!["?build"]);
        assert_eq!(targets("pushd && rm -r build"), vec!["?build"]);
        assert_eq!(targets("pushd +1 && rm -r build"), vec!["?build"]);
        assert_eq!(targets("env -C $X rm -r build"), vec!["?build"]);
        assert_eq!(targets_with("cd sub && rm -r build", true), vec!["?build"], "CDPATH may redirect cd");
        assert_eq!(targets_with("cd ./sub && rm -r build", true), vec!["build@/work/proj/sub"]);
        let many = (0..MAX_CWDS).map(|i| format!("cd d{i}")).collect::<Vec<_>>().join("; ");
        assert_eq!(targets(&format!("{many}; rm -r x")), vec!["?x"]);
    }

    fn forms(command: &str) -> Vec<String> {
        let analysis = analyze_in(command, false);
        rule_forms(&analysis.found[0]).iter().map(|f| f.join(" ")).collect()
    }

    #[test]
    fn rule_forms_see_through_paths_and_git_options() {
        assert_eq!(forms("git push"), vec!["git push"]);
        assert_eq!(forms("/usr/bin/git push"), vec!["/usr/bin/git push", "git push"]);
        assert_eq!(forms("sudo git push"), vec!["sudo git push", "git push"]);
        assert_eq!(
            forms("git -C .. -c a=b --no-pager --git-dir=x push -f"),
            vec!["git -C .. -c a=b --no-pager --git-dir=x push -f", "git push -f"]
        );
        assert_eq!(forms("env A=1 /bin/rm -rf x"), vec!["env A=1 /bin/rm -rf x", "/bin/rm -rf x", "rm -rf x"]);
    }

    #[test]
    fn file_commands_with_plain_options() {
        let operands = |s: &str| file_command(&words(s)).map(|c| c.operands);
        assert_eq!(operands("mkdir -p a/b c"), Some(words("a/b c")));
        assert_eq!(operands("touch x"), Some(words("x")));
        assert_eq!(operands("cp -rv src dst"), Some(words("src dst")));
        assert_eq!(operands("mv -- -a b"), Some(words("-a b")));
        assert_eq!(operands("mkdir -m 700 x"), None);
        assert_eq!(operands("cp -t dst src"), None);
        assert_eq!(operands("mv --target-directory=d x"), None);
        assert_eq!(operands("touch -r ref x"), None);
        assert_eq!(operands("cp -s a b"), None);
        assert_eq!(operands("mkdir"), None);
        assert_eq!(operands("rm x"), None);
        assert_eq!(operands("./mkdir x"), None);
        assert_eq!(file_command(&words("cp a b")).unwrap().name, "cp");
    }

    fn proposal(command: &str) -> Option<String> {
        propose_rule(&words(command)).map(|rule| rule.prefix.join(" "))
    }

    #[test]
    fn proposes_narrow_prefixes_only() {
        assert_eq!(proposal("cargo test --release").as_deref(), Some("cargo test"));
        assert_eq!(proposal("make").as_deref(), Some("make"));
        assert_eq!(proposal("git push origin main").as_deref(), Some("git push"));
        assert_eq!(proposal("npm install").as_deref(), Some("npm install"));
        assert_eq!(proposal("npm run build -- --watch").as_deref(), Some("npm run build"));
        assert_eq!(proposal("pnpm exec vitest").as_deref(), Some("pnpm exec vitest"));
        assert_eq!(proposal("uv run pytest -x").as_deref(), Some("uv run pytest"));
        assert_eq!(proposal("go run ./cmd/x").as_deref(), Some("go run ./cmd/x"));
        assert_eq!(proposal("python3 build.py --fast").as_deref(), Some("python3 build.py"));
        assert_eq!(proposal("python3 -m pytest -x").as_deref(), Some("python3 -m pytest"));
        assert_eq!(proposal("node server.js").as_deref(), Some("node server.js"));
        assert_eq!(proposal("bash scripts/check.sh").as_deref(), Some("bash scripts/check.sh"));
        assert_eq!(proposal("deno fmt").as_deref(), Some("deno fmt"));
        assert_eq!(proposal("npx create-vite app").as_deref(), Some("npx create-vite"));
        for broad in [
            "python3 -c print(1)",
            "python3",
            "python -",
            "python3 -m",
            "/usr/bin/python3 -c x",
            "node -e x",
            "perl -e x",
            "ruby -e x",
            "deno eval x",
            "bun -e x",
            "bash -lc x",
            "sh -c x",
            "zsh",
            "npm run",
            "npm run --silent",
            "pnpm dlx",
            "npx -y pkg",
            "git -c core.pager=x log",
            "git -C .. push",
            "git",
            "sudo apt install x",
            "env FOO=1 make",
            "xargs rm",
            "eval x",
            "rm -rf build",
            "rmdir x",
            "find . -delete",
            "timeout 5 cargo test",
            "source env.sh",
        ] {
            assert_eq!(proposal(broad), None, "{broad}");
        }
    }
}
