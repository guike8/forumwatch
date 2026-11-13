// swift-tools-version: 5.9
import PackageDescription

let package = Package(
    name: "Nightwire",
    platforms: [.macOS(.v14)],
    products: [.executable(name: "Nightwire", targets: ["Nightwire"])],
    targets: [
        .executableTarget(name: "Nightwire", resources: [.process("Resources")]),
        .testTarget(name: "NightwireTests", dependencies: ["Nightwire"])
    ]
)
