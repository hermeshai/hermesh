//! COSY catalog envelope, implemented natively. No SDK worker or local proxy.
//! Protocol reference: Magpie Qoder plugin (MIT), see crates/her-connections/licenses/README.md.
use crate::{ConnectionError, Result};
use base64::{Engine, engine::general_purpose::STANDARD};
use cbc::cipher::{BlockEncryptMut, KeyIvInit, block_padding::Pkcs7};
use md5::{Digest, Md5};
use rsa::{Pkcs1v15Encrypt, RsaPublicKey, pkcs8::DecodePublicKey};
use serde_json::json;
/// The client build the envelope names: the worker runtime bundled in Qoder's desktop App
/// (0.4.x). Raised when the App moves, in this one place.
pub(super) const VERSION: &str = "1.1.69";
const PUBLIC_KEY: &str = "-----BEGIN PUBLIC KEY-----\nMIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDA8iMH5c02LilrsERw9t6Pv5Nc\n4k6Pz1EaDicBMpdpxKduSZu5OANqUq8er4GM95omAGIOPOh+Nx0spthYA2BqGz+l\n6HRkPJ7S236FZz73In/KVuLnwI8JJ2CbuJap8kvheCCZpmAWpb/cPx/3Vr/J6I17\nXcW+ML9FoCI6AOvOzwIDAQAB\n-----END PUBLIC KEY-----";
/// Which request the envelope goes on. Qoder's App puts the machine id in every machine field
/// of an ordinary call (catalog, queue status); its chat leaves out the client address and the
/// device fields it has no device identity for, and asks for an event stream.
#[derive(Clone, Copy, PartialEq, Eq)]
pub(super) enum Kind {
    Request,
    Chat,
}

/// The envelope of a GET (no body).
pub(super) fn headers(
    url: &str,
    uid: &str,
    token: &str,
    machine: &str,
) -> Result<reqwest::header::HeaderMap> {
    signed(url, uid, token, machine, "", Kind::Request)
}

/// The envelope of a call whose body, as sent, is `body`: the signature covers it.
pub(super) fn signed(
    url: &str,
    uid: &str,
    token: &str,
    machine: &str,
    body: &str,
    kind: Kind,
) -> Result<reqwest::header::HeaderMap> {
    let plain = zeroize::Zeroizing::new(
        json!({"uid":uid,"aid":"","name":"","email":"","security_oauth_token":token}).to_string(),
    );
    let key = zeroize::Zeroizing::new(
        uuid::Uuid::new_v4().simple().to_string().as_bytes()[..16].to_vec(),
    );
    let encrypted = cbc::Encryptor::<aes::Aes128>::new_from_slices(&key, &key)
        .map_err(|_| ConnectionError::unsupported())?
        .encrypt_padded_vec_mut::<Pkcs7>(plain.as_bytes());
    let wrapped = RsaPublicKey::from_public_key_pem(PUBLIC_KEY)
        .map_err(|_| ConnectionError::unsupported())?
        .encrypt(&mut rand08::rngs::OsRng, Pkcs1v15Encrypt, &key)
        .map_err(|_| ConnectionError::unsupported())?;
    let wrapped = STANDARD.encode(wrapped);
    let payload=STANDARD.encode(json!({"version":"v1","requestId":uuid::Uuid::new_v4().to_string(),"info":STANDARD.encode(encrypted),"cosyVersion":VERSION,"ideVersion":""}).to_string());
    let ts = chrono::Utc::now().timestamp().to_string();
    // The signed path leaves out the gateway's `/algo` prefix, which some calls go without.
    let url = reqwest::Url::parse(url).map_err(|_| ConnectionError::unsupported())?;
    let path = url
        .path()
        .strip_prefix("/algo")
        .unwrap_or(url.path())
        .to_string();
    let input = zeroize::Zeroizing::new(format!("{payload}\n{wrapped}\n{ts}\n{body}\n{path}"));
    let signature = format!("{:x}", Md5::digest(input.as_bytes()));
    let mut headers = reqwest::header::HeaderMap::new();
    let chat = kind == Kind::Chat;
    for (name, value) in [
        (
            "Authorization",
            format!("Bearer COSY.{payload}.{signature}"),
        ),
        ("Cosy-Key", wrapped),
        ("Cosy-Date", ts),
        ("Cosy-User", uid.into()),
        ("Cosy-MachineId", machine.into()),
        ("Cosy-ClientIp", machine.into()),
        ("Cosy-MachineToken", machine.into()),
        ("Cosy-MachineType", "5".into()),
        ("Cosy-Version", VERSION.into()),
        ("Cosy-ClientType", "10".into()),
        ("Cosy-Business-Product", "app".into()),
        ("Cosy-Business-Type", "agent".into()),
        ("Cosy-Scene", "app".into()),
        ("Cosy-Data-Policy", "disagree".into()),
        ("Login-Version", "v2".into()),
        (
            "Cosy-MachineOS",
            if cfg!(target_arch = "aarch64") {
                "aarch64_darwin"
            } else {
                "x86_64_darwin"
            }
            .into(),
        ),
        (
            "Accept",
            if chat {
                "text/event-stream"
            } else {
                "application/json"
            }
            .into(),
        ),
    ] {
        if chat
            && matches!(
                name,
                "Cosy-ClientIp" | "Cosy-MachineToken" | "Cosy-MachineType"
            )
        {
            continue;
        }
        let mut value = reqwest::header::HeaderValue::from_str(&value)
            .map_err(|_| ConnectionError::unsupported())?;
        value.set_sensitive(true);
        headers.insert(
            reqwest::header::HeaderName::from_bytes(name.as_bytes())
                .map_err(|_| ConnectionError::unsupported())?,
            value,
        );
    }
    Ok(headers)
}
