// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "WingthingCore",
    platforms: [.iOS(.v16), .macOS(.v13)],
    products: [.library(name: "WingthingCore", targets: ["WingthingCore"]), .library(name: "WingthingUI", targets: ["WingthingUI"])],
    targets: [
        .target(name: "WingthingCore"),
        .target(name: "WingthingUI", dependencies: ["WingthingCore"]),
        // Debug-only read acceptance against an explicit same-Mac preview roost.
        // Deliberately not a product; release builds refuse to run.
        .executableTarget(name: "WingthingLivePreview", dependencies: ["WingthingCore"]),
        .testTarget(name: "WingthingCoreTests", dependencies: ["WingthingCore", "WingthingUI"]),
    ]
)
