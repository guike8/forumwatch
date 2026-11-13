import Foundation
import Darwin

private final class ProfileLock {
    private let descriptor: Int32
    init(_ path: String) throws {
        let handle = Darwin.open(path, O_CREAT | O_RDWR | O_NOFOLLOW, mode_t(0o600))
        guard handle >= 0 else { throw ClientError.message("Cannot open the private profile lock.") }
        guard flock(handle, LOCK_EX | LOCK_NB) == 0 else {
            Darwin.close(handle)
            throw ClientError.message("This Nightwire profile is already open in another instance.")
        }
        descriptor = handle
    }
    deinit { flock(descriptor, LOCK_UN); Darwin.close(descriptor) }
}

struct LaunchOptions {
    let root: URL?
    let smoke: Bool
    init(arguments: [String]) throws {
        let options = arguments.enumerated().filter { ["--smoke-test", "--data-dir"].contains($0.element) }
        guard options.count <= 1 else { throw ClientError.message("Use only one of --smoke-test or --data-dir.") }
        guard let option = options.first else { root = nil; smoke = false; return }
        guard arguments.indices.contains(option.offset + 1), arguments[option.offset + 1].hasPrefix("/") else {
            throw ClientError.message("\(option.element) requires an absolute private directory path.")
        }
        root = URL(fileURLWithPath: arguments[option.offset + 1], isDirectory: true).standardizedFileURL
        smoke = option.element == "--smoke-test"
    }
}

struct AppConfiguration: Codable {
    var listen = "127.0.0.1:0"
    var tokenEnv = "NIGHTWIRE_TOKEN"
    var statePath: String
    var torProxy = "127.0.0.1:9050"
    var sources: [Source] = []
    enum CodingKeys: String, CodingKey {
        case listen, sources
        case tokenEnv = "token_env", statePath = "state_path", torProxy = "tor_proxy"
    }
}

struct PrivateStorage {
    let root: URL
    private let profileLock: ProfileLock
    var configURL: URL { root.appendingPathComponent("config.json") }
    var stateURL: URL { root.appendingPathComponent("state.json") }
    init(root: URL? = nil) throws {
        self.root = root ?? FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("Nightwire", isDirectory: true)
        try Self.directory(self.root)
        profileLock = try ProfileLock(self.root.appendingPathComponent("profile.lock").path)
        try Self.rejectSymlink(configURL)
        try Self.rejectSymlink(stateURL)
    }
    static func rejectSymlink(_ url: URL) throws {
        if let attributes = try? FileManager.default.attributesOfItem(atPath: url.path),
           attributes[.type] as? FileAttributeType == .typeSymbolicLink {
            throw ClientError.message("Refusing symbolic link at \(url.lastPathComponent).")
        }
    }
    static func directory(_ url: URL) throws {
        try rejectSymlink(url)
        try FileManager.default.createDirectory(at: url, withIntermediateDirectories: true,
                                                attributes: [.posixPermissions: 0o700])
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: url.path)
    }
    func load() throws -> AppConfiguration {
        guard FileManager.default.fileExists(atPath: configURL.path) else {
            let config = AppConfiguration(statePath: stateURL.path)
            try save(config)
            return config
        }
        var config = try JSONDecoder().decode(AppConfiguration.self, from: Data(contentsOf: configURL))
        // Never adopt externally supplied listen/token/state paths or a direct source.
        config.listen = "127.0.0.1:0"
        config.tokenEnv = "NIGHTWIRE_TOKEN"
        config.statePath = stateURL.path
        config.sources = try config.sources.map { try $0.validated(replacing: nil) }
        guard Set(config.sources.map(\.id)).count == config.sources.count else {
            throw ClientError.message("Source IDs must be unique.")
        }
        return config
    }
    func save(_ config: AppConfiguration) throws {
        try Self.rejectSymlink(configURL)
        try Self.rejectSymlink(stateURL)
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
        try encoder.encode(config).write(to: configURL, options: [.atomic])
        try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: configURL.path)
    }
    func newRun() throws -> URL {
        let runs = root.appendingPathComponent("runs", isDirectory: true)
        try Self.directory(runs)
        let run = runs.appendingPathComponent(UUID().uuidString, isDirectory: true)
        try Self.directory(run)
        return run
    }
}
