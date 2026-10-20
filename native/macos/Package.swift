// swift-tools-version: 5.9
import PackageDescription
let package = Package(name: "TitenAudio", platforms: [.macOS("15.0")], products: [.executable(name: "titen-audio", targets: ["TitenAudio"])], targets: [.executableTarget(name: "TitenAudio", path: ".", exclude: ["Package.swift"], sources: ["AudioCapture.swift"])])
