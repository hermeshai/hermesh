// swift-tools-version:6.0
import PackageDescription

// hermesh-screen: what Computer Control needs of macOS (docs/computer-control.md §4.1).
// hermeshd starts it and talks to it over stdin and stdout; it has no network.
let package = Package(
    name: "HermeshScreen",
    platforms: [.macOS(.v14)],
    targets: [
        .executableTarget(
            name: "hermesh-screen",
            path: "Sources/HermeshScreen",
            swiftSettings: [.swiftLanguageMode(.v5)]
        ),
    ]
)
