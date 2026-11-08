//! `her login` and `her logout`: the one way to connect a model, by key, subscription, local
//! server or a compatible service of one's own (standalone-cli-design §7.3). Keys go through the
//! service's `set_key`, the rules the desktop app follows too.

use anyhow::Context as _;
use anyhow::bail;
use her_llm::AuthConfig;
use her_protocol::api::ChatGptLoginState;
use her_protocol::api::ModelsListResult;
use her_protocol::api::ProviderSummary;
use her_protocol::customize::ConfigEdit;
use her_protocol::customize::ConfigLayer;
use her_protocol::customize::ConfigMerge;
use her_protocol::customize::ConfigWriteParams;
use her_server::Server;
use std::io::BufRead as _;
use std::io::IsTerminal as _;
use std::io::Read as _;
use std::io::Write as _;

use crate::cmd_customize::shorten;
use crate::cmd_serve;
use crate::setup;

#[derive(Default)]
pub struct LoginArgs {
    pub provider: Option<String>,
    pub key_stdin: bool,
    pub model: Option<String>,
    pub base_url: Option<String>,
    pub api: Option<String>,
    pub no_key: bool,
}

/// `chatgpt` is how a person says the ChatGPT subscription.
const CHATGPT: &str = "chatgpt";

/// A key that was refused: what the provider said, nothing saved.
#[derive(Debug)]
pub struct KeyRejected(pub String);

impl std::fmt::Display for KeyRejected {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "the key was not accepted: {}\nnothing was saved", self.0)
    }
}

impl std::error::Error for KeyRejected {}

/// A key: asked for without showing it at a terminal, otherwise read from standard input.
pub(crate) fn read_key() -> anyhow::Result<String> {
    let key = if std::io::stdin().is_terminal() {
        read_hidden("Paste the key (not shown): ")?
    } else {
        let mut text = String::new();
        std::io::stdin().lock().read_to_string(&mut text).context("cannot read the key from standard input")?;
        text.trim().to_string()
    };
    if key.is_empty() {
        bail!("the key is empty");
    }
    Ok(key)
}

fn ask_line(question: &str) -> anyhow::Result<String> {
    eprint!("{question}");
    std::io::stderr().flush()?;
    let mut line = String::new();
    std::io::stdin().lock().read_line(&mut line)?;
    Ok(line.trim().to_string())
}

/// Picks one of `items` by number. Empty input takes the first.
fn choose(question: &str, items: &[String]) -> anyhow::Result<usize> {
    eprintln!("{question}");
    for (index, item) in items.iter().enumerate() {
        eprintln!("  {}  {item}", index + 1);
    }
    loop {
        let answer = ask_line("> ")?;
        if answer.is_empty() {
            return Ok(0);
        }
        match answer.parse::<usize>() {
            Ok(number) if (1..=items.len()).contains(&number) => return Ok(number - 1),
            _ => eprintln!("a number from 1 to {}", items.len()),
        }
    }
}

/// A line typed without showing it.
#[cfg(unix)]
fn read_hidden(question: &str) -> anyhow::Result<String> {
    eprint!("{question}");
    std::io::stderr().flush()?;
    let fd = libc::STDIN_FILENO;
    // SAFETY: `termios` is plain data that `tcgetattr` fills in; the original is put back below.
    let original = unsafe {
        let mut settings = std::mem::zeroed::<libc::termios>();
        (libc::tcgetattr(fd, &mut settings) == 0).then_some(settings)
    };
    if let Some(original) = original {
        let mut hidden = original;
        hidden.c_lflag &= !libc::ECHO;
        // SAFETY: a valid descriptor and settings read from it.
        unsafe { libc::tcsetattr(fd, libc::TCSANOW, &hidden) };
    }
    let mut line = String::new();
    let read = std::io::stdin().lock().read_line(&mut line);
    if let Some(original) = original {
        // SAFETY: as above.
        unsafe { libc::tcsetattr(fd, libc::TCSANOW, &original) };
    }
    eprintln!();
    read?;
    Ok(line.trim().to_string())
}

fn providers_with_keys(listing: &ModelsListResult) -> Vec<&ProviderSummary> {
    // The Anthropic-compatible DeepSeek endpoint is the same account as DeepSeek.
    listing.providers.iter().filter(|p| matches!(p.auth, her_protocol::api::AuthKind::ApiKey) && p.id != "deepseek-anthropic").collect()
}

/// What the person wants to connect, asked when not given.
fn ask_provider(listing: &ModelsListResult) -> anyhow::Result<String> {
    let ways = [
        "API key       DeepSeek, OpenAI, Anthropic, Gemini, Qwen, Kimi, GLM, OpenRouter".to_string(),
        "ChatGPT       sign in with your subscription, no key".to_string(),
        "Token Plan    Bailian, Tencent, Volcengine, Zhipu, Z.ai".to_string(),
        "Local model   Ollama".to_string(),
    ];
    let keys = providers_with_keys(listing);
    let names = |wanted_plan: bool| -> Vec<&ProviderSummary> { keys.iter().copied().filter(|p| p.plan.is_some() == wanted_plan).collect() };
    match choose("How do you want to connect a model?", &ways)? {
        1 => Ok(CHATGPT.to_string()),
        3 => Ok("ollama".to_string()),
        way => {
            let list = names(way == 2);
            let items: Vec<String> = list.iter().map(|p| p.name.clone()).collect();
            let picked = choose("Which one?", &items)?;
            Ok(list[picked].id.clone())
        }
    }
}

/// Adds a service of one's own to models.toml; returns what to put back if its key is refused.
fn add_service(home: &her_core::HerHome, id: &str, base_url: &str, api: &str, no_key: bool, model: Option<&str>) -> anyhow::Result<Option<Vec<u8>>> {
    let valid = !id.is_empty() && id.chars().all(|c| c.is_ascii_alphanumeric() || matches!(c, '-' | '_'));
    if !valid {
        bail!("`{id}` cannot be a provider's name: use letters, digits, - and _");
    }
    if !["openai-chat", "anthropic-messages", "openai-responses"].contains(&api) {
        bail!("--api {api}: expected openai-chat, anthropic-messages or openai-responses");
    }
    let path = home.models_file();
    let _lock = her_core::config_edit::lock(&path, home.root()).map_err(|err| anyhow::anyhow!("{err}"))?;
    let before = match std::fs::read(&path) {
        Ok(bytes) => Some(bytes),
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => None,
        Err(err) => return Err(err).with_context(|| format!("cannot read {}", path.display())),
    };
    let text = before.as_deref().map(String::from_utf8_lossy).unwrap_or_default();
    let mut document: toml_edit::DocumentMut = text.parse().with_context(|| format!("cannot read {}", path.display()))?;
    let providers = document.entry("providers").or_insert(toml_edit::table());
    let providers = providers.as_table_mut().context("`providers` in models.toml is not a table")?;
    providers.set_implicit(true);
    let mut table = toml_edit::Table::new();
    table["name"] = toml_edit::value(id);
    table["api"] = toml_edit::value(api);
    table["base_url"] = toml_edit::value(base_url);
    if no_key {
        table["auth"] = toml_edit::value("none");
    }
    if let Some(model) = model.and_then(|m| m.split_once('/').map(|(_, id)| id).or(Some(m))) {
        let mut entry = toml_edit::Table::new();
        entry["id"] = toml_edit::value(model);
        let mut models = toml_edit::ArrayOfTables::new();
        models.push(entry);
        table["models"] = toml_edit::Item::ArrayOfTables(models);
    }
    providers[id] = toml_edit::Item::Table(table);
    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent)?;
    }
    her_core::config_edit::replace_file(&path, document.to_string().as_bytes())
        .with_context(|| format!("cannot write {}", path.display()))?;
    Ok(before)
}

async fn set_model(server: &Server, home: &her_core::HerHome, reference: &str) -> anyhow::Result<()> {
    let written = server
        .config_write(ConfigWriteParams {
            layer: ConfigLayer::User,
            cwd: None,
            edits: vec![ConfigEdit { key_path: "model".into(), value: reference.into(), merge: ConfigMerge::Replace }],
            expected_version: None,
        })
        .await
        .map_err(|err| anyhow::anyhow!("{}", err.message))?;
    println!("model set to {reference} in {}", shorten(home, &written.file));
    Ok(())
}

/// Offers the provider's models and writes the one chosen; `--model` skips the question, and
/// without a terminal nothing is chosen: the default follows the connected providers.
async fn choose_model(server: &Server, home: &her_core::HerHome, provider_id: &str, given: Option<String>, interactive: bool) -> anyhow::Result<()> {
    if let Some(given) = given {
        let reference = if given.contains('/') && given.split('/').next() == Some(provider_id) { given } else { format!("{provider_id}/{given}") };
        return set_model(server, home, &reference).await;
    }
    if !interactive {
        return Ok(());
    }
    let listing = {
        let server = server.clone();
        tokio::task::spawn_blocking(move || server.list_models()).await?
    };
    let start = server.models().provider(provider_id).and_then(|p| her_core::models::provider_start(server.models(), &p));
    let mut shown: Vec<&str> = listing.models.iter().filter(|m| m.provider == provider_id && m.available && m.shown).map(|m| m.id.as_str()).collect();
    if let Some(start) = &start {
        shown.retain(|id| id != start);
        shown.insert(0, start);
    }
    match shown.as_slice() {
        [] => eprintln!("no models listed yet; name one with -m {provider_id}/<model-id>"),
        [only] => set_model(server, home, only).await?,
        many => {
            let items: Vec<String> = many.iter().map(|id| id.to_string()).collect();
            let picked = choose("Which model should new sessions start on?", &items)?;
            set_model(server, home, many[picked]).await?;
        }
    }
    Ok(())
}

async fn chatgpt_login(overrides: &[String], model: Option<String>, interactive: bool) -> anyhow::Result<u8> {
    let (server, _guard) = cmd_serve::start(overrides)?;
    let chatgpt = server.connections().chatgpt.clone();
    let started = chatgpt.start().await?;
    if let Some(url) = &started.authorize_url {
        // Print the address for terminals without a browser; never launch a shell with it.
        eprintln!("Sign in to ChatGPT in your browser:\n{url}");
        crate::cmd_customize::open_browser(url);
    }
    loop {
        tokio::select! {
            _ = tokio::signal::ctrl_c() => {
                if let Some(id) = &started.login_id { let _ = chatgpt.cancel(id); }
                bail!("sign-in cancelled");
            }
            _ = tokio::time::sleep(std::time::Duration::from_millis(300)) => {}
        }
        let status = chatgpt.status()?;
        match status.state {
            ChatGptLoginState::Pending => continue,
            ChatGptLoginState::Connected => {
                println!("signed in to ChatGPT");
                if let Some(connection) = &status.connection {
                    let home = setup::home()?;
                    choose_model(&server, &home, &connection.provider_id, model, interactive).await?;
                }
                return Ok(0);
            }
            _ => bail!("{}", status.message.unwrap_or_else(|| "the ChatGPT sign-in did not complete".into())),
        }
    }
}

pub async fn login(overrides: &[String], args: LoginArgs) -> anyhow::Result<u8> {
    let LoginArgs { provider, key_stdin, model, base_url, api, no_key } = args;
    let interactive = std::io::stdin().is_terminal() && !key_stdin;
    if provider.as_deref() == Some(CHATGPT) {
        return chatgpt_login(overrides, model, interactive).await;
    }
    let home = setup::home()?;
    // Before the service starts, so that it knows the new provider.
    let mut undo = None;
    if let Some(base_url) = &base_url {
        let id = provider.as_deref().context("--base-url needs a name: her login NAME --base-url URL")?;
        let api = api.as_deref().unwrap_or("openai-chat");
        let builtin = her_llm::Catalog::builtin()?;
        if builtin.providers.contains_key(id) {
            bail!("{id} is a built-in provider; pick another name for your service");
        }
        undo = Some(add_service(&home, id, base_url, api, no_key, model.as_deref())?);
        println!("added {id} to {}", shorten(&home, &home.models_file()));
    } else if api.is_some() || no_key {
        bail!("--api and --no-key go with --base-url");
    }
    let (server, _guard) = cmd_serve::start(overrides)?;
    server.refresh_local_models().await;
    let listing = {
        let server = server.clone();
        tokio::task::spawn_blocking(move || server.list_models()).await?
    };
    let provider_id = match provider {
        Some(provider) => provider,
        None if interactive => {
            let picked = ask_provider(&listing)?;
            if picked == CHATGPT {
                drop(server);
                return chatgpt_login(overrides, model, interactive).await;
            }
            picked
        }
        None => bail!("no terminal to ask in: name the provider, as in her login deepseek --key-stdin"),
    };
    let Some(config) = server.models().provider(&provider_id) else {
        let known: Vec<&str> = listing.providers.iter().map(|p| p.id.as_str()).collect();
        bail!("unknown provider `{provider_id}`; known: {}, chatgpt. A compatible service of your own: her login NAME --base-url URL", known.join(", "));
    };
    match &config.auth {
        AuthConfig::None => {
            if !listing.models.iter().any(|m| m.provider == provider_id && m.available) && model.is_none() {
                eprintln!("{} did not list any models. Is it running? To name a model anyway, add --model", config.name);
            } else {
                println!("{} needs no key", config.name);
            }
        }
        AuthConfig::ApiKey { env, .. } => {
            if let Some(plan) = &config.plan {
                eprintln!("Get the key at {}", plan.key_url);
            }
            let key = if key_stdin {
                let mut text = String::new();
                std::io::stdin().lock().read_to_string(&mut text).context("cannot read the key from standard input")?;
                text.trim().to_string()
            } else if interactive {
                read_hidden(&format!("Paste the key of {} (not shown): ", config.name))?
            } else {
                bail!("no terminal to ask in: pipe the key and add --key-stdin. A key never goes in an argument, where `ps` and the shell's history keep it");
            };
            if key.is_empty() {
                bail!("the key is empty");
            }
            match server.set_key(&provider_id, &key).await {
                Ok(result) => {
                    let store = her_llm::credentials::store_name().unwrap_or("credential store");
                    match (result.verified, result.models) {
                        (true, Some(models)) => println!("key accepted, {models} models; saved it in the {store}"),
                        _ => println!("saved the key in the {store}; it could not be checked now"),
                    }
                }
                Err(err) => {
                    if let Some(before) = undo.take() {
                        match before {
                            Some(bytes) => her_core::config_edit::replace_file(&home.models_file(), &bytes)?,
                            None => {
                                let _ = std::fs::remove_file(home.models_file());
                            }
                        }
                    }
                    if err.kind() == Some("key_rejected") {
                        return Err(KeyRejected(err.message).into());
                    }
                    bail!("{}", err.message);
                }
            }
            if let Some(variable) = env.iter().find(|name| std::env::var(name).is_ok_and(|v| !v.trim().is_empty())) {
                eprintln!("note: {variable} is set and wins over the saved key until you unset it");
            }
        }
        _ => bail!("{provider_id} is set up in models.toml or as a connection, not here"),
    }
    choose_model(&server, &home, &provider_id, model, interactive).await?;
    if interactive {
        println!("Try it: her \"say hello\"");
    }
    Ok(0)
}

pub async fn logout(overrides: &[String], provider: &str, forget: bool) -> anyhow::Result<u8> {
    if provider == CHATGPT {
        let (server, _guard) = cmd_serve::start(overrides)?;
        let chatgpt = server.connections().chatgpt.clone();
        if forget {
            chatgpt.forget().await?;
            println!("signed out of ChatGPT and forgot this registration");
        } else {
            chatgpt.logout().await?;
            println!("signed out of ChatGPT");
        }
        return Ok(0);
    }
    if forget {
        bail!("--forget is for chatgpt");
    }
    let (server, _guard) = cmd_serve::start(overrides)?;
    let config = server.models().provider(provider).with_context(|| format!("unknown provider `{provider}`"))?;
    server.delete_key(provider).map_err(|err| anyhow::anyhow!("{}", err.message))?;
    println!("removed the saved key of {}", config.name);
    if let Some(variable) = config.auth.env_vars().iter().find(|name| std::env::var(name).is_ok_and(|v| !v.trim().is_empty())) {
        eprintln!("note: {variable} is still set, so {} keeps working", config.name);
    }
    Ok(0)
}
