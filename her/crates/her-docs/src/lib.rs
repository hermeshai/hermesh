//! Documents opened beside a conversation (docs/desktop/document-editor-design.md): DocStore,
//! the layout analysis, and the PDF backend on PDFium, which runs in a worker process
//! (`her doc-worker`).

mod atomic;
pub mod autofill;
pub mod deck;
pub mod engine;
pub mod kind;
pub mod layout;
pub mod sheet;
pub mod store;
pub mod word;
pub mod worker;

pub use engine::Rendered;
pub use kind::DocKind;
pub use store::Doc;
pub use store::DocError;
pub use store::DocStore;
pub use store::Hit;
pub use worker::Launcher;

/// `$HER_HOME`, else `~/.her`.
pub(crate) fn her_home() -> Option<std::path::PathBuf> {
    match std::env::var_os("HER_HOME") {
        Some(home) if !home.is_empty() => Some(std::path::PathBuf::from(home)),
        _ => std::env::var_os("HOME").map(|home| std::path::PathBuf::from(home).join(".her")),
    }
}

/// The `pdf` component of this version. Another version's is not used: the worker and the program
/// that talks to it are released together.
pub(crate) fn pdf_addon_dir() -> Option<std::path::PathBuf> {
    her_home().map(|home| home.join("addons").join("pdf").join(env!("CARGO_PKG_VERSION")))
}
