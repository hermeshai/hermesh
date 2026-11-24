import Foundation

private final class BundleFinder {}

extension Bundle {
    /// HermeshSigner's resource bundle (its strings). Resolved explicitly
    /// instead of SwiftPM's generated Bundle.module, whose single-arch native
    /// build accessor omits Bundle.main.resourceURL (Contents/Resources in a
    /// macOS .app) and crashes.
    static let signer: Bundle = {
        let name = "HermeshSigner_HermeshSigner.bundle"
        var candidates: [URL?] = []
        #if DEBUG
        if let override = ProcessInfo.processInfo.environment["PACKAGE_RESOURCE_BUNDLE_PATH"]
            ?? ProcessInfo.processInfo.environment["PACKAGE_RESOURCE_BUNDLE_URL"] {
            candidates.append(URL(fileURLWithPath: override))
        }
        #endif
        candidates += [
            Bundle.main.resourceURL,
            Bundle(for: BundleFinder.self).resourceURL,
            Bundle.main.bundleURL,
            Bundle(for: BundleFinder.self).bundleURL.deletingLastPathComponent(),
        ]
        for candidate in candidates {
            if let url = candidate?.appendingPathComponent(name), let bundle = Bundle(url: url) {
                return bundle
            }
        }
        return .main
    }()
}
