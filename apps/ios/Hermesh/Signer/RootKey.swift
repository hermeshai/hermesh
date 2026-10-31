import CryptoKit
import Foundation
import HermeshKit
import LocalAuthentication
import Security

/// The owner root keys of this iPhone, one per profile the owner role keeps
/// (one per owner the phone belongs to). On a phone they live in the
/// Secure Enclave and every use needs Face ID (or the passcode); the
/// Simulator has no Secure Enclave, so there they are software keys in the
/// Keychain. Signatures are ASN.1 DER ECDSA-P256 over SHA-256(message).
nonisolated enum RootKey {
    // The app's first bundle id, kept: the keys phones already made carry it.
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

    /// Creates a new root key, keeping the others, and returns its public
    /// key (SPKI DER).
    static func create() throws -> Data {
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
        return try spki(key)
    }

    /// Signs every message with the key keyID names, using context's
    /// authentication.
    static func sign(_ messages: [Data], keyID: String, context: LAContext?) throws -> [Data] {
        guard let key = try keys(context).first(where: { $0.id == keyID })?.key else {
            throw NSError(domain: NSOSStatusErrorDomain, code: Int(errSecItemNotFound), userInfo: [NSLocalizedDescriptionKey: "This iPhone does not have that owner key"])
        }
        return try messages.map { m in
            var err: Unmanaged<CFError>?
            guard let sig = SecKeyCreateSignature(key, .ecdsaSignatureMessageX962SHA256, m as CFData, &err) else { throw err!.takeRetainedValue() as Error }
            return sig as Data
        }
    }

    /// Forgets the key keyID names (the iPhone left that owner).
    static func delete(keyID: String) throws {
        guard let key = try keys(nil).first(where: { $0.id == keyID })?.key else { return }
        SecItemDelete([kSecClass: kSecClassKey, kSecValueRef: key] as CFDictionary)
    }

    private static func spki(_ key: SecKey) throws -> Data {
        var err: Unmanaged<CFError>?
        guard let pub = SecKeyCopyPublicKey(key), let x963 = SecKeyCopyExternalRepresentation(pub, &err) else { throw err!.takeRetainedValue() as Error }
        return try P256.Signing.PublicKey(x963Representation: x963 as Data).derRepresentation
    }

    /// The keys with the ids the owner role names them by.
    private static func keys(_ context: LAContext?) throws -> [(id: String, key: SecKey)] {
        var q: [CFString: Any] = [kSecClass: kSecClassKey, kSecAttrApplicationTag: tag, kSecAttrKeyType: kSecAttrKeyTypeECSECPrimeRandom,
                                  kSecReturnRef: true, kSecMatchLimit: kSecMatchLimitAll]
        if let context { q[kSecUseAuthenticationContext] = context }
        var items: CFTypeRef?
        let status = SecItemCopyMatching(q as CFDictionary, &items)
        if status == errSecItemNotFound { return [] }
        guard status == errSecSuccess else { throw NSError(domain: NSOSStatusErrorDomain, code: Int(status)) }
        return try (items as! [SecKey]).map { (SigningPreview.keyID(try spki($0)), $0) }
    }
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
