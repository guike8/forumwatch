import Foundation
import AppKit
import SwiftUI
import Security
import Darwin

@MainActor
final class AppModel: ObservableObject {
    enum Phase: String { case stopped = "Stopped", starting = "Starting", running = "Running", stopping = "Stopping" }
    @Published private(set) var phase = Phase.stopped
    @Published private(set) var configuration: AppConfiguration?
    @Published private(set) var items: [Item] = []
    @Published private(set) var statuses: [SourceStatus] = []
    @Published private(set) var connection = "Offline"
    @Published private(set) var statusNotice: String?
    @Published var error: String?
    var sources: [Source] { configuration?.sources ?? [] }
    var storagePath: String { storage?.root.path ?? "Unavailable" }
    private var storage: PrivateStorage?
    private var process: Process?
    private var runDirectory: URL?
    private var logHandle: FileHandle?
    private var api: LocalAPI?
    private var streamTask: Task<Void, Never>?
    private var statusTask: Task<Void, Never>?
    private var processTask: Task<Void, Never>?
    private var index = ItemIndex()
    private(set) var smokeStarted = false

    init(storageRoot: URL? = nil, requireEmptyRoot: Bool = false, startupError: String? = nil) {
        do {
            if let startupError { throw ClientError.message(startupError) }
            if requireEmptyRoot, let storageRoot,
               FileManager.default.fileExists(atPath: storageRoot.path),
               !(try FileManager.default.contentsOfDirectory(atPath: storageRoot.path)).isEmpty {
                throw ClientError.message("Smoke tests require a new, empty private directory.")
            }
            let storage = try PrivateStorage(root: storageRoot)
            self.storage = storage
            configuration = try storage.load()
        } catch { self.error = error.localizedDescription }
    }

    func save(source: Source, replacing original: Source?, torProxy: String) async throws {
        guard var next = configuration else { throw ClientError.message("Private storage is unavailable.") }
        let validated = try source.validated(replacing: original)
        try Self.validateProxy(torProxy)
        if let original, let i = next.sources.firstIndex(where: { $0.id == original.id }) {
            next.sources[i] = validated
        } else { next.sources.append(validated) }
        next.torProxy = torProxy
        try await applyConfiguration(next)
    }

    func saveFilters(_ changes: [String: AlertFilter]) async throws {
        guard var next = configuration else { throw ClientError.message("Private storage is unavailable.") }
        let known = Set(next.sources.map(\.id))
        guard Set(changes.keys).isSubset(of: known) else {
            throw ClientError.message("A source was removed. Reopen Filters and try again.")
        }
        for index in next.sources.indices {
            if let filter = changes[next.sources[index].id] { next.sources[index].alertFilter = filter }
        }
        guard next.sources != configuration?.sources else { return }
        try await applyConfiguration(next)
    }

    private func applyConfiguration(_ next: AppConfiguration) async throws {
        guard let storage else { throw ClientError.message("Private storage is unavailable.") }
        // Go/RE2 is authoritative. Invalid rules must not stop a working collector
        // or replace the last valid configuration.
        try await validateConfiguration(next)
        let restart = phase == .running || phase == .starting
        await stop()
        try storage.save(next)
        configuration = next
        index = ItemIndex(); items = []; statuses = []
        if restart { await start() }
    }

    private func validateConfiguration(_ next: AppConfiguration) async throws {
        guard let storage else { throw ClientError.message("Private storage is unavailable.") }
        let run = try storage.newRun()
        defer { try? FileManager.default.removeItem(at: run) }
        let candidate = run.appendingPathComponent("check-config.json")
        let log = run.appendingPathComponent("check-config.log")
        guard FileManager.default.createFile(atPath: candidate.path, contents: try JSONEncoder().encode(next), attributes: [.posixPermissions: 0o600]),
              FileManager.default.createFile(atPath: log.path, contents: nil, attributes: [.posixPermissions: 0o600]) else {
            throw ClientError.message("Unable to prepare private configuration validation.")
        }
        let handle = try FileHandle(forWritingTo: log)
        defer { try? handle.close() }
        let child = Process()
        child.executableURL = Bundle.main.bundleURL.appendingPathComponent("Contents/MacOS/forumwatch")
        child.arguments = ["-config", candidate.path, "-check-config", "-require-tor"]
        child.currentDirectoryURL = run
        var environment = ProcessInfo.processInfo.environment
        environment.removeValue(forKey: "NIGHTWIRE_TOKEN")
        child.environment = environment
        child.standardInput = FileHandle.nullDevice
        child.standardOutput = FileHandle.nullDevice
        child.standardError = handle
        try Task.checkCancellation()
        try child.run()
        defer {
            if child.isRunning {
                _ = Darwin.kill(child.processIdentifier, SIGKILL)
                child.waitUntilExit()
            }
        }
        let deadline = Date().addingTimeInterval(5)
        while child.isRunning {
            guard Date() < deadline else { throw ClientError.message("Configuration validation timed out.") }
            try await Task.sleep(nanoseconds: 50_000_000)
        }
        child.waitUntilExit()
        guard child.terminationStatus == 0 else {
            let detail = (try? Data(contentsOf: log)).flatMap { String(data: $0.suffix(8192), encoding: .utf8) } ?? "Invalid configuration."
            throw ClientError.message(detail)
        }
    }

    func delete(_ source: Source) async {
        guard var next = configuration, let storage else { return }
        let restart = phase == .running || phase == .starting
        await stop()
        next.sources.removeAll { $0.id == source.id }
        do {
            try storage.save(next)
            configuration = next
            index = ItemIndex(); items = []; statuses = []
            if restart { await start() }
        } catch { self.error = error.localizedDescription }
    }

    static func validateProxy(_ address: String) throws {
        // Tor is a separately installed local SOCKS listener, never auto-started.
        _ = try Ready(pid: 1, address: address).endpoint(expectedPID: 1)
    }

    func start() async {
        guard phase == .stopped, let storage, let configuration else { return }
        phase = .starting
        error = nil
        connection = "Waiting for local backend"
        let child = Process()
        do {
            try Self.validateProxy(configuration.torProxy)
            try storage.save(configuration)
            let executable = Bundle.main.bundleURL.appendingPathComponent("Contents/MacOS/forumwatch")
            guard FileManager.default.isExecutableFile(atPath: executable.path) else {
                throw ClientError.message("Bundled forumwatch is missing. Build Nightwire.app using build-app.sh.")
            }
            var random = [UInt8](repeating: 0, count: 32)
            guard SecRandomCopyBytes(kSecRandomDefault, random.count, &random) == errSecSuccess else {
                throw ClientError.message("Unable to generate the local API credential.")
            }
            let token = random.map { String(format: "%02x", $0) }.joined()
            let run = try storage.newRun()
            runDirectory = run
            let readyFile = run.appendingPathComponent("ready.json")
            let log = run.appendingPathComponent("backend.log")
            FileManager.default.createFile(atPath: log.path, contents: nil, attributes: [.posixPermissions: 0o600])
            let handle = try FileHandle(forWritingTo: log)
            logHandle = handle
            child.executableURL = executable
            child.arguments = ["-config", storage.configURL.path, "-ready-file", readyFile.path, "-require-tor"]
            child.currentDirectoryURL = storage.root
            var environment = ProcessInfo.processInfo.environment
            environment["NIGHTWIRE_TOKEN"] = token
            child.environment = environment
            child.standardInput = FileHandle.nullDevice
            child.standardOutput = FileHandle.nullDevice
            child.standardError = handle
            try child.run()
            process = child
            let deadline = Date().addingTimeInterval(15)
            var endpoint: URL?
            while phase == .starting, process === child {
                guard child.isRunning else { throw ClientError.message("Backend exited before readiness." + backendLog()) }
                if FileManager.default.fileExists(atPath: readyFile.path) {
                    try PrivateStorage.rejectSymlink(readyFile)
                    let data = try Data(contentsOf: readyFile)
                    guard data.count < 4096 else { throw ClientError.message("Oversized backend readiness file.") }
                    let ready = try JSONDecoder().decode(Ready.self, from: data)
                    endpoint = try ready.endpoint(expectedPID: child.processIdentifier)
                    break
                }
                guard Date() < deadline else { throw ClientError.message("Backend readiness timed out." + backendLog()) }
                try await Task.sleep(nanoseconds: 100_000_000)
            }
            guard phase == .starting, process === child, child.isRunning, let endpoint else { return }
            let api = LocalAPI(base: endpoint, token: token)
            self.api = api
            phase = .running
            index = ItemIndex(); items = []; statuses = []
            beginStreaming(api)
            beginStatusPolling(api)
            processTask = Task { [weak self] in
                while !Task.isCancelled && child.isRunning {
                    do { try await Task.sleep(nanoseconds: 300_000_000) } catch { return }
                }
                guard let self, !Task.isCancelled, self.process === child, self.phase == .running else { return }
                let message = "Backend stopped unexpectedly." + self.backendLog()
                await self.stop()
                self.error = message
            }
        } catch {
            guard phase != .stopping else { return }
            let message = error.localizedDescription
            await stop()
            self.error = message
        }
    }

    // Only signal the Foundation.Process instance created above. Never use a PID
    // read from disk, search for a process by name, or start a replacement early.
    func stop() async {
        if phase == .stopping {
            while phase == .stopping { try? await Task.sleep(nanoseconds: 100_000_000) }
            return
        }
        phase = .stopping
        streamTask?.cancel(); streamTask = nil
        statusTask?.cancel(); statusTask = nil
        processTask?.cancel(); processTask = nil
        api?.close(); api = nil
        if let child = process {
            if child.isRunning { child.terminate() }
            let deadline = Date().addingTimeInterval(5)
            while child.isRunning {
                if Date() >= deadline { _ = Darwin.kill(child.processIdentifier, SIGKILL) }
                try? await Task.sleep(nanoseconds: 100_000_000)
            }
            child.waitUntilExit()
        }
        process = nil
        try? logHandle?.close(); logHandle = nil
        if let runDirectory { try? FileManager.default.removeItem(at: runDirectory) }
        runDirectory = nil
        connection = "Offline"
        statusNotice = nil
        statuses = []
        phase = .stopped
    }

    private func backendLog() -> String {
        guard let runDirectory, let data = try? Data(contentsOf: runDirectory.appendingPathComponent("backend.log")),
              let text = String(data: data.suffix(4096), encoding: .utf8), !text.isEmpty else { return "" }
        return "\n" + text
    }

    private func publishItems() {
        let active = Set(sources.map(\.id))
        items = index.items.values.filter { active.contains($0.source) }.sorted {
            $0.observedDate == $1.observedDate ? $0.id < $1.id : $0.observedDate > $1.observedDate
        }
    }

    // Explicit opt-in for isolated GUI smoke runs. Only synthetic fixtures are
    // saved while stopped; collector cycles always have no sources.
    func runSmoke(resultDirectory: URL) async {
        guard !smokeStarted else { return }
        smokeStarted = true
        var pids: [Int32] = []
        var failure: String?
        do {
            if let error { throw ClientError.message(error) }
            guard sources.isEmpty else { throw ClientError.message("Smoke tests must have no sources.") }
            try await Task.sleep(nanoseconds: 500_000_000)
            try captureOwnedView(in: resultDirectory, name: "empty-window.png", sheet: false)
            guard let window = NSApplication.shared.windows.first(where: { $0.isVisible && !$0.isSheet }) else {
                throw ClientError.message("Smoke resize could not find the app-owned window.")
            }
            let originalFrame = window.frame
            for width: CGFloat in [600, 756, 1250] {
                var frame = originalFrame
                frame.origin.x = window.screen?.visibleFrame.minX ?? originalFrame.minX
                frame.size.width = width
                window.setFrame(frame, display: true)
                try await Task.sleep(nanoseconds: 300_000_000)
                guard abs(window.frame.width - width) <= 1 else {
                    throw ClientError.message("Window refused width \(width): actual \(window.frame.width).")
                }
                try captureOwnedView(in: resultDirectory, name: "window-\(Int(width)).png", sheet: false)
            }
            window.setFrame(originalFrame, display: true)
            for cycle in 0..<2 {
                await start()
                guard phase == .running, let child = process, let client = api else {
                    throw ClientError.message(error ?? "Smoke backend did not start.")
                }
                pids.append(child.processIdentifier)
                let deadline = Date().addingTimeInterval(12)
                while connection != "Stream connected · resynced" {
                    guard phase == .running, Date() < deadline else {
                        throw ClientError.message("Smoke SSE/snapshot synchronization failed: " + connection)
                    }
                    try await Task.sleep(nanoseconds: 100_000_000)
                }
                let snapshot = try await client.get("api/items", as: [Item]?.self) ?? []
                let status = try await client.get("api/status", as: [SourceStatus]?.self) ?? []
                guard snapshot.isEmpty, status.isEmpty else { throw ClientError.message("Smoke snapshot/status were not empty.") }
                let (_, unauthorized) = try await client.session.data(from: client.base.appendingPathComponent("api/items"))
                guard (unauthorized as? HTTPURLResponse)?.statusCode == 401 else {
                    throw ClientError.message("Local API accepted a missing bearer token.")
                }
                if cycle == 0, let storage {
                    let before = try Data(contentsOf: storage.configURL)
                    var invalid = Source()
                    invalid.url = "https://example.invalid/feed"
                    invalid.alertFilter.excludeTitle = "(?=unsupported)"
                    var rejected = false
                    do { try await save(source: invalid, replacing: nil, torProxy: "127.0.0.1:9050") }
                    catch { rejected = error.localizedDescription.contains("alert_filter.exclude_title") }
                    guard rejected, process === child, child.isRunning, phase == .running,
                          try Data(contentsOf: storage.configURL) == before else {
                        throw ClientError.message("Invalid filters changed the config or stopped the running child.")
                    }
                }
                await stop()
                guard !child.isRunning, phase == .stopped else { throw ClientError.message("Owned backend did not terminate.") }
            }
            // A synthetic source is saved only in this isolated smoke profile while
            // stopped. It is never collected, and is removed before the test ends.
            var fixture = Source()
            fixture.url = "https://example.invalid/filter-preview"
            fixture.alertFilter.excludeTitle = "custom-noise"
            try await save(source: fixture, replacing: nil, torProxy: "127.0.0.1:9050")
            var rules = fixture.alertFilter
            rules.quietSales = true
            rules.readableOnly = true
            try await saveFilters([fixture.id: rules])
            guard phase == .stopped, process == nil, let stored = try storage?.load().sources.first,
                  stored.id == fixture.id, stored.alertFilter == rules else {
                throw ClientError.message("Filter settings did not round-trip while stopped.")
            }
            rules.quietSales = false
            try await saveFilters([fixture.id: rules])
            guard sources.first?.alertFilter.excludeTitle == "custom-noise",
                  sources.first?.alertFilter.quietSales == false else {
                throw ClientError.message("Disabling quiet sales damaged custom rules.")
            }
            NotificationCenter.default.post(name: .nightwireFilters, object: nil)
            try await Task.sleep(nanoseconds: 700_000_000)
            try captureOwnedView(in: resultDirectory, name: "filter-settings.png", sheet: true)
            NotificationCenter.default.post(name: .nightwireDismissEditor, object: nil)
            try await Task.sleep(nanoseconds: 300_000_000)
            await delete(fixture)
            let catalog = try SourceCatalog.load()
            guard !catalog.isEmpty, sources.isEmpty, phase == .stopped else {
                throw ClientError.message("Bundled source catalog is missing or changed monitoring state.")
            }
            for entry in catalog {
                if entry.canConfigure {
                    _ = try entry.makeSource().validated(replacing: nil)
                } else {
                    var rejected = false
                    do { _ = try entry.makeSource() } catch { rejected = true }
                    guard rejected else { throw ClientError.message("A research-only catalog entry constructed a source.") }
                }
            }
            NotificationCenter.default.post(name: .nightwireAddSource, object: nil)
            try await Task.sleep(nanoseconds: 700_000_000)
            try captureOwnedView(in: resultDirectory, name: "source-editor.png", sheet: true)
            guard let ready = catalog.first(where: \.canConfigure),
                  let research = catalog.first(where: { $0.status == .loginRequired }) else {
                throw ClientError.message("Smoke catalog needs configurable and login-required entries.")
            }
            let beforeBrowsing = try storage.map { try Data(contentsOf: $0.configURL) }
            for (id, expectedDraft, image) in [(ready.id, true, "catalog-ready.png"),
                                               (research.id, false, "catalog-research.png"),
                                               ("", true, "catalog-custom.png")] {
                let selection = CatalogSmokeSelection(id: id)
                NotificationCenter.default.post(name: .nightwireSmokeCatalog, object: selection)
                guard selection.canSave == expectedDraft, selection.hasDraft == expectedDraft else {
                    throw ClientError.message("Catalog selection did not enforce draft/Save readiness for \(id).")
                }
                try await Task.sleep(nanoseconds: 400_000_000)
                try captureOwnedView(in: resultDirectory, name: image, sheet: true)
            }
            guard sources.isEmpty, phase == .stopped, process == nil,
                  try storage.map({ try Data(contentsOf: $0.configURL) }) == beforeBrowsing else {
                throw ClientError.message("Browsing the catalog changed configuration or monitoring state.")
            }
        } catch { failure = error.localizedDescription }
        NotificationCenter.default.post(name: .nightwireDismissEditor, object: nil)
        try? await Task.sleep(nanoseconds: 300_000_000)
        await stop()
        let result: [String: Any] = [
            "success": failure == nil,
            "error": failure ?? "",
            "child_pids": pids,
            "sources": sources.count,
            "checks": ["window widths 600/756/1250", "bundled child readiness/PID", "bearer authentication", "SSE before snapshot", "empty items/status", "owned stop and restart", "invalid RE2 preserves running child/config", "filter preference persistence and custom-rule preservation", "stopped filter saves never start collection", "native Filters panel", "bundled source catalog loads without configuring sources", "native ready/research/custom catalog draft and Save guards"]
        ]
        do {
            // A failed empty-directory guard must not write into an existing profile.
            guard storage != nil else { return }
            let destination = resultDirectory.appendingPathComponent("smoke-result.json")
            try PrivateStorage.rejectSymlink(destination)
            try JSONSerialization.data(withJSONObject: result, options: [.prettyPrinted, .sortedKeys]).write(to: destination, options: .atomic)
            try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: destination.path)
        } catch { self.error = error.localizedDescription }
    }

    private func captureOwnedView(in directory: URL, name: String, sheet: Bool) throws {
        guard let window = NSApplication.shared.windows.first(where: { $0.isVisible && !$0.isSheet }),
              let view = (sheet ? window.attachedSheet : window)?.contentView else {
            throw ClientError.message("Smoke capture could not find the app-owned view.")
        }
        view.layoutSubtreeIfNeeded()
        guard let bitmap = view.bitmapImageRepForCachingDisplay(in: view.bounds) else {
            throw ClientError.message("App-owned view capture is unavailable.")
        }
        view.cacheDisplay(in: view.bounds, to: bitmap)
        guard let png = bitmap.representation(using: .png, properties: [:]) else {
            throw ClientError.message("App-owned view capture could not encode PNG.")
        }
        let destination = directory.appendingPathComponent(name)
        try PrivateStorage.rejectSymlink(destination)
        try png.write(to: destination, options: .atomic)
        try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: destination.path)
    }

    func reconnect() {
        guard phase == .running, let api else { return }
        streamTask?.cancel()
        beginStreaming(api)
    }

    private func beginStreaming(_ client: LocalAPI) {
        streamTask = Task { [weak self] in
            var delay: UInt64 = 1
            while !Task.isCancelled {
                guard let self, self.api === client else { return }
                self.connection = "Connecting live stream"
                do {
                    var request = client.request("events")
                    request.setValue("text/event-stream", forHTTPHeaderField: "Accept")
                    let (bytes, response) = try await client.session.bytes(for: request)
                    try client.check(response)
                    guard response.mimeType == "text/event-stream" else {
                        throw ClientError.message("Local API did not return an event stream.")
                    }
                    var parser = SSEByteParser()
                    var subscribed = false
                    for try await byte in bytes {
                        guard let (event, data) = try parser.byte(byte) else { continue }
                        try Task.checkCancellation()
                        guard self.api === client else { return }
                        if event == "ready" {
                            // Server has subscribed us before announcing ready. Events that
                            // arrive during this snapshot remain buffered in this stream.
                            self.connection = "Resyncing items"
                            let snapshot = try await client.get("api/items", as: [Item]?.self) ?? []
                            guard self.api === client, !Task.isCancelled else { return }
                            self.index.snapshot(snapshot)
                            self.publishItems()
                            subscribed = true
                            delay = 1
                            self.connection = "Stream connected · resynced"
                        } else if event == "item", subscribed {
                            let envelope = try JSONDecoder().decode(ItemEvent.self, from: data)
                            if envelope.filtered == true { self.index.remove(envelope) }
                            else { self.index.upsert(try JSONDecoder().decode(Item.self, from: data)) }
                            self.publishItems()
                        }
                    }
                    throw ClientError.message("Live connection closed.")
                } catch {
                    guard !Task.isCancelled, self.api === client else { return }
                    self.connection = "Reconnecting in \(delay)s · \(error.localizedDescription)"
                }
                do { try await Task.sleep(nanoseconds: delay * 1_000_000_000) } catch { return }
                delay = min(delay * 2, 30)
            }
        }
    }

    private func beginStatusPolling(_ client: LocalAPI) {
        statusTask = Task { [weak self] in
            while !Task.isCancelled {
                do {
                    let status = try await client.get("api/status", as: [SourceStatus]?.self) ?? []
                    guard let self, self.api === client, !Task.isCancelled else { return }
                    self.statuses = status
                    self.statusNotice = nil
                } catch {
                    guard let self, self.api === client, !Task.isCancelled else { return }
                    self.statusNotice = "Status unavailable · \(error.localizedDescription)"
                }
                do { try await Task.sleep(nanoseconds: 5_000_000_000) } catch { return }
            }
        }
    }
}
