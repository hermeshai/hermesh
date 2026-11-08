//! The maintainers' half of signed releases (standalone-cli-design §13.6), kept out of `her`.
//!
//! `keygen` prints a new seed and its public key, each on its own line. `sign FILE` reads the
//! base64 seed from standard input and prints the signature of FILE, as `her-manifest.json.sig`
//! holds it. `public` reads the seed from standard input and prints its public key.

use base64::Engine;
use ring::rand::SecureRandom;
use ring::signature::Ed25519KeyPair;
use ring::signature::KeyPair;
use std::io::Read;

fn pair_from_stdin() -> Ed25519KeyPair {
    let mut text = String::new();
    std::io::stdin()
        .read_to_string(&mut text)
        .expect("the seed on standard input");
    let seed = base64::engine::general_purpose::STANDARD
        .decode(text.trim())
        .expect("a base64 seed");
    Ed25519KeyPair::from_seed_unchecked(&seed).expect("a 32-byte seed")
}

#[allow(clippy::print_stdout, clippy::print_stderr)] // a CLI: output on stdout, usage on stderr
fn main() {
    let base64 = base64::engine::general_purpose::STANDARD;
    let args: Vec<String> = std::env::args().skip(1).collect();
    match args
        .iter()
        .map(String::as_str)
        .collect::<Vec<_>>()
        .as_slice()
    {
        ["keygen"] => {
            let mut seed = [0u8; 32];
            ring::rand::SystemRandom::new()
                .fill(&mut seed)
                .expect("random bytes");
            let pair = Ed25519KeyPair::from_seed_unchecked(&seed).expect("a 32-byte seed");
            println!("{}", base64.encode(seed));
            println!("{}", base64.encode(pair.public_key().as_ref()));
        }
        ["public"] => println!("{}", base64.encode(pair_from_stdin().public_key().as_ref())),
        ["sign", file] => {
            let bytes = std::fs::read(file).expect("the file to sign");
            println!("{}", base64.encode(pair_from_stdin().sign(&bytes).as_ref()));
        }
        _ => {
            eprintln!("usage: release_sign keygen | public | sign FILE");
            std::process::exit(2);
        }
    }
}
