import CryptoKit
import Foundation
import LocalAuthentication
import Security

/// Holds the owner root key: creates it (replacing any earlier one) and
/// signs with it. Each signature is ASN.1 DER ECDSA-P256 over
/// SHA-256(message).
protocol RootKeyStore: Sendable {
    /// Creates a new root key and returns its public key as SPKI DER.
    func create() throws -> Data
    /// Signs every message; context is the authenticated LAContext the key
    /// needs (nil for the test key).
    func sign(_ messages: [Data], context: LAContext?) throws -> [Data]
}

struct SignerError: LocalizedError {
    let message: String
    var errorDescription: String? { message }
}

/// The real root key: in the Secure Enclave, and every use needs Touch ID
/// or the login password. It is kept in the data-protection Keychain, where
/// only binaries signed with our keychain group can use it. Builds without
/// that entitlement (no provisioning profile) keep the Secure Enclave's
/// encrypted handle in a file instead.
struct SecureEnclaveKey: RootKeyStore {
    static let tag = Data("com.hermesh.signer.root".utf8)
    let blobURL: URL

    init(blobURL: URL = Paths.appSupport.appendingPathComponent("app/root-key.se")) { self.blobURL = blobURL }

    /// Which store holds the key now, for the log.
    var location: String {
        if (try? keychainKey(nil)) != nil { return "Keychain (Secure Enclave)" }
        if FileManager.default.fileExists(atPath: blobURL.path) { return "file \(blobURL.path) (Secure Enclave handle)" }
        return "none yet"
    }

    func create() throws -> Data {
        guard SecureEnclave.isAvailable else { throw SignerError(message: "This Mac has no Secure Enclave") }
        var err: Unmanaged<CFError>?
        guard let access = SecAccessControlCreateWithFlags(nil, kSecAttrAccessibleWhenUnlockedThisDeviceOnly, [.privateKeyUsage, .userPresence], &err) else {
            throw err!.takeRetainedValue() as Error
        }
        SecItemDelete(Self.query as CFDictionary)
        let attributes: [CFString: Any] = [
            kSecAttrKeyType: kSecAttrKeyTypeECSECPrimeRandom,
            kSecAttrKeySizeInBits: 256,
            kSecAttrTokenID: kSecAttrTokenIDSecureEnclave,
            kSecUseDataProtectionKeychain: true,
            kSecPrivateKeyAttrs: [
                kSecAttrIsPermanent: true,
                kSecAttrApplicationTag: Self.tag,
                kSecAttrAccessControl: access,
            ] as [CFString: Any],
        ]
        if let key = SecKeyCreateRandomKey(attributes as CFDictionary, &err) {
            try? FileManager.default.removeItem(at: blobURL)
            log("root key created in the Keychain (Secure Enclave)")
            guard let pub = SecKeyCopyPublicKey(key), let x963 = SecKeyCopyExternalRepresentation(pub, &err) else {
                throw err!.takeRetainedValue() as Error
            }
            return try P256.Signing.PublicKey(x963Representation: x963 as Data).derRepresentation
        }
        let e = err!.takeRetainedValue() as Error
        guard (e as NSError).code == Int(errSecMissingEntitlement) else { throw e }
        // A development build: no keychain group to store the key in.
        let key = try SecureEnclave.P256.Signing.PrivateKey(accessControl: access)
        try FileManager.default.createDirectory(at: blobURL.deletingLastPathComponent(), withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        try key.dataRepresentation.write(to: blobURL, options: .atomic)
        try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: blobURL.path)
        log("no keychain entitlement (errSecMissingEntitlement): root key created as a Secure Enclave handle in \(blobURL.path)")
        return key.publicKey.derRepresentation
    }

    func sign(_ messages: [Data], context: LAContext?) throws -> [Data] {
        if let key = try keychainKey(context) {
            return try messages.map { m in
                var err: Unmanaged<CFError>?
                guard let sig = SecKeyCreateSignature(key, .ecdsaSignatureMessageX962SHA256, m as CFData, &err) else {
                    throw err!.takeRetainedValue() as Error
                }
                return sig as Data
            }
        }
        guard FileManager.default.fileExists(atPath: blobURL.path) else { throw SignerError(message: "There is no root key on this Mac") }
        let key = try SecureEnclave.P256.Signing.PrivateKey(dataRepresentation: Data(contentsOf: blobURL), authenticationContext: context)
        return try messages.map { try key.signature(for: $0).derRepresentation }
    }

    private static var query: [CFString: Any] {
        [kSecClass: kSecClassKey, kSecAttrApplicationTag: tag, kSecAttrKeyType: kSecAttrKeyTypeECSECPrimeRandom, kSecUseDataProtectionKeychain: true]
    }

    /// The Keychain key, or nil when there is none (or no entitlement to
    /// look). Used with an authenticated context, it signs without asking.
    private func keychainKey(_ context: LAContext?) throws -> SecKey? {
        var q = Self.query
        q[kSecReturnRef] = true
        if let context { q[kSecUseAuthenticationContext] = context }
        var item: CFTypeRef?
        switch SecItemCopyMatching(q as CFDictionary, &item) {
        case errSecSuccess: return (item as! SecKey)
        case errSecItemNotFound, errSecMissingEntitlement: return nil
        case let status: throw NSError(domain: NSOSStatusErrorDomain, code: Int(status))
        }
    }
}

/// A software key in a file (--test-key), for end-to-end tests: PKCS#8
/// PEM, as identity.SaveKey writes it.
struct TestKeyFile: RootKeyStore {
    let path: String

    /// Opens the key file, creating a key if there is none.
    init(path: String) throws {
        self.path = path
        if !FileManager.default.fileExists(atPath: path) { _ = try create() }
    }

    func create() throws -> Data {
        let key = P256.Signing.PrivateKey()
        try Data(key.pemRepresentation.utf8).write(to: URL(fileURLWithPath: path), options: .atomic)
        try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: path)
        return key.publicKey.derRepresentation
    }

    func sign(_ messages: [Data], context _: LAContext?) throws -> [Data] {
        let key = try P256.Signing.PrivateKey(pemRepresentation: String(decoding: Data(contentsOf: URL(fileURLWithPath: path)), as: UTF8.self))
        return try messages.map { try key.signature(for: $0).derRepresentation }
    }
}

enum Paths {
    /// Hermesh's data directory; HERMESH_DATA_DIR as for hermeshd and the desktop app.
    static var appSupport: URL { dataDirectory(ProcessInfo.processInfo.environment) }

    static func dataDirectory(_ env: [String: String]) -> URL {
        if let d = env["HERMESH_DATA_DIR"], !d.isEmpty { return URL(fileURLWithPath: d) }
        return FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0].appendingPathComponent("Hermesh")
    }

    /// Must match SocketPath() in cmd/hermeshd.
    static var socket: String {
        let p = appSupport.appendingPathComponent("run/hermeshd.sock").path
        if p.utf8.count > 100 {
            return (NSTemporaryDirectory() as NSString).appendingPathComponent("hermesh-\(getuid())/hermeshd.sock")
        }
        return p
    }
}

/// Logs to stderr, which the app that started the signer keeps.
func log(_ message: String) {
    FileHandle.standardError.write(Data("hermesh-signer: \(message)\n".utf8))
}
