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

#[cfg(windows)]
fn read_hidden(question: &str) -> anyhow::Result<String> {
    use std::os::windows::io::AsRawHandle;
    use windows_sys::Win32::Foundation::HANDLE;
    use windows_sys::Win32::System::Console::ENABLE_ECHO_INPUT;
    use windows_sys::Win32::System::Console::GetConsoleMode;
    use windows_sys::Win32::System::Console::SetConsoleMode;

    eprint!("{question}");
    std::io::stderr().flush()?;
    let console = std::io::stdin().as_raw_handle() as HANDLE;
    let mut mode = 0u32;
    // SAFETY: standard input's handle, open for the life of the process; `mode` outlives the call.
    let original = (unsafe { GetConsoleMode(console, &mut mode) } != 0).then_some(mode);
    if let Some(original) = original {
        // SAFETY: the same handle, and the mode read from it less echo.
        unsafe { SetConsoleMode(console, original & !ENABLE_ECHO_INPUT) };
    }
    let mut line = String::new();
    let read = std::io::stdin().lock().read_line(&mut line);
    if let Some(original) = original {
        // SAFETY: as above.
        unsafe { SetConsoleMode(console, original) };
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
    let started = chatgpt.start(None).await?;
    if let Some(url) = &started.authorize_url {
        // Print the address for terminals without a browser; never launch a shell with it.
        eprintln!("Sign in to ChatGPT in your browser:\n{url}");
        crate::cmd_customize::open_browser(url);
    }
    let pasted = crate::cmd_customize::take_pasted_redirect(started.authorize_url.as_deref().unwrap_or_default());
    tokio::pin!(pasted);
    loop {
        tokio::select! {
            _ = tokio::signal::ctrl_c() => {
                if let Some(id) = &started.login_id { let _ = chatgpt.cancel(id); }
                bail!("sign-in cancelled");
            }
            () = &mut pasted => {}
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

/// The names of the protocols, as people know them.
fn protocol_name(api: &str) -> &str {
    match api {
        "openai-chat" => "OpenAI Chat Completions",
        "anthropic-messages" => "Anthropic Messages",
        "openai-responses" => "OpenAI Responses",
        other => other,
    }
}

/// `her login NAME --base-url URL`: probes the service, saves it with its key and offers its
/// models (docs/kernel/custom-endpoint-design.md §3.2). The kernel's `providers/add` does the work.
async fn add_service(overrides: &[String], base_url: String, args: LoginArgs, interactive: bool) -> anyhow::Result<u8> {
    let LoginArgs { provider, key_stdin, model, api, no_key, .. } = args;
    let name = provider.context("--base-url needs a name: her login NAME --base-url URL")?;
    if her_llm::Catalog::builtin()?.providers.contains_key(&name) {
        bail!("{name} is a built-in provider; pick another name for your service");
    }
    let key = if no_key {
        None
    } else if key_stdin {
        let mut text = String::new();
        std::io::stdin().lock().read_to_string(&mut text).context("cannot read the key from standard input")?;
        Some(text.trim().to_string()).filter(|k| !k.is_empty())
    } else if interactive {
        Some(read_hidden(&format!("Paste the key of {name} (not shown; Enter if it takes none): "))?).filter(|k| !k.is_empty())
    } else {
        None
    };
    let (server, _guard) = cmd_serve::start(overrides)?;
    let home = setup::home()?;
    // The same name again is the same service: probed again with what was given, not a second one.
    let again = her_llm::Catalog::custom_providers(&home.models_file()).is_ok_and(|ps| ps.iter().any(|p| p.id == name));
    let result = if again {
        let params = her_protocol::api::ProvidersUpdateParams { id: name.clone(), name: None, base_url: Some(base_url), api_key: key.clone(), api };
        server.update_provider(params).await
    } else {
        let named = model.as_deref().map(|m| m.strip_prefix(&format!("{name}/")).unwrap_or(m).to_string());
        let params = her_protocol::api::ProvidersAddParams { base_url, api_key: key.clone(), name: Some(name.clone()), api, models: named.into_iter().collect() };
        server.add_provider(params).await
    };
    let added = match result {
        Ok(added) => added,
        Err(err) if err.kind() == Some("key_rejected") => return Err(KeyRejected(err.message).into()),
        Err(err) => {
            let reason = err.data.as_ref().and_then(|d| d.get("reason")).and_then(|r| r.as_str());
            match reason {
                Some("no_models") => bail!("{name} gave no model list: name one with --model"),
                Some("key_required") => bail!("{name} needs a key: pipe it with --key-stdin, or run this at a terminal"),
                _ => bail!("{}", err.message),
            }
        }
    };
    let probe = &added.probe;
    let found: Vec<String> = probe.protocols.iter().map(|p| format!("{} ({})", protocol_name(&p.api), p.base_url)).collect();
    println!("found {}", found.join(", "));
    if probe.no_list {
        println!("it lists no models; using the one you named");
    } else {
        println!("{} models, {} with known details", probe.models, probe.known);
    }
    if probe.protocols.iter().any(|p| p.api == "anthropic-messages") && probe.protocols.iter().any(|p| p.api == "openai-chat") {
        println!("Claude models use Anthropic Messages, the others Chat Completions");
    }
    let id = added.provider.id.clone();
    if again {
        println!("updated {id} in {}", shorten(&home, &home.models_file()));
    } else {
        println!("added {id} to {}", shorten(&home, &home.models_file()));
    }
    if key.is_some() {
        let store = her_llm::credentials::store_name().unwrap_or("credential store");
        println!("saved the key in the {store}");
    }
    if probe.insecure {
        eprintln!("note: the address is http://, so the key travels unencrypted");
    }
    if let Some(builtin) = &probe.builtin {
        eprintln!("note: this is the address of {builtin}; `her login {builtin}` gives its own thinking options and prices");
    }
    if probe.unsure.iter().any(|a| a == "anthropic-messages") {
        eprintln!("note: could not tell whether it speaks Anthropic Messages; add --api anthropic-messages to use only that");
    }
    match probe.note.as_deref() {
        Some("azure_deployments") => eprintln!("note: Azure requests name a deployment: add yours with --model"),
        Some("bedrock_models") => eprintln!("note: Bedrock lists no models here: name them with --model"),
        Some("vertex_token") => eprintln!("note: Vertex wants a token that expires hourly: use api_key_command in models.toml"),
        Some("portkey_headers") => eprintln!("note: Portkey routes by its x-portkey-* headers: add them as headers in models.toml"),
        _ => {}
    }
    choose_model(&server, &home, &id, model, interactive).await?;
    if interactive {
        println!("Try it: her \"say hello\"");
    }
    Ok(0)
}

pub async fn login(overrides: &[String], mut args: LoginArgs) -> anyhow::Result<u8> {
    let interactive = std::io::stdin().is_terminal() && !args.key_stdin;
    if args.provider.as_deref() == Some(CHATGPT) {
        return chatgpt_login(overrides, args.model, interactive).await;
    }
    if let Some(base_url) = args.base_url.take() {
        return add_service(overrides, base_url, args, interactive).await;
    }
    let LoginArgs { provider, key_stdin, model, api, no_key, .. } = args;
    let home = setup::home()?;
    if api.is_some() || no_key {
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

/// What `her models set` says about a model (custom-endpoint-design §7.3).
pub struct SetModel {
    pub model: String,
    pub like: Option<String>,
    pub context: Option<String>,
    pub images: Option<bool>,
    pub reasoning: Option<bool>,
    pub clear: bool,
}

/// `128000`, `128k`, `1m`.
fn tokens(text: &str) -> anyhow::Result<u64> {
    let t = text.trim().to_ascii_lowercase();
    let (number, unit) = match t.strip_suffix('k') {
        Some(n) => (n, 1_000.0),
        None => match t.strip_suffix('m') {
            Some(n) => (n, 1_000_000.0),
            None => (t.as_str(), 1.0),
        },
    };
    let value: f64 = number.trim().parse().map_err(|_| anyhow::anyhow!("--context {text}: a number of tokens, as 131072, 128k or 1m"))?;
    if !value.is_finite() || value < 1.0 {
        bail!("--context {text}: a number of tokens, as 131072, 128k or 1m");
    }
    Ok((value * unit) as u64)
}

pub async fn configure_model(overrides: &[String], args: SetModel) -> anyhow::Result<u8> {
    let SetModel { model, like, context, images, reasoning, clear } = args;
    if !clear && like.is_none() && context.is_none() && images.is_none() && reasoning.is_none() {
        bail!("say what it is like: --like MODEL, --context TOKENS, --images on|off, --reasoning on|off, or --clear");
    }
    let params = her_protocol::api::ModelsConfigureParams {
        model: model.clone(),
        canonical: like.map(Some),
        context_window: context.as_deref().map(tokens).transpose()?.map(Some),
        images: images.map(Some),
        reasoning: reasoning.map(Some),
        clear,
    };
    let (server, _guard) = cmd_serve::start(overrides)?;
    let result = tokio::task::spawn_blocking(move || server.configure_model(params)).await?.map_err(|err| anyhow::anyhow!("{}", err.message))?;
    let m = result.model;
    let mut said = Vec::new();
    if let Some(canonical) = &m.canonical {
        said.push(format!("like {}", canonical.name));
    }
    said.push(format!("{}k window", m.context_window / 1000));
    if m.images {
        said.push("images".into());
    }
    if m.reasoning {
        said.push("thinks".into());
    }
    println!("{}: {}", m.id, said.join(", "));
    Ok(0)
}

pub async fn logout(overrides: &[String], provider: &str, forget: bool, remove: bool) -> anyhow::Result<u8> {
    if remove {
        let (server, _guard) = cmd_serve::start(overrides)?;
        let id = provider.to_string();
        tokio::task::spawn_blocking(move || server.remove_provider(&id)).await?.map_err(|err| anyhow::anyhow!("{}", err.message))?;
        println!("removed {provider}: its settings, key and model list");
        return Ok(0);
    }
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
