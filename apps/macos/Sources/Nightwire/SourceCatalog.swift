import Foundation

struct CatalogSource: Decodable, Identifiable {
    let id: String
    let name: String
    let url: String
    let format: String
    let category: String
    let evidence: String
    let notes: String

    func makeSource() -> Source {
        var source = Source()
        source.url = url
        source.format = format
        return source
    }

    func isConfigured(in sources: [Source]) -> Bool {
        sources.contains { $0.format == format && Self.bindingURL($0.url) == Self.bindingURL(url) }
    }

    private static func bindingURL(_ text: String) -> String {
        guard var parts = URLComponents(string: text) else { return text }
        parts.scheme = parts.scheme?.lowercased()
        parts.host = parts.host?.lowercased()
        parts.fragment = nil
        if parts.path.hasSuffix("/") { parts.path.removeLast() }
        return parts.string ?? text
    }
}

enum SourceCatalog {
    static func load() throws -> [CatalogSource] {
        let bundle = Bundle.main.bundleURL.pathExtension == "app" ? Bundle.main : Bundle.module
        guard let url = bundle.url(forResource: "source-catalog", withExtension: "json") else {
            throw ClientError.message("The bundled source catalog is missing. You can still add a custom source.")
        }
        return try JSONDecoder().decode([CatalogSource].self, from: Data(contentsOf: url))
    }
}
