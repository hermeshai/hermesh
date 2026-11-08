//! TLS for model requests: the operating system verifies server certificates, so system and
//! corporate roots are trusted. The verifier is built for the first handshake rather than at
//! start-up: on Linux building it reads and parses the whole system trust store.

use rustls::DigitallySignedStruct;
use rustls::SignatureScheme;
use rustls::client::danger::HandshakeSignatureValid;
use rustls::client::danger::ServerCertVerified;
use rustls::client::danger::ServerCertVerifier;
use rustls::crypto::CryptoProvider;
use rustls::crypto::verify_tls12_signature;
use rustls::crypto::verify_tls13_signature;
use rustls::pki_types::CertificateDer;
use rustls::pki_types::ServerName;
use rustls::pki_types::UnixTime;
use std::sync::Arc;
use std::sync::OnceLock;

pub fn client_config() -> Result<rustls::ClientConfig, rustls::Error> {
    let provider = Arc::new(rustls::crypto::ring::default_provider());
    let verifier = Arc::new(LazyVerifier {
        provider: provider.clone(),
        platform: Arc::new(OnceLock::new()),
        #[cfg(not(target_vendor = "apple"))]
        warmup: std::sync::Once::new(),
    });
    config_with(verifier)
}

fn config_with(verifier: Arc<LazyVerifier>) -> Result<rustls::ClientConfig, rustls::Error> {
    let mut config = rustls::ClientConfig::builder_with_provider(verifier.provider.clone())
        .with_safe_default_protocol_versions()?
        .dangerous()
        .with_custom_certificate_verifier(verifier)
        .with_no_client_auth();
    // A preconfigured TLS config gets no ALPN from reqwest; offer HTTP/2 like the default does.
    config.alpn_protocols = vec![b"h2".to_vec(), b"http/1.1".to_vec()];
    // The default cache reserves room for 256 server names up front (14 KiB). her resumes
    // sessions with a model provider and a few MCP or catalog hosts.
    config.resumption = rustls::client::Resumption::in_memory_sessions(32);
    Ok(config)
}

/// The platform verifier, created on first use. Handshake signatures need no trust store and
/// are checked the same way the platform verifier checks them.
#[derive(Debug)]
struct LazyVerifier {
    provider: Arc<CryptoProvider>,
    platform: Arc<OnceLock<Result<rustls_platform_verifier::Verifier, rustls::Error>>>,
    #[cfg(not(target_vendor = "apple"))]
    warmup: std::sync::Once,
}

impl LazyVerifier {
    fn platform(&self) -> Result<&rustls_platform_verifier::Verifier, rustls::Error> {
        self.platform
            .get_or_init(|| rustls_platform_verifier::Verifier::new(self.provider.clone()))
            .as_ref()
            .map_err(Clone::clone)
    }
}

impl ServerCertVerifier for LazyVerifier {
    fn verify_server_cert(
        &self,
        end_entity: &CertificateDer<'_>,
        intermediates: &[CertificateDer<'_>],
        server_name: &ServerName<'_>,
        ocsp_response: &[u8],
        now: UnixTime,
    ) -> Result<ServerCertVerified, rustls::Error> {
        self.platform()?
            .verify_server_cert(end_entity, intermediates, server_name, ocsp_response, now)
    }

    fn verify_tls12_signature(
        &self,
        message: &[u8],
        cert: &CertificateDer<'_>,
        dss: &DigitallySignedStruct,
    ) -> Result<HandshakeSignatureValid, rustls::Error> {
        verify_tls12_signature(message, cert, dss, &self.provider.signature_verification_algorithms)
    }

    fn verify_tls13_signature(
        &self,
        message: &[u8],
        cert: &CertificateDer<'_>,
        dss: &DigitallySignedStruct,
    ) -> Result<HandshakeSignatureValid, rustls::Error> {
        verify_tls13_signature(message, cert, dss, &self.provider.signature_verification_algorithms)
    }

    fn supported_verify_schemes(&self) -> Vec<SignatureScheme> {
        // Rustls asks for these while constructing ClientHello. Start loading system roots
        // only once a TLS handshake actually begins: plain HTTP must not spawn a thread or
        // parse the certificate store. HTTPS still overlaps that work with the handshake.
        #[cfg(not(target_vendor = "apple"))]
        self.warmup.call_once(|| {
            let (platform, provider) = (self.platform.clone(), self.provider.clone());
            std::thread::spawn(move || {
                let _ = platform.get_or_init(|| rustls_platform_verifier::Verifier::new(provider));
            });
        });
        self.provider.signature_verification_algorithms.supported_schemes()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn verifier() -> Arc<LazyVerifier> {
        Arc::new(LazyVerifier {
            provider: Arc::new(rustls::crypto::ring::default_provider()),
            platform: Arc::new(OnceLock::new()),
            #[cfg(not(target_vendor = "apple"))]
            warmup: std::sync::Once::new(),
        })
    }

    #[tokio::test]
    async fn plain_http_never_initializes_the_trust_store() {
        let server = wiremock::MockServer::start().await;
        wiremock::Mock::given(wiremock::matchers::method("GET"))
            .respond_with(wiremock::ResponseTemplate::new(200))
            .mount(&server)
            .await;
        let verifier = verifier();
        let client = reqwest::Client::builder()
            .no_proxy()
            .use_preconfigured_tls(config_with(verifier.clone()).unwrap())
            .build()
            .unwrap();
        assert!(client.get(server.uri()).send().await.unwrap().status().is_success());
        assert!(verifier.platform.get().is_none());
        #[cfg(not(target_vendor = "apple"))]
        assert!(!verifier.warmup.is_completed());
    }

    #[test]
    fn certificate_verification_is_still_required_without_warmup() {
        let verifier = verifier();
        assert!(verifier.verify_server_cert(
            &CertificateDer::from(vec![0, 1, 2]),
            &[],
            &ServerName::try_from("localhost").unwrap(),
            &[],
            UnixTime::now(),
        ).is_err());
        assert!(verifier.platform.get().is_some());
    }

    #[test]
    fn handshake_starts_warmup_and_preserves_signature_schemes() {
        let verifier = verifier();
        let expected = verifier.provider.signature_verification_algorithms.supported_schemes();
        assert_eq!(verifier.supported_verify_schemes(), expected);
        assert_eq!(verifier.supported_verify_schemes(), expected);
        #[cfg(not(target_vendor = "apple"))]
        assert!(verifier.warmup.is_completed());
    }
}
