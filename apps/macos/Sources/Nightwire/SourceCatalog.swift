import Foundation

enum CatalogStatus: String, Decodable, CaseIterable {
    case ready
    case needsVerification = "needs_verification"
    case loginRequired = "login_required"
    case unsupported, challenge, unavailable

    var label: String {
        switch self {
        case .ready: return "Ready"
        case .needsVerification: return "Needs verification"
        case .loginRequired: return "Login required"
        case .unsupported: return "Unsupported"
        case .challenge: return "Challenge"
        case .unavailable: return "Unavailable"
        }
    }

    var permitsConfiguration: Bool { self == .ready || self == .needsVerification }

    var guidance: String {
        switch self {
        case .ready, .needsVerification: return "Review the evidence and source settings before saving."
        case .loginRequired: return "Wait for verified public access; Nightwire does not sign in or bypass access restrictions."
        case .unsupported: return "A supported HTTP collector format must be verified before this entry can be configured."
        case .challenge: return "Wait for verified public HTTP access; Nightwire does not bypass challenges."
        case .unavailable: return "Wait for a new evidence check confirming availability before configuring this entry."
        }
    }
}

struct CatalogSource: Decodable, Identifiable {
    let id: String
    let name: String
    let url: String
    let format: String?
    let category: String
    let evidence: String
    let notes: String
    let status: CatalogStatus
    let checkedAt: String

    enum CodingKeys: String, CodingKey {
        case id, name, url, format, category, evidence, notes, status
        case checkedAt = "checked_at"
    }

    var statusLabel: String { status.label }
    var canConfigure: Bool {
        status.permitsConfiguration && format.map(Source.formats.contains) == true && (try? validate()) != nil
    }

    func makeSource() throws -> Source {
        try validate()
        guard status.permitsConfiguration, let format, Source.formats.contains(format) else {
            throw ClientError.message("\(name) is research-only (\(statusLabel)). \(status.guidance) Copy the link to investigate separately; no source was added.")
        }
        var source = Source()
        source.url = url
        source.format = format
        return source
    }

    func validate() throws {
        guard [id, name, category, evidence, notes].allSatisfy({ !$0.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }) else {
            throw ClientError.message("Catalog entry \(id) has missing descriptive fields. Correct the catalog or use a custom source.")
        }
        guard !url.contains(where: { $0.isWhitespace || $0.isNewline }),
              !url.unicodeScalars.contains(where: CharacterSet.controlCharacters.contains),
              let parts = URLComponents(string: url), ["http", "https"].contains(parts.scheme ?? ""),
              let host = parts.host, !host.isEmpty, parts.url != nil,
              parts.user == nil, parts.password == nil,
              parts.port == nil || (1...65535).contains(parts.port!) else {
            throw ClientError.message("Catalog entry \(id) needs a valid HTTP(S) URL without credentials. Correct the catalog before configuring it.")
        }
        let formatter = DateFormatter()
        formatter.locale = Locale(identifier: "en_US_POSIX")
        formatter.calendar = Calendar(identifier: .gregorian)
        formatter.timeZone = TimeZone(secondsFromGMT: 0)
        formatter.dateFormat = "yyyy-MM-dd"
        formatter.isLenient = false
        guard checkedAt.range(of: #"^[0-9]{4}-[0-9]{2}-[0-9]{2}$"#, options: .regularExpression) != nil,
              let date = formatter.date(from: checkedAt), formatter.string(from: date) == checkedAt else {
            throw ClientError.message("Catalog entry \(id) needs a checked_at evidence date in YYYY-MM-DD form. Record the actual check date before configuring it.")
        }
        guard !status.permitsConfiguration || format.map(Source.formats.contains) == true else {
            throw ClientError.message("Catalog entry \(id) is marked \(statusLabel) but has no supported format. Verify its collector format or mark it research-only.")
        }
    }

    func isConfigured(in sources: [Source]) -> Bool {
        sources.contains { $0.format == format && Self.bindingURL($0.url) == Self.bindingURL(url) }
    }

    static func bindingURL(_ text: String) -> String {
        guard var parts = URLComponents(string: text) else { return text }
        parts.scheme = parts.scheme?.lowercased()
        parts.host = parts.host?.lowercased()
        if (parts.scheme == "http" && parts.port == 80) || (parts.scheme == "https" && parts.port == 443) { parts.port = nil }
        parts.fragment = nil
        if parts.path.hasSuffix("/") { parts.path.removeLast() }
        return parts.string ?? text
    }
}

enum SourceCatalog {
    static func decode(_ data: Data) throws -> [CatalogSource] {
        let entries = try JSONDecoder().decode([CatalogSource].self, from: data)
        var ids = Set<String>()
        var urls = Set<String>()
        for entry in entries {
            try entry.validate()
            guard ids.insert(entry.id).inserted, urls.insert(CatalogSource.bindingURL(entry.url)).inserted else {
                throw ClientError.message("The source catalog has a duplicate ID or URL (\(entry.id)). Correct the catalog or add a custom source.")
            }
        }
        return entries
    }

    static func load() throws -> [CatalogSource] {
        let bundle = Bundle.main.bundleURL.pathExtension == "app" ? Bundle.main : Bundle.module
        guard let url = bundle.url(forResource: "source-catalog", withExtension: "json") else {
            throw ClientError.message("The bundled source catalog is missing. You can still add a custom source.")
        }
        return try decode(Data(contentsOf: url))
    }
}
