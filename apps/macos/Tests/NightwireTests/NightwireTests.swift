import AppKit
import Foundation
import Testing
@testable import Nightwire

private func expectEqual<T: Equatable>(_ actual: T, _ expected: T, sourceLocation: SourceLocation = #_sourceLocation) {
    #expect(actual == expected, sourceLocation: sourceLocation)
}
private func expectNotEqual<T: Equatable>(_ actual: T, _ expected: T, sourceLocation: SourceLocation = #_sourceLocation) {
    #expect(actual != expected, sourceLocation: sourceLocation)
}
private func expectTrue(_ value: Bool, sourceLocation: SourceLocation = #_sourceLocation) {
    #expect(value, sourceLocation: sourceLocation)
}
private func expectFalse(_ value: Bool, sourceLocation: SourceLocation = #_sourceLocation) {
    #expect(!value, sourceLocation: sourceLocation)
}
private func expectNil<T>(_ value: T?, sourceLocation: SourceLocation = #_sourceLocation) {
    #expect(value == nil, sourceLocation: sourceLocation)
}
private func require<T>(_ value: T?, sourceLocation: SourceLocation = #_sourceLocation) throws -> T {
    try #require(value, sourceLocation: sourceLocation)
}
private func expectNoThrow<T>(_ value: @autoclosure () throws -> T, _ message: String = "", sourceLocation: SourceLocation = #_sourceLocation) {
    do { _ = try value() } catch { Issue.record("Unexpected error: \(error) \(message)", sourceLocation: sourceLocation) }
}
private func expectThrows<T>(_ value: @autoclosure () throws -> T, _ message: String = "", sourceLocation: SourceLocation = #_sourceLocation) {
    do { _ = try value(); Issue.record("Expected rejection: \(message)", sourceLocation: sourceLocation) } catch { }
}

@Suite struct NightwireTests {
    private func item(id: String = "one", observed: String = "2026-11-13T12:00:00Z", hash: String = "a", extract: Bool = false) throws -> Item {
        var json: [String: Any] = ["id": id, "source": "source", "title": "Post", "url": "http://example.onion/post/1",
                                   "body": "Visible text", "first_seen": observed, "observed_at": observed,
                                   "kind": "baseline", "hash": hash]
        if extract { json["enrichment"] = ["method": "local/extractive", "text": "Local text", "source_hash": hash] }
        return try JSONDecoder().decode(Item.self, from: JSONSerialization.data(withJSONObject: json))
    }
    @Test @MainActor func testMenuBarIconIsAResizableTemplate() {
        let icon = Branding.menuBarIcon
        expectTrue(icon.isTemplate)
        expectEqual(icon.size.width, 18)
        expectEqual(icon.size.height, 18)
        expectTrue(icon.isValid)
    }
    @Test @MainActor func testMothArtworkRendersUnfilledAtBothDetailLevels() throws {
        for simplified in [false, true] {
            let bitmap = try require(NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: 512, pixelsHigh: 512,
                                                     bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true,
                                                     isPlanar: false, colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0))
            let context = try require(NSGraphicsContext(bitmapImageRep: bitmap))
            NSGraphicsContext.saveGraphicsState()
            NSGraphicsContext.current = context
            MothArtwork.draw(in: NSRect(x: 0, y: 0, width: 512, height: 512), tint: .black, simplified: simplified)
            NSGraphicsContext.restoreGraphicsState()
            var inkPixels = 0
            for y in 0..<512 {
                for x in 0..<512 {
                    if (bitmap.colorAt(x: x, y: y)?.alphaComponent ?? 0) > 0.1 { inkPixels += 1 }
                }
            }
            expectTrue(inkPixels > 1_000)
            expectTrue(inkPixels < 512 * 512 / 5)
            // Empty space inside the forewing stays transparent, not a filled panel.
            expectEqual(bitmap.colorAt(x: 100, y: 155)?.alphaComponent, 0)
        }
    }
    @Test func testReadyAcceptsNumericLoopbackOnly() throws {
        for address in ["127.0.0.1:1234", "127.10.20.30:65535", "[::1]:4321"] {
            expectNoThrow(try Ready(pid: 42, address: address).endpoint(expectedPID: 42), address)
        }
        for address in ["localhost:1234", "0.0.0.0:1234", "192.168.1.1:1234", "[::]:1234", "example.com:1234",
                        "127.0.0.1:0", "127.0.0.1:65536", "127.0.0.1:1234/path", "user@127.0.0.1:1234",
                        "127.0.0.1:1234?query=1", "127.0.0.1:1234#part", "127.1:1234", "2130706433:1234"] {
            expectThrows(try Ready(pid: 42, address: address).endpoint(expectedPID: 42), address)
        }
        expectThrows(try Ready(pid: 41, address: "127.0.0.1:1234").endpoint(expectedPID: 42))
    }
    @Test func testFilterWireFieldsAreSingleRegexStrings() throws {
        let filter = AlertFilter(include: "(?i)access", exclude: "noise", excludeTitle: "sale", readableOnly: true)
        let data = try JSONEncoder().encode(filter)
        let wire = try require(JSONSerialization.jsonObject(with: data) as? [String: Any])
        expectEqual(wire["include"] as? String, "(?i)access")
        expectEqual(wire["exclude"] as? String, "noise")
        expectEqual(wire["exclude_title"] as? String, "sale")
        expectEqual(wire["readable_only"] as? Bool, true)
        expectEqual(try JSONDecoder().decode(AlertFilter.self, from: data), filter)
    }
    @Test func testQuietSalesTogglePreservesCustomRulesAndRoundTrips() throws {
        var filter = AlertFilter(include: "(?i)access", exclude: "spam", excludeTitle: "custom", readableOnly: true)
        expectFalse(filter.quietSales)
        filter.quietSales = true
        let wire = try require(JSONSerialization.jsonObject(with: JSONEncoder().encode(filter)) as? [String: Any])
        expectEqual(wire["quiet_sales"] as? Bool, true)
        let restored = try JSONDecoder().decode(AlertFilter.self, from: JSONEncoder().encode(filter))
        expectEqual(restored, filter)
        filter.quietSales = false
        expectEqual(filter.include, "(?i)access")
        expectEqual(filter.exclude, "spam")
        expectEqual(filter.excludeTitle, "custom")
        expectTrue(filter.readableOnly)
    }
    @Test func testSparseFilterDefaultsAndActivity() throws {
        var filter = try JSONDecoder().decode(AlertFilter.self, from: Data("{}".utf8))
        expectFalse(filter.quietSales)
        expectFalse(filter.isActive)
        filter.quietSales = true
        expectTrue(filter.isActive)
        filter.quietSales = false
        filter.excludeTitle = "existing rule"
        expectTrue(filter.isActive)
        filter = AlertFilter(readableOnly: true)
        expectTrue(filter.isActive)
    }
    @Test func testFilterPreferencesPersistWithoutChangingSourceBinding() throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let storage = try PrivateStorage(root: root)
        var config = try storage.load()
        var source = Source(); source.url = "https://example.invalid/feed"
        let originalID = source.id
        source.alertFilter = AlertFilter(excludeTitle: "custom", quietSales: true)
        config.sources = [source]
        try storage.save(config)
        let restored = try storage.load().sources[0]
        expectEqual(restored.id, originalID)
        expectTrue(restored.alertFilter.quietSales)
        expectEqual(restored.alertFilter.excludeTitle, "custom")
    }
    @Test @MainActor func testSavingFiltersRejectsUnknownSourceWithoutChangingConfig() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let model = AppModel(storageRoot: root)
        let path = root.appendingPathComponent("config.json")
        let before = try Data(contentsOf: path)
        do {
            try await model.saveFilters(["removed-source": AlertFilter(quietSales: true)])
            Issue.record("Unknown source filters should not be saved")
        } catch {
            expectTrue(error.localizedDescription.contains("source was removed"))
        }
        expectEqual(try Data(contentsOf: path), before)
        expectEqual(model.phase, .stopped)
        expectTrue(model.sources.isEmpty)
    }
    @Test func testBundledCatalogHasUniqueValidatedTorOnlyPresets() throws {
        let catalog = try SourceCatalog.load()
        expectTrue(catalog.count >= 2)
        expectEqual(Set(catalog.map(\.id)).count, catalog.count)
        expectEqual(Set(catalog.map(\.url)).count, catalog.count)
        for entry in catalog {
            expectFalse(entry.name.isEmpty)
            expectFalse(entry.category.isEmpty)
            expectFalse(entry.evidence.isEmpty)
            expectFalse(entry.notes.isEmpty)
            let first = try entry.makeSource().validated(replacing: nil)
            let second = entry.makeSource()
            expectNotEqual(first.id, second.id)
            expectEqual(first.url, entry.url)
            expectEqual(first.format, entry.format)
            expectEqual(first.transport, "tor")
            expectEqual(first.fetcher, "http")
            expectEqual(first.intervalSeconds, 60)
            expectEqual(first.maxPagesPerPoll, 4)
            expectFalse(first.alertFilter.isActive)
        }
    }
    @Test func testCatalogDetectsConfiguredBindingsWithoutReplacingSettings() throws {
        let entry = try require(SourceCatalog.load().first)
        var source = entry.makeSource()
        source.url += "/"
        source.alertFilter = AlertFilter(excludeTitle: "custom", quietSales: true)
        expectTrue(entry.isConfigured(in: [source]))
        source.format = source.format == "feed" ? "mybb-index" : "feed"
        expectFalse(entry.isConfigured(in: [source]))
        expectEqual(source.alertFilter.excludeTitle, "custom")
    }
    @Test @MainActor func testCatalogBrowsingDoesNotConfigureOrStartMonitoring() throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let model = AppModel(storageRoot: root)
        let path = root.appendingPathComponent("config.json")
        let before = try Data(contentsOf: path)
        for entry in try SourceCatalog.load() { _ = entry.makeSource() }
        expectTrue(model.sources.isEmpty)
        expectEqual(model.phase, .stopped)
        expectEqual(try Data(contentsOf: path), before)
    }
    @Test func testSourceBindingChangesOnlyWithURLOrFormat() throws {
        var original = Source(); original.url = "http://example.onion/forum"
        var modified = original
        modified.alertFilter.include = "(?i)access"
        modified.intervalSeconds = 120
        expectEqual(try modified.validated(replacing: original).id, original.id)
        modified.url += "/other"
        expectNotEqual(try modified.validated(replacing: original).id, original.id)
        modified = original; modified.format = "xenforo-index"
        expectNotEqual(try modified.validated(replacing: original).id, original.id)
    }
    @Test func testSourceRejectsDirectBrowserAndBadInputs() throws {
        var source = Source(); source.url = "http://example.onion/"
        expectNoThrow(try source.validated(replacing: nil))
        source.transport = "direct"
        expectThrows(try source.validated(replacing: nil))
        source.transport = "tor"; source.fetcher = "browser"
        expectThrows(try source.validated(replacing: nil))
        source.fetcher = "http"; source.url = "https://user:password@example.com/"
        expectThrows(try source.validated(replacing: nil))
        source.url = "file:///tmp/post"
        expectThrows(try source.validated(replacing: nil))
        source.url = "http://example.onion/"; source.format = "html"
        expectThrows(try source.validated(replacing: nil))
        source.includeURL = "/thread/"; source.intervalSeconds = 9
        expectThrows(try source.validated(replacing: nil))
    }
    @Test func testRevisionOrderingAndSameHashEnrichment() throws {
        var index = ItemIndex()
        let enriched = try item(observed: "2026-11-13T12:00:01.000000002Z", hash: "new", extract: true)
        index.upsert(enriched)
        index.upsert(try item(observed: "2026-11-13T12:00:01.000000001Z", hash: "old"))
        expectEqual(index.items["one"]?.hash, "new")
        index.upsert(try item(observed: enriched.observedAt, hash: "new"))
        expectEqual(index.items["one"]?.localExtract, "Local text")
        index.upsert(try item(observed: "2026-11-13T12:00:02Z", hash: "next"))
        expectNil(index.items["one"]?.localExtract)
    }
    @Test func testEnrichmentArrivesAfterInitialRevision() throws {
        var index = ItemIndex()
        index.upsert(try item())
        index.upsert(try item(extract: true))
        expectEqual(index.items["one"]?.localExtract, "Local text")
        var bad = try item(extract: true)
        bad.enrichment?.sourceHash = "stale"
        expectNil(bad.localExtract)
        bad = try item(extract: true); bad.access = "restricted"
        expectNil(bad.localExtract)
    }
    @Test func testFilteredTombstoneRemovesAndRejectsStaleReplays() throws {
        var index = ItemIndex()
        index.upsert(try item())
        let tombstone = ItemEvent(id: "one", filtered: true, observedAt: "2026-11-13T12:00:01Z", hash: nil)
        index.remove(tombstone)
        expectNil(index.items["one"])
        index.upsert(try item(extract: true))
        expectNil(index.items["one"])
        index.upsert(try item(observed: "2026-11-13T12:00:02Z", hash: "new"))
        index.remove(tombstone)
        expectEqual(index.items["one"]?.hash, "new")
    }
    @Test func testAuthoritativeSnapshotPrunesAndRetainsSameHashExtract() throws {
        var index = ItemIndex()
        index.upsert(try item(extract: true))
        index.upsert(try item(id: "removed"))
        index.snapshot([try item()])
        expectNil(index.items["removed"])
        expectEqual(index.items["one"]?.localExtract, "Local text")
        index.snapshot([])
        expectTrue(index.items.isEmpty)
    }
    @Test func testSSEWireBytesPreserveBlankFrameDelimiters() throws {
        for newline in ["\n", "\r\n", "\r"] {
            let wire = ["event: ready", #"data: {"resync":"/api/items"}"#, "", ": heartbeat", "", "event: item", #"data: {"id":"one","title":"可见文本"}"#, "", ""].joined(separator: newline)
            var parser = SSEByteParser()
            var frames: [(String, Data)] = []
            // Intentionally feed one byte at a time, including split UTF-8 and CRLF.
            // Do not use AsyncBytes.lines: Foundation can discard the empty lines
            // separating these frames, which previously prevented ready/resync.
            for byte in wire.utf8 {
                if let frame = try parser.byte(byte) { frames.append(frame) }
            }
            expectEqual(frames.count, 2)
            expectEqual(frames.first?.0, "ready")
            expectEqual(frames.last?.0, "item")
            expectEqual(String(data: try require(frames.last?.1), encoding: .utf8), #"{"id":"one","title":"可见文本"}"#)
        }
    }
    @Test func testSSEFrameParsing() throws {
        var parser = SSEParser()
        expectNil(try parser.line(": heartbeat"))
        expectNil(try parser.line("event: ready"))
        expectNil(try parser.line("data: {\"resync\":\"/api/items\"}"))
        let event = try require(parser.line(""))
        expectEqual(event.0, "ready")
        expectEqual(String(data: event.1, encoding: .utf8), "{\"resync\":\"/api/items\"}")
        expectNil(try parser.line("data:first"))
        expectNil(try parser.line("data: second"))
        let multiline = try require(parser.line(""))
        expectEqual(multiline.0, "message")
        expectEqual(String(data: multiline.1, encoding: .utf8), "first\nsecond")
    }
    @Test func testPrivateEmptyDefaultsAndPermissions() throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let storage = try PrivateStorage(root: root)
        let config = try storage.load()
        expectTrue(config.sources.isEmpty)
        expectEqual(config.listen, "127.0.0.1:0")
        expectEqual(config.tokenEnv, "NIGHTWIRE_TOKEN")
        expectEqual(config.statePath, storage.stateURL.path)
        let rootMode = try FileManager.default.attributesOfItem(atPath: root.path)[.posixPermissions] as? Int
        let configMode = try FileManager.default.attributesOfItem(atPath: storage.configURL.path)[.posixPermissions] as? Int
        expectEqual(rootMode, 0o700)
        expectEqual(configMode, 0o600)
        let wire = try String(contentsOf: storage.configURL)
        expectFalse(wire.contains("Authorization"))
        let run1 = try storage.newRun(), run2 = try storage.newRun()
        expectNotEqual(run1, run2)
    }
    @Test func testConfigRejectsSymlinks() throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let storage = try PrivateStorage(root: root)
        try FileManager.default.createSymbolicLink(at: storage.configURL, withDestinationURL: root.appendingPathComponent("missing"))
        expectThrows(try storage.save(AppConfiguration(statePath: storage.stateURL.path)))
    }
    @Test func testSourceStatusIncludesFilterAndCrawlBacklog() throws {
        let json = #"{"id":"source","transport":"tor","last_attempt":"2026-11-13T12:00:00Z","last_success":"2026-11-13T12:00:00Z","next_check":"2026-11-13T12:01:00Z","consecutive_failures":0,"matching_items":3,"filtered_items":7,"pending_pages":2,"tracked_threads":4,"failed_pages":1}"#
        let status = try JSONDecoder().decode(SourceStatus.self, from: Data(json.utf8))
        expectEqual(status.id, "source")
        expectEqual(status.matchingItems, 3)
        expectEqual(status.filteredItems, 7)
        expectEqual(status.pendingPages, 2)
        expectEqual(status.trackedThreads, 4)
        expectEqual(status.failedPages, 1)
    }
    @Test func testSparseBackendFilterAndSourceDecode() throws {
        let filter = try JSONDecoder().decode(AlertFilter.self, from: Data(#"{"readable_only":true}"#.utf8))
        expectTrue(filter.readableOnly)
        expectEqual(filter.excludeTitle, "")
        let source = try JSONDecoder().decode(Source.self, from: Data(#"{"id":"stable","url":"http://example.onion/","transport":"tor","format":"feed","interval_seconds":60}"#.utf8))
        expectEqual(source.fetcher, "http")
        expectEqual(source.alertFilter, AlertFilter())
    }
    @Test func testProfileLockRejectsConcurrentInstances() throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        do {
            let first = try PrivateStorage(root: root)
            try withExtendedLifetime(first) { expectThrows(try PrivateStorage(root: root)) }
        }
        expectNoThrow(try PrivateStorage(root: root))
    }
    @Test func testLaunchIsolationArguments() throws {
        expectNil(try LaunchOptions(arguments: ["Nightwire"]).root)
        let smoke = try LaunchOptions(arguments: ["Nightwire", "--smoke-test", "/tmp/private-run"])
        expectTrue(smoke.smoke)
        expectEqual(smoke.root?.path, "/tmp/private-run")
        expectFalse(try LaunchOptions(arguments: ["Nightwire", "--data-dir", "/tmp/private-run"]).smoke)
        expectThrows(try LaunchOptions(arguments: ["Nightwire", "--smoke-test"]))
        expectThrows(try LaunchOptions(arguments: ["Nightwire", "--data-dir", "relative"]))
        expectThrows(try LaunchOptions(arguments: ["Nightwire", "--data-dir", "/tmp/a", "--smoke-test", "/tmp/b"]))
    }
}
