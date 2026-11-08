// Cargo directives are the build script's output.
#![allow(clippy::print_stdout)]

#[path = "src/skills/system/bundle.rs"]
mod bundle;

fn main() {
    println!("cargo:rerun-if-changed=src/skills/system/bundle.rs");
    let mut digest = ring::digest::Context::new(&ring::digest::SHA256);
    for (path, content) in bundle::FILES {
        println!("cargo:rerun-if-changed=src/skills/system/{path}");
        digest.update(path.as_bytes());
        digest.update(&[0]);
        digest.update(content.as_bytes());
        digest.update(&[0]);
    }
    let fingerprint: String = digest.finish().as_ref()[..8].iter().map(|b| format!("{b:02x}")).collect();
    println!("cargo:rustc-env=HER_SYSTEM_SKILLS_FINGERPRINT={fingerprint}");
}
