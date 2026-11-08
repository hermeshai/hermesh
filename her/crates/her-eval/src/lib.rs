//! Evaluation of the browser tools with real models (browser-eval.md).
//!
//! Each case is a directory: `case.toml` (the task and its checks) and the files of a small
//! site. A run serves the site on loopback, gives the task to `her exec` with only the
//! browser tools, and judges by what the site received and by the final answer.

pub mod case;
pub mod report;
pub mod run;
pub mod site;

pub use case::Case;
pub use run::RunResult;
pub use run::Runner;
