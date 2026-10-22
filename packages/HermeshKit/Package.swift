// swift-tools-version:6.2
import PackageDescription

// What the Mac signer and the iPhone app share: the generated protos, the
// LocalService client, and the signing preview (what a signature means,
// rendered from the bytes to be signed).
let package = Package(
    name: "HermeshKit",
    defaultLocalization: "en",
    platforms: [.macOS(.v14), .iOS(.v26)],
    products: [
        .library(name: "HermeshProto", targets: ["HermeshProto"]),
        .library(name: "HermeshKit", targets: ["HermeshKit"]),
        .library(name: "HermeshSessions", targets: ["HermeshSessions"]),
    ],
    dependencies: [
        .package(url: "https://github.com/apple/swift-protobuf.git", from: "1.38.0"),
    ],
    targets: [
        .target(
            name: "HermeshProto",
            dependencies: [.product(name: "SwiftProtobuf", package: "swift-protobuf")],
            path: "Sources/HermeshProto"
        ),
        .target(
            name: "HermeshKit",
            dependencies: ["HermeshProto", .product(name: "SwiftProtobuf", package: "swift-protobuf")],
            path: "Sources/HermeshKit",
            resources: [.process("Resources")],
            swiftSettings: [.swiftLanguageMode(.v5)]
        ),
        // What the iPhone app shows about sessions: a port of @hermesh/core.
        .target(
            name: "HermeshSessions",
            dependencies: ["HermeshProto", .product(name: "SwiftProtobuf", package: "swift-protobuf")],
            path: "Sources/HermeshSessions",
            resources: [.process("Resources")],
            swiftSettings: [.swiftLanguageMode(.v5)]
        ),
        .testTarget(
            name: "HermeshSessionsTests",
            dependencies: ["HermeshSessions", "HermeshProto"],
            path: "Tests/HermeshSessionsTests",
            swiftSettings: [.swiftLanguageMode(.v5)]
        ),
        .testTarget(
            name: "HermeshKitTests",
            dependencies: ["HermeshKit", "HermeshProto"],
            path: "Tests/HermeshKitTests",
            swiftSettings: [.swiftLanguageMode(.v5)]
        ),
    ]
)
