// swift-tools-version:6.0
import PackageDescription

let package = Package(
    name: "HermeshSigner",
    defaultLocalization: "en",
    platforms: [.macOS(.v14)],
    dependencies: [
        .package(url: "https://github.com/apple/swift-protobuf.git", from: "1.38.0"),
        .package(path: "../../../packages/HermeshKit"),
    ],
    targets: [
        .executableTarget(
            name: "HermeshSigner",
            dependencies: [
                .product(name: "HermeshProto", package: "HermeshKit"),
                .product(name: "HermeshKit", package: "HermeshKit"),
                .product(name: "SwiftProtobuf", package: "swift-protobuf"),
            ],
            path: "Sources/HermeshSigner",
            resources: [.process("Resources")],
            swiftSettings: [.swiftLanguageMode(.v5)]
        ),
        .testTarget(
            name: "HermeshSignerTests",
            dependencies: ["HermeshSigner", .product(name: "HermeshProto", package: "HermeshKit"), .product(name: "HermeshKit", package: "HermeshKit")],
            path: "Tests/HermeshSignerTests",
            swiftSettings: [.swiftLanguageMode(.v5)]
        ),
    ]
)
