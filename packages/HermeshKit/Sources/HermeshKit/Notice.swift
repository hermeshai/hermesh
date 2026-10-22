import CryptoKit
import Foundation
import HermeshProto
import Security

/// The iPhone's notice key: an X25519 key that machines seal lock-screen
/// notices to (identity.v1.PushRegistration). It lives in the keychain
/// group the app shares with its notification extension, readable after
/// the first unlock (notices arrive while the phone is locked).
public enum NoticeKey {
    /// The keychain group, from the bundle's HermeshKeychainGroup
    /// ($(AppIdentifierPrefix)com.hermesh.ios.shared).
    static var group: String? { Bundle.main.object(forInfoDictionaryKey: "HermeshKeychainGroup") as? String }

    private static func query() -> [CFString: Any] {
        var q: [CFString: Any] = [kSecClass: kSecClassGenericPassword, kSecAttrService: "com.hermesh.notice", kSecAttrAccount: "notice"]
        if let group, !group.hasPrefix("$") { q[kSecAttrAccessGroup] = group }
        return q
    }

    /// The key, created on first use.
    public static func load() throws -> Curve25519.KeyAgreement.PrivateKey {
        var q = query()
        q[kSecReturnData] = true
        var item: CFTypeRef?
        if SecItemCopyMatching(q as CFDictionary, &item) == errSecSuccess, let d = item as? Data {
            return try Curve25519.KeyAgreement.PrivateKey(rawRepresentation: d)
        }
        let key = Curve25519.KeyAgreement.PrivateKey()
        var add = query()
        add[kSecValueData] = key.rawRepresentation
        add[kSecAttrAccessible] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        let status = SecItemAdd(add as CFDictionary, nil)
        guard status == errSecSuccess || status == errSecDuplicateItem else {
            throw NSError(domain: NSOSStatusErrorDomain, code: Int(status))
        }
        return key
    }

    /// Opens a sealed notice: the HPKE encapsulated key followed by the
    /// ciphertext (identity.SealNotice).
    public static func open(_ sealed: Data) throws -> Hermesh_Session_V1_Notice {
        try open(sealed, with: load())
    }

    static func open(_ sealed: Data, with key: Curve25519.KeyAgreement.PrivateKey) throws -> Hermesh_Session_V1_Notice {
        let suite = HPKE.Ciphersuite(kem: .Curve25519_HKDF_SHA256, kdf: .HKDF_SHA256, aead: .AES_GCM_128)
        guard sealed.count > 32 else { throw CryptoKitError.incorrectParameterSize }
        var r = try HPKE.Recipient(privateKey: key, ciphersuite: suite, info: Data("hermesh-notice-v1".utf8),
                                   encapsulatedKey: sealed.prefix(32))
        return try Hermesh_Session_V1_Notice(serializedBytes: r.open(sealed.dropFirst(32)))
    }
}
