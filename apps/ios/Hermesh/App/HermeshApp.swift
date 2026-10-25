import HermeshKit
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
                .task { await model.start() }
                .onOpenURL { url in model.onboardingLink = url }
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
        Task { await Push.shared.register(api: AppModel.shared.api) }
    }

    func application(_ application: UIApplication, didFailToRegisterForRemoteNotificationsWithError error: Error) {}

    /// In front: show it, unless it is about the session on screen.
    func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification) async -> UNNotificationPresentationOptions {
        let session = notification.request.content.userInfo["session"] as? String
        if let session, await AppModel.shared.isShowing(session: session) { return [] }
        return [.banner, .list, .sound]
    }

    func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse) async {
        await AppModel.shared.handle(response)
    }
}
