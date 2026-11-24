import Foundation

private final class BundleFinder {}

/// HermeshKit's resource bundle (its strings), for tests and for apps that
/// look a string up themselves. Resolved explicitly instead of SwiftPM's
/// generated Bundle.module, whose single-arch native build accessor omits
/// Bundle.main.resourceURL (Contents/Resources in a macOS .app) and crashes.
public enum HermeshKitResources {
    public static let bundle: Bundle = {
        let name = "HermeshKit_HermeshKit.bundle"
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

extension Bundle {
    static var hermeshKit: Bundle { HermeshKitResources.bundle }
}
