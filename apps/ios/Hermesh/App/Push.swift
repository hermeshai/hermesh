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

    /// Registers (again) with what the owner wants to hear about.
    func register(api: LocalAPI?) async {
        guard let api, let token else { return }
        do {
            var r = Hermesh_Local_V1_RegisterPushRequest()
            r.token = token
            r.environment = Self.environment
            r.noticePublicKey = try NoticeKey.load().publicKey.rawRepresentation
            let prefs = NotificationPrefs.current
            r.kinds = [prefs.sessionsStarted ? "session_started" : nil, prefs.questions ? "question" : nil, prefs.failures ? "failure" : nil, prefs.turnsDone ? "turn_done" : nil].compactMap { $0 }
            let _: Hermesh_Local_V1_RegisterPushResponse = try await api.call("RegisterPush", r, timeout: 20)
        } catch {
            // Tried again when the app next starts.
        }
    }
}
