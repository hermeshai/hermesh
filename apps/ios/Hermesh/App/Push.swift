import CryptoKit
import Foundation
import HermeshKit
import HermeshProto
import UIKit
import UserNotifications

/// Lock-screen notifications. This iPhone registers its APNs token and a
/// notice key (X25519) with the owner's machines, signed by its device key;
/// machines seal each notice to that key, and the notification extension
/// opens it here. The relay and Apple only carry ciphertext.
final class Push {
    static let shared = Push()

    /// The APNs token, hex, once iOS gave one.
    var token: String?
    private var lastSignature: String?
    private var lastRegisteredAt: Date?

    func invalidate() {
        lastSignature = nil
        lastRegisteredAt = nil
    }

    /// Asks iOS for the APNs token (AppDelegate receives it).
    func requestToken() {
        UIApplication.shared.registerForRemoteNotifications()
        #if DEBUG && targetEnvironment(simulator)
        // The Simulator may give no token; tests deliver with `simctl push`, which needs none.
        if token == nil, ProcessInfo.processInfo.arguments.contains("-HermeshTestApprove") { token = "simulator" }
        #endif
    }

    /// Which APNs the token belongs to: the signing profile's, so a build
    /// installed from Xcode (development) and one from the App Store
    /// (production, no profile in the bundle) each get theirs.
    static let environment: String = {
        guard let url = Bundle.main.url(forResource: "embedded", withExtension: "mobileprovision"),
              let data = try? Data(contentsOf: url),
              let start = data.range(of: Data("<?xml".utf8)), let end = data.range(of: Data("</plist>".utf8)),
              let plist = try? PropertyListSerialization.propertyList(from: data[start.lowerBound..<end.upperBound], format: nil) as? [String: Any],
              let entitlements = plist["Entitlements"] as? [String: Any], let env = entitlements["aps-environment"] as? String
        else { return "production" }
        return env
    }()

    /// Registers (again) with what the owner wants to hear about, Her
    /// always (as on the Mac), and keeps how many machines run Her for the
    /// extension (each time the app comes to the front).
    func register(api: LocalAPI?, force: Bool = false) async {
        guard let api, let token else { return }
        NoticeKey.herEnvironments = await MainActor.run { AppModel.shared.machines.filter(\.her.enabled).count }
        do {
            let pubKey = try NoticeKey.load().publicKey.rawRepresentation
            let prefs = NotificationPrefs.current
            let kinds = [prefs.sessionsStarted ? "session_started" : nil, prefs.questions ? "question" : nil, prefs.failures ? "failure" : nil, prefs.turnsDone ? "turn_done" : nil, "her"].compactMap { $0 }
            let ownerID = await MainActor.run { AppModel.shared.status.ownerID }
            let sig = "\(ownerID)|\(token)|\(Self.environment)|\(pubKey.base64EncodedString())|\(kinds.joined(separator: ","))"
            if !force, sig == lastSignature, let last = lastRegisteredAt, Date().timeIntervalSince(last) < 600 {
                return
            }
            var r = Hermesh_Local_V1_RegisterPushRequest()
            r.token = token
            r.environment = Self.environment
            r.noticePublicKey = pubKey
            r.kinds = kinds
            let _: Hermesh_Local_V1_RegisterPushResponse = try await api.call("RegisterPush", r, timeout: 20)
            lastSignature = sig
            lastRegisteredAt = Date()
        } catch {
            // Tried again when the app next starts.
        }
    }
}
