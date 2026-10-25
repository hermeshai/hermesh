import CryptoKit
import Foundation
import HermeshKit
import LocalAuthentication
import Security

/// Holds the root keys, one per profile hermeshd keeps (one per owner this
/// Mac belongs to): creates them, signs with the one a request names, and
/// deletes one hermeshd no longer uses. Each signature is ASN.1 DER
/// ECDSA-P256 over SHA-256(message).
protocol RootKeyStore: Sendable {
    /// Creates a new root key, keeping the others, and returns its public
    /// key as SPKI DER.
    func create() throws -> Data
    /// Signs every message with the key keyID names; context is the
    /// authenticated LAContext the key needs (nil for the test key).
    func sign(_ messages: [Data], keyID: String, context: LAContext?) throws -> [Data]
    /// Deletes the key keyID names; one that is not there is already gone.
    func delete(keyID: String) throws
}

struct SignerError: LocalizedError {
    let message: String
    var errorDescription: String? { message }
}

/// The key id hermeshd names a root key by (identity.KeyID).
func keyID(spki: Data) -> String { SigningPreview.keyID(spki) }

/// The real root keys: in the Secure Enclave, and every use needs Touch ID
/// or the login password. They are kept in the data-protection Keychain,
/// where only binaries signed with our keychain group can use them. Builds
/// without that entitlement (no provisioning profile) keep each key's
/// Secure Enclave handle in a file instead.
struct SecureEnclaveKey: RootKeyStore {
    static let tag = Data("com.hermesh.signer.root".utf8)
    let blobDir: URL

    init(blobDir: URL = Paths.appSupport.appendingPathComponent("app/root-keys")) { self.blobDir = blobDir }

    /// Where the keys are kept, for the log.
    var location: String {
        if let keys = try? keychainKeys(nil), !keys.isEmpty { return "Keychain (Secure Enclave), \(keys.count) key(s)" }
        if let files = try? FileManager.default.contentsOfDirectory(atPath: blobDir.path), !files.isEmpty {
            return "\(blobDir.path) (Secure Enclave handles), \(files.count) key(s)"
        }
        return "none yet"
    }

    func create() throws -> Data {
        guard SecureEnclave.isAvailable else { throw SignerError(message: "This Mac has no Secure Enclave") }
        var err: Unmanaged<CFError>?
        guard let access = SecAccessControlCreateWithFlags(nil, kSecAttrAccessibleWhenUnlockedThisDeviceOnly, [.privateKeyUsage, .userPresence], &err) else {
            throw err!.takeRetainedValue() as Error
        }
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
            log("root key created in the Keychain (Secure Enclave)")
            return try Self.spki(key)
        }
        let e = err!.takeRetainedValue() as Error
        guard (e as NSError).code == Int(errSecMissingEntitlement) else { throw e }
        // A development build: no keychain group to store the key in.
        let key = try SecureEnclave.P256.Signing.PrivateKey(accessControl: access)
        let spki = key.publicKey.derRepresentation
        try FileManager.default.createDirectory(at: blobDir, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        let url = blob(keyID(spki: spki))
        try key.dataRepresentation.write(to: url, options: .atomic)
        try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: url.path)
        log("no keychain entitlement (errSecMissingEntitlement): root key created as a Secure Enclave handle in \(url.path)")
        return spki
    }

    func sign(_ messages: [Data], keyID id: String, context: LAContext?) throws -> [Data] {
        if let key = try keychainKeys(context).first(where: { $0.id == id })?.key {
            return try messages.map { m in
                var err: Unmanaged<CFError>?
                guard let sig = SecKeyCreateSignature(key, .ecdsaSignatureMessageX962SHA256, m as CFData, &err) else {
                    throw err!.takeRetainedValue() as Error
                }
                return sig as Data
            }
        }
        let url = blob(id)
        guard FileManager.default.fileExists(atPath: url.path) else { throw SignerError(message: "This Mac does not have that root key") }
        let key = try SecureEnclave.P256.Signing.PrivateKey(dataRepresentation: Data(contentsOf: url), authenticationContext: context)
        return try messages.map { try key.signature(for: $0).derRepresentation }
    }

    func delete(keyID id: String) throws {
        if let key = try keychainKeys(nil).first(where: { $0.id == id })?.key {
            let status = SecItemDelete([kSecClass: kSecClassKey, kSecValueRef: key, kSecUseDataProtectionKeychain: true] as CFDictionary)
            guard status == errSecSuccess || status == errSecItemNotFound else { throw NSError(domain: NSOSStatusErrorDomain, code: Int(status)) }
        }
        try? FileManager.default.removeItem(at: blob(id))
        log("root key \(id) deleted")
    }

    /// Before profiles, the one key's handle was app/root-key.se: it moves
    /// beside the others, under its key id. Reading its public key needs no
    /// authentication.
    func adoptLegacyBlob() {
        let old = blobDir.deletingLastPathComponent().appendingPathComponent("root-key.se")
        guard let data = try? Data(contentsOf: old) else { return }
        do {
            let key = try SecureEnclave.P256.Signing.PrivateKey(dataRepresentation: data)
            try FileManager.default.createDirectory(at: blobDir, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
            let url = blob(keyID(spki: key.publicKey.derRepresentation))
            try FileManager.default.moveItem(at: old, to: url)
            log("moved the root key handle to \(url.path)")
        } catch {
            log("moving the root key handle \(old.path): \(error.localizedDescription)")
        }
    }

    private func blob(_ id: String) -> URL { blobDir.appendingPathComponent("\(id).se") }

    private static func spki(_ key: SecKey) throws -> Data {
        var err: Unmanaged<CFError>?
        guard let pub = SecKeyCopyPublicKey(key), let x963 = SecKeyCopyExternalRepresentation(pub, &err) else {
            throw err!.takeRetainedValue() as Error
        }
        return try P256.Signing.PublicKey(x963Representation: x963 as Data).derRepresentation
    }

    /// The Keychain keys with their ids, or none when there are none (or no
    /// entitlement to look). Used with an authenticated context, a key
    /// signs without asking.
    private func keychainKeys(_ context: LAContext?) throws -> [(id: String, key: SecKey)] {
        var q: [CFString: Any] = [kSecClass: kSecClassKey, kSecAttrApplicationTag: Self.tag, kSecAttrKeyType: kSecAttrKeyTypeECSECPrimeRandom,
                                  kSecUseDataProtectionKeychain: true, kSecReturnRef: true, kSecMatchLimit: kSecMatchLimitAll]
        if let context { q[kSecUseAuthenticationContext] = context }
        var items: CFTypeRef?
        switch SecItemCopyMatching(q as CFDictionary, &items) {
        case errSecSuccess:
            return try (items as! [SecKey]).map { (keyID(spki: try Self.spki($0)), $0) }
        case errSecItemNotFound, errSecMissingEntitlement: return []
        case let status: throw NSError(domain: NSOSStatusErrorDomain, code: Int(status))
        }
    }
}

/// Software keys in files (--test-key), for end-to-end tests: PKCS#8 PEM,
/// as identity.LoadKey reads them. The first is at path, and is the first
/// one created (tests read it from there); later ones are beside it.
struct TestKeyFile: RootKeyStore {
    let path: String

    /// Opens the key file, creating a key if there is none.
    init(path: String) throws {
        self.path = path
        if !FileManager.default.fileExists(atPath: path) { try write(P256.Signing.PrivateKey(), to: path) }
    }

    func create() throws -> Data {
        let handed = path + ".handed"
        if !FileManager.default.fileExists(atPath: handed) {
            FileManager.default.createFile(atPath: handed, contents: nil)
            return try load(path).publicKey.derRepresentation
        }
        let key = P256.Signing.PrivateKey()
        try write(key, to: "\(path).\(keyID(spki: key.publicKey.derRepresentation))")
        return key.publicKey.derRepresentation
    }

    func sign(_ messages: [Data], keyID id: String, context _: LAContext?) throws -> [Data] {
        let key = try load(file(id))
        return try messages.map { try key.signature(for: $0).derRepresentation }
    }

    func delete(keyID id: String) throws {
        try? FileManager.default.removeItem(atPath: try file(id))
    }

    /// The file of the key id names.
    private func file(_ id: String) throws -> String {
        if FileManager.default.fileExists(atPath: path), keyID(spki: try load(path).publicKey.derRepresentation) == id { return path }
        return "\(path).\(id)"
    }

    private func load(_ path: String) throws -> P256.Signing.PrivateKey {
        try P256.Signing.PrivateKey(pemRepresentation: String(decoding: Data(contentsOf: URL(fileURLWithPath: path)), as: UTF8.self))
    }

    private func write(_ key: P256.Signing.PrivateKey, to path: String) throws {
        try Data(key.pemRepresentation.utf8).write(to: URL(fileURLWithPath: path), options: .atomic)
        try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: path)
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
