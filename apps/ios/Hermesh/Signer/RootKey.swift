import CryptoKit
import Foundation
import LocalAuthentication
import Security

/// The owner root key of this iPhone. On a phone it lives in the Secure
/// Enclave and every use needs Face ID (or the passcode); the Simulator has
/// no Secure Enclave, so there it is a software key in the Keychain.
/// Signatures are ASN.1 DER ECDSA-P256 over SHA-256(message).
nonisolated enum RootKey {
    private static let tag = Data("com.hermesh.ios.root".utf8)

    /// A phone's Secure Enclave; the Simulator's can't make keys that need
    /// a passcode or Face ID it doesn't have.
    private static var useSecureEnclave: Bool {
        #if targetEnvironment(simulator)
        return false
        #else
        return SecureEnclave.isAvailable
        #endif
    }

    private static var query: [CFString: Any] {
        [kSecClass: kSecClassKey, kSecAttrApplicationTag: tag, kSecAttrKeyType: kSecAttrKeyTypeECSECPrimeRandom]
    }

    /// Creates a new root key, replacing any earlier one, and returns its
    /// public key (SPKI DER).
    static func create() throws -> Data {
        SecItemDelete(query as CFDictionary)
        var err: Unmanaged<CFError>?
        var attributes: [CFString: Any] = [kSecAttrKeyType: kSecAttrKeyTypeECSECPrimeRandom, kSecAttrKeySizeInBits: 256]
        if useSecureEnclave {
            guard let access = SecAccessControlCreateWithFlags(nil, kSecAttrAccessibleWhenUnlockedThisDeviceOnly, [.privateKeyUsage, .userPresence], &err) else {
                throw err!.takeRetainedValue() as Error
            }
            attributes[kSecAttrTokenID] = kSecAttrTokenIDSecureEnclave
            attributes[kSecPrivateKeyAttrs] = [kSecAttrIsPermanent: true, kSecAttrApplicationTag: tag, kSecAttrAccessControl: access] as [CFString: Any]
        } else {
            attributes[kSecPrivateKeyAttrs] = [kSecAttrIsPermanent: true, kSecAttrApplicationTag: tag,
                                               kSecAttrAccessible: kSecAttrAccessibleWhenUnlockedThisDeviceOnly] as [CFString: Any]
        }
        guard let key = SecKeyCreateRandomKey(attributes as CFDictionary, &err) else { throw err!.takeRetainedValue() as Error }
        guard let pub = SecKeyCopyPublicKey(key), let x963 = SecKeyCopyExternalRepresentation(pub, &err) else { throw err!.takeRetainedValue() as Error }
        return try P256.Signing.PublicKey(x963Representation: x963 as Data).derRepresentation
    }

    /// Signs every message with the key, using context's authentication.
    static func sign(_ messages: [Data], context: LAContext?) throws -> [Data] {
        var q = query
        q[kSecReturnRef] = true
        if let context { q[kSecUseAuthenticationContext] = context }
        var item: CFTypeRef?
        let status = SecItemCopyMatching(q as CFDictionary, &item)
        guard status == errSecSuccess, let found = item else {
            throw NSError(domain: NSOSStatusErrorDomain, code: Int(status), userInfo: [NSLocalizedDescriptionKey: "There is no owner key on this iPhone"])
        }
        let key = found as! SecKey
        return try messages.map { m in
            var err: Unmanaged<CFError>?
            guard let sig = SecKeyCreateSignature(key, .ecdsaSignatureMessageX962SHA256, m as CFData, &err) else { throw err!.takeRetainedValue() as Error }
            return sig as Data
        }
    }

    /// Forgets the key (the iPhone left its owner).
    static func delete() { SecItemDelete(query as CFDictionary) }
}

/// Face ID (or the passcode) for one decision. End-to-end tests on the
/// Simulator, which has neither, run with -HermeshTestApprove: the person's
/// tap on Approve is then the whole decision.
nonisolated enum Authentication {
    static var skipped: Bool {
        #if DEBUG && targetEnvironment(simulator)
        return ProcessInfo.processInfo.arguments.contains("-HermeshTestApprove")
        #else
        return false
        #endif
    }

    /// An authenticated context for signing, or nil when the person did not
    /// confirm. reason names the decision, from the signed bytes.
    static func confirm(_ reason: String) async -> LAContext? {
        let ctx = LAContext()
        if skipped { return ctx }
        do {
            _ = try await ctx.evaluatePolicy(.deviceOwnerAuthentication, localizedReason: reason)
            return ctx
        } catch {
            return nil
        }
    }
}
