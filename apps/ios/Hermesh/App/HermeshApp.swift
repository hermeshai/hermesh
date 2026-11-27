import HermeshKit
import HermeshSessions
import SwiftUI
import UserNotifications

@main
struct HermeshApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) private var delegate
    @State private var model = AppModel.shared
    @Environment(\.scenePhase) private var scenePhase
    /// Light or dark: the system's ("system"), or one the owner chose in Account.
    @AppStorage("appearance") private var appearance = "system"

    init() {
        SigningPreview.device = .iPhone
    }

    var body: some Scene {
        WindowGroup {
            RootView()
                .environment(model)
                .preferredColorScheme(appearance == "light" ? .light : appearance == "dark" ? .dark : nil)
                .task {
                    if !ProcessInfo.processInfo.arguments.contains("-HermeshTestScreen") {
                        await model.start()
                    }
                }
                .onOpenURL { url in model.open(url) }
                .onChange(of: scenePhase, initial: true) { _, phase in
                    switch phase {
                    case .active: model.foreground()
                    case .background: model.background()
                    default: break
                    }
                }
        }
    }
}

/// The push token, notifications shown while the app is in front, and
/// what a tap or an action on a notification does.
final class AppDelegate: NSObject, UIApplicationDelegate, UNUserNotificationCenterDelegate {
    func application(_ application: UIApplication, didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]? = nil) -> Bool {
        let center = UNUserNotificationCenter.current()
        center.delegate = self
        // A question with options gets a category of its own (the
        // notification extension makes it); this one is for the rest.
        let reply = UNTextInputNotificationAction(identifier: "reply", title: String(localized: "Answer…"), options: [.authenticationRequired],
                                                  textInputButtonTitle: String(localized: "Answer"), textInputPlaceholder: "")
        center.setNotificationCategories([UNNotificationCategory(identifier: "question", actions: [reply], intentIdentifiers: [])])
        return true
    }

    func application(_ application: UIApplication, didRegisterForRemoteNotificationsWithDeviceToken deviceToken: Data) {
        Push.shared.token = deviceToken.map { String(format: "%02x", $0) }.joined()
        Task { await Push.shared.register(api: AppModel.shared.api?.live) }
    }

    func application(_ application: UIApplication, didFailToRegisterForRemoteNotificationsWithError error: Error) {}

    /// Upright, but for Computer Control's page, which may be held sideways. (-HermeshTestTurns YES: every
    /// page turns, so WideUITests reaches the wide layout on an iPhone Pro Max, as iPhone Duo's inner display does.)
    func application(_ application: UIApplication, supportedInterfaceOrientationsFor window: UIWindow?) -> UIInterfaceOrientationMask {
        UserDefaults.standard.bool(forKey: "HermeshTestTurns") ? .allButUpsideDown : ScreenOrientation.allowed
    }

    /// Match the visible page even when the notification extension did not run.
    @MainActor
    func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification) async -> UNNotificationPresentationOptions {
        var info = notification.request.content.userInfo
        let envelope = info["hermesh"] as? [String: Any]
        if info["session"] == nil || (info["her"] != nil && info["bot"] == nil),
           let encoded = envelope?["n"] as? String, let sealed = Data(base64Encoded: encoded),
           let notice = try? NoticeKey.open(sealed) {
            let content = UNMutableNotificationContent()
            NoticeText.fill(content, with: notice)
            info = content.userInfo
        }
        let model = AppModel.shared
        if let machine = info["her"] as? String, let bot = info["bot"] as? String,
           model.isShowingHer(machineID: machine, botID: bot) {
            model.syncBadge(force: true)
            return []
        }
        if let session = info["session"] as? String, !session.isEmpty, model.isShowing(session: session) {
            model.syncBadge(force: true)
            return []
        }
        if let code = info["pairing"] as? String, model.sheet == .pairing(code) {
            model.syncBadge(force: true)
            return []
        }
        if envelope?["k"] as? String == "session_started" { return [.list] }
        return [.banner, .list, .sound]
    }

    func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse) async {
        await AppModel.shared.handle(response)
    }
}
