import Foundation
import Darwin

struct AlertFilter: Codable, Equatable {
    var include = ""
    var exclude = ""
    var excludeTitle = ""
    var readableOnly = false
    var quietSales = false
    init(include: String = "", exclude: String = "", excludeTitle: String = "", readableOnly: Bool = false, quietSales: Bool = false) {
        self.include = include; self.exclude = exclude; self.excludeTitle = excludeTitle; self.readableOnly = readableOnly
        self.quietSales = quietSales
    }
    var isActive: Bool { quietSales || readableOnly || !include.isEmpty || !exclude.isEmpty || !excludeTitle.isEmpty }
    init(from decoder: Decoder) throws {
        let values = try decoder.container(keyedBy: CodingKeys.self)
        include = try values.decodeIfPresent(String.self, forKey: .include) ?? ""
        exclude = try values.decodeIfPresent(String.self, forKey: .exclude) ?? ""
        excludeTitle = try values.decodeIfPresent(String.self, forKey: .excludeTitle) ?? ""
        readableOnly = try values.decodeIfPresent(Bool.self, forKey: .readableOnly) ?? false
        quietSales = try values.decodeIfPresent(Bool.self, forKey: .quietSales) ?? false
    }
    enum CodingKeys: String, CodingKey {
        case include, exclude
        case excludeTitle = "exclude_title", readableOnly = "readable_only", quietSales = "quiet_sales"
    }
}

struct Source: Codable, Identifiable, Equatable {
    var id = UUID().uuidString.lowercased()
    var url = ""
    var transport = "tor"
    var fetcher = "http"
    var format = "feed"
    var intervalSeconds = 60
    var includeURL = ""
    var maxPagesPerPoll = 4
    var maxThreads = 256
    var requestGapSeconds = 2
    var alertFilter = AlertFilter()
    init() { }
    init(from decoder: Decoder) throws {
        let values = try decoder.container(keyedBy: CodingKeys.self)
        id = try values.decode(String.self, forKey: .id)
        url = try values.decode(String.self, forKey: .url)
        transport = try values.decodeIfPresent(String.self, forKey: .transport) ?? "tor"
        fetcher = try values.decodeIfPresent(String.self, forKey: .fetcher) ?? "http"
        format = try values.decodeIfPresent(String.self, forKey: .format) ?? "feed"
        intervalSeconds = try values.decodeIfPresent(Int.self, forKey: .intervalSeconds) ?? 60
        includeURL = try values.decodeIfPresent(String.self, forKey: .includeURL) ?? ""
        maxPagesPerPoll = try values.decodeIfPresent(Int.self, forKey: .maxPagesPerPoll) ?? 4
        maxThreads = try values.decodeIfPresent(Int.self, forKey: .maxThreads) ?? 256
        requestGapSeconds = try values.decodeIfPresent(Int.self, forKey: .requestGapSeconds) ?? 2
        alertFilter = try values.decodeIfPresent(AlertFilter.self, forKey: .alertFilter) ?? AlertFilter()
    }
    static let formats = ["feed", "html", "xenforo", "xenforo-index", "mybb", "mybb-index"]
    var displayName: String { URL(string: url)?.host ?? url }
    enum CodingKeys: String, CodingKey {
        case id, url, transport, fetcher, format
        case intervalSeconds = "interval_seconds", includeURL = "include_url"
        case maxPagesPerPoll = "max_pages_per_poll", maxThreads = "max_threads"
        case requestGapSeconds = "request_gap_seconds", alertFilter = "alert_filter"
    }
    func validated(replacing original: Source?) throws -> Source {
        guard !id.isEmpty else { throw ClientError.message("A source ID cannot be empty.") }
        guard let parsed = URLComponents(string: url), ["http", "https"].contains(parsed.scheme ?? ""),
              let host = parsed.host, !host.isEmpty, parsed.user == nil, parsed.password == nil,
              parsed.port == nil || (1...65535).contains(parsed.port!) else {
            throw ClientError.message("Enter an HTTP(S) source URL without credentials.")
        }
        guard Self.formats.contains(format), transport == "tor", fetcher == "http" else {
            throw ClientError.message("Nightwire supports Tor transport and the HTTP fetcher only.")
        }
        guard (10...86400).contains(intervalSeconds), (1...32).contains(maxPagesPerPoll),
              (1...4096).contains(maxThreads), (1...60).contains(requestGapSeconds) else {
            throw ClientError.message("Check the polling interval and crawl bounds.")
        }
        guard format != "html" || !includeURL.isEmpty else {
            throw ClientError.message("HTML sources require an include URL expression.")
        }
        var result = self
        // A crawl checkpoint belongs permanently to its original URL and format.
        if let original, original.url != url || original.format != format {
            result.id = UUID().uuidString.lowercased()
        }
        return result
    }
}

struct Enrichment: Codable, Equatable {
    var method: String
    var text: String
    var sourceHash: String
    enum CodingKeys: String, CodingKey { case method, text; case sourceHash = "source_hash" }
}

struct Item: Decodable, Identifiable, Equatable {
    var id: String
    var source: String
    var title: String
    var url: String
    var summary: String?
    var author: String?
    var body: String?
    var access: String?
    var hiddenLinks: Int?
    var published: String?
    var firstSeen: String
    var observedAt: String
    var kind: String
    var hash: String
    var enrichment: Enrichment?
    enum CodingKeys: String, CodingKey {
        case id, source, title, url, summary, author, body, access, published, kind, hash, enrichment
        case hiddenLinks = "hidden_links", firstSeen = "first_seen", observedAt = "observed_at"
    }
    var observedDate: Date { Self.parseDate(observedAt) ?? .distantPast }
    static func parseDate(_ text: String) -> Date? {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return formatter.date(from: text) ?? ISO8601DateFormatter().date(from: text)
    }
    var localExtract: String? {
        guard access != "restricted", let enrichment, enrichment.method == "local/extractive",
              enrichment.sourceHash == hash else { return nil }
        return enrichment.text
    }
}

struct ItemEvent: Decodable {
    var id: String
    var filtered: Bool?
    var observedAt: String?
    var hash: String?
    enum CodingKeys: String, CodingKey { case id, filtered, hash; case observedAt = "observed_at" }
}

// Go emits RFC3339Nano. Date formatters may truncate fractional seconds; retain
// nanoseconds separately so a stale event within the same millisecond cannot win.
struct Observation: Comparable {
    let second: Date
    let nanosecond: Int
    init(_ text: String) {
        if let dot = text.firstIndex(of: ".") {
            let suffix = text[text.index(after: dot)...]
            let digits = suffix.prefix(while: { $0.isNumber })
            second = Item.parseDate(String(text[..<dot]) + suffix.dropFirst(digits.count)) ?? .distantPast
            nanosecond = Int(String((digits + "000000000").prefix(9))) ?? 0
        } else {
            second = Item.parseDate(text) ?? .distantPast
            nanosecond = 0
        }
    }
    static func < (lhs: Self, rhs: Self) -> Bool {
        lhs.second == rhs.second ? lhs.nanosecond < rhs.nanosecond : lhs.second < rhs.second
    }
}

// Revisions order by observation time, not event delivery order. Enrichment can arrive
// later with the exact same observation and hash; an older snapshot must not erase it.
struct ItemIndex {
    private(set) var items: [String: Item] = [:]
    private var tombstones: [String: Observation] = [:]
    mutating func upsert(_ incoming: Item) {
        let observed = Observation(incoming.observedAt)
        if let removed = tombstones[incoming.id], observed <= removed { return }
        if let current = items[incoming.id] {
            guard observed >= Observation(current.observedAt) else { return }
            var merged = incoming
            if incoming.hash == current.hash && incoming.enrichment == nil {
                merged.enrichment = current.enrichment
            }
            items[incoming.id] = merged
        } else { items[incoming.id] = incoming }
    }
    mutating func remove(_ event: ItemEvent) {
        let date = Observation(event.observedAt ?? items[event.id]?.observedAt ?? "")
        if let current = items[event.id], date < Observation(current.observedAt) { return }
        if let previous = tombstones[event.id], date < previous { return }
        tombstones[event.id] = date
        items.removeValue(forKey: event.id)
    }
    mutating func snapshot(_ snapshot: [Item]) {
        let previous = items
        items = [:]
        tombstones = [:]
        for var item in snapshot {
            if let old = previous[item.id], old.hash == item.hash, item.enrichment == nil {
                item.enrichment = old.enrichment
            }
            upsert(item)
        }
    }
}

struct SourceStatus: Decodable, Identifiable {
    var id: String
    var transport: String
    var lastAttempt: String
    var lastSuccess: String
    var nextCheck: String
    var error: String?
    var consecutiveFailures: Int
    var matchingItems: Int?
    var filteredItems: Int?
    var pendingPages: Int?
    var trackedThreads: Int?
    var failedPages: Int?
    enum CodingKeys: String, CodingKey {
        case id, transport, error
        case lastAttempt = "last_attempt", lastSuccess = "last_success", nextCheck = "next_check"
        case consecutiveFailures = "consecutive_failures", matchingItems = "matching_items", filteredItems = "filtered_items"
        case pendingPages = "pending_pages", trackedThreads = "tracked_threads", failedPages = "failed_pages"
    }
}

struct Ready: Decodable {
    var pid: Int32
    var address: String
    func endpoint(expectedPID: Int32) throws -> URL {
        guard pid == expectedPID, var parts = URLComponents(string: "http://" + address),
              parts.user == nil, parts.password == nil, parts.path.isEmpty,
              parts.query == nil, parts.fragment == nil, let port = parts.port,
              (1...65535).contains(port), let host = parts.host else {
            throw ClientError.message("Backend readiness has an invalid PID or address.")
        }
        // No DNS names: a ready file can only direct the bearer token to numeric loopback.
        let numericHost = host.trimmingCharacters(in: CharacterSet(charactersIn: "[]"))
        var v4 = in_addr()
        var v6 = in6_addr()
        let valid4 = inet_pton(AF_INET, numericHost, &v4) == 1 && (UInt32(bigEndian: v4.s_addr) >> 24) == 127
        let valid6 = inet_pton(AF_INET6, numericHost, &v6) == 1 && numericHost == "::1"
        guard valid4 || valid6 else { throw ClientError.message("Backend address is not numeric loopback.") }
        parts.path = "/"
        guard let url = parts.url else { throw ClientError.message("Invalid backend URL.") }
        return url
    }
}

enum ClientError: LocalizedError {
    case message(String)
    var errorDescription: String? { if case let .message(text) = self { return text }; return nil }
}
