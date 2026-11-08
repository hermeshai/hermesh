//! The document worker alone, for the PDF component on Linux (standalone-cli-design §13.3): `her`
//! there is static and cannot load a library, so this program sits beside `libpdfium.so`.

#[allow(clippy::print_stderr)] // the worker reports its fatal error to the parent on stderr
fn main() -> std::process::ExitCode {
    match her_docs::engine::serve(std::io::stdin().lock(), std::io::stdout().lock()) {
        Ok(()) => std::process::ExitCode::SUCCESS,
        Err(err) => {
            eprintln!("error: {err}");
            std::process::ExitCode::FAILURE
        }
    }
}
