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
    private var registering: Task<Void, Never>?
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

    /// Registers this device’s notification choices with its machines.
    func register(api: LocalAPI?, force: Bool = false) async {
        // An earlier request must finish before a newer device preference can reach the relay.
        while let registering { await registering.value }
        guard let api, let token else { return }
        let task = Task {
            await registerNow(api: api, token: token, force: force)
            registering = nil
        }
        registering = task
        await task.value
    }

    private func registerNow(api: LocalAPI, token: String, force: Bool) async {
        NoticeKey.herEnvironments = AppModel.shared.machines.filter(\.her.enabled).count
        do {
            let pubKey = try NoticeKey.load().publicKey.rawRepresentation
            let prefs = NotificationPrefs.current
            var kinds: [String] = []
            if prefs.sessionsStarted { kinds.append("session_started") }
            if prefs.questions { kinds += ["question", "session_request"] }
            if prefs.results { kinds += ["failure", "turn_done"] }
            if prefs.herReplies && prefs.herRequests { kinds.append("her") }
            else if prefs.herReplies { kinds.append("her_reply") }
            else if prefs.herRequests { kinds.append("her_request") }
            let ownerID = AppModel.shared.status.ownerID
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
