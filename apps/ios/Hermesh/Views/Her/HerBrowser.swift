import AuthenticationServices
import UIKit

/// The browser a sign-in for Her opens in (docs/her-server.md §5.4): the
/// system's sign-in sheet, sharing Safari's sign-ins. The provider sends it
/// back to this iPhone's core on 127.0.0.1, which catches the redirect;
/// nothing comes back through the sheet itself, so it is closed once the
/// core has caught it. Closing it first stops the sign-in.
final class HerBrowser: NSObject, ASWebAuthenticationPresentationContextProviding {
    static let shared = HerBrowser()
    private var session: ASWebAuthenticationSession?
    private var current: UUID?

    /// Opens url, in place of a sheet open already; `closed` runs if the owner closes it.
    func open(_ url: URL, closed: @escaping @MainActor () -> Void) {
        close()
        let token = UUID()
        // No redirect comes back to the app: the scheme only names it.
        let s = ASWebAuthenticationSession(url: url, callback: .customScheme("hermesh")) { [weak self] _, _ in
            Task { @MainActor in
                guard let self, self.current == token else { return }
                self.current = nil
                self.session = nil
                closed()
            }
        }
        s.presentationContextProvider = self
        current = token
        session = s
        s.start()
    }

    /// Closes the sheet, as the sign-in ended.
    func close() {
        current = nil
        session?.cancel()
        session = nil
    }

    nonisolated func presentationAnchor(for session: ASWebAuthenticationSession) -> ASPresentationAnchor {
        MainActor.assumeIsolated {
            let scenes = UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }
            return scenes.compactMap(\.keyWindow).first ?? scenes.flatMap(\.windows).first ?? ASPresentationAnchor(windowScene: scenes[0])
        }
    }
}
