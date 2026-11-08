#![allow(clippy::print_stdout)]

include!("catalog/makers.rs");

fn main() {
    println!("cargo:rerun-if-changed=catalog/builtin.toml");
    println!("cargo:rerun-if-changed=catalog/makers.rs");
    println!("cargo:rerun-if-changed=catalog/public");
    // Keep TOML as the editable source. Convert its syntax once at build time; the runtime
    // deserializes the same typed catalog and merges the user's TOML on every launch.
    let source = std::fs::read_to_string("catalog/builtin.toml").expect("read built-in catalog");
    let catalog: toml::Value = toml::from_str(&source).expect("parse built-in catalog");
    let output = std::path::PathBuf::from(std::env::var_os("OUT_DIR").expect("OUT_DIR"));
    std::fs::write(
        output.join("builtin-catalog.json"),
        serde_json::to_vec(&catalog).expect("catalog JSON"),
    )
    .expect("write compiled catalog");
    // The hashes of the makers' model ids, sorted: most models a channel's row does not describe
    // are no maker's either, and this says so without touching the snapshots. No strings, whose
    // table would fault in several pages on a command's only lookup.
    let mut hashes: Vec<u32> = Vec::new();
    for maker in MAKERS {
        let text = std::fs::read_to_string(format!("catalog/public/{maker}.json")).expect("read a maker's snapshot");
        let file: serde_json::Value = serde_json::from_str(&text).expect("parse a maker's snapshot");
        for model in file["models"].as_array().expect("its models") {
            hashes.push(model_name_hash(model["id"].as_str().expect("a model's id")));
        }
    }
    hashes.sort_unstable();
    hashes.dedup();
    std::fs::write(
        output.join("maker-ids.rs"),
        format!("const MAKER_NAME_HASHES: [u32; {}] = {hashes:?};\n", hashes.len()),
    )
    .expect("write the makers' model ids");
}
