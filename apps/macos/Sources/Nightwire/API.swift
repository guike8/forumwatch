import Foundation

// Refuse redirects, even loopback-to-loopback, so an unexpected server cannot move
// the bearer credential. No cookies, cache, system HTTP proxy, or external requests.
final class LocalOnlyDelegate: NSObject, URLSessionTaskDelegate {
    func urlSession(_ session: URLSession, task: URLSessionTask,
                    willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest,
                    completionHandler: @escaping (URLRequest?) -> Void) {
        completionHandler(nil)
    }
}

final class LocalAPI {
    let session: URLSession
    let base: URL
    private let token: String
    init(base: URL, token: String) {
        self.base = base
        self.token = token
        let configuration = URLSessionConfiguration.ephemeral
        configuration.connectionProxyDictionary = [:]
        configuration.httpShouldSetCookies = false
        configuration.urlCache = nil
        configuration.timeoutIntervalForRequest = 45
        configuration.timeoutIntervalForResource = 24 * 60 * 60
        session = URLSession(configuration: configuration, delegate: LocalOnlyDelegate(), delegateQueue: nil)
    }
    func request(_ path: String) -> URLRequest {
        var request = URLRequest(url: base.appendingPathComponent(path))
        request.setValue("Bearer " + token, forHTTPHeaderField: "Authorization")
        request.cachePolicy = .reloadIgnoringLocalCacheData
        return request
    }
    func check(_ response: URLResponse) throws {
        guard let http = response as? HTTPURLResponse, http.statusCode == 200 else {
            throw ClientError.message("Local API request failed (HTTP \((response as? HTTPURLResponse)?.statusCode ?? 0)).")
        }
    }
    func get<T: Decodable>(_ path: String, as type: T.Type) async throws -> T {
        let (data, response) = try await session.data(for: request(path))
        try check(response)
        return try JSONDecoder().decode(type, from: data)
    }
    func close() { session.invalidateAndCancel() }
}

// AsyncBytes.lines is unsuitable for SSE: Foundation can omit empty lines.
// Consume bytes directly so LF/CRLF/CR blank-frame delimiters are preserved.
struct SSEByteParser {
    private var parser = SSEParser()
    private var lineBytes: [UInt8] = []
    private var previousCR = false
    mutating func byte(_ value: UInt8) throws -> (String, Data)? {
        if value == 10 && previousCR { previousCR = false; return nil }
        if value == 10 || value == 13 {
            previousCR = value == 13
            guard let line = String(bytes: lineBytes, encoding: .utf8) else {
                throw ClientError.message("Live event was not UTF-8.")
            }
            lineBytes.removeAll(keepingCapacity: true)
            return try parser.line(line)
        }
        previousCR = false
        guard lineBytes.count < 4 * 1024 * 1024 else {
            throw ClientError.message("Live event line exceeded the size limit.")
        }
        lineBytes.append(value)
        return nil
    }
}

struct SSEParser {
    private var event = "message"
    private var data: [String] = []
    mutating func line(_ line: String) throws -> (String, Data)? {
        if line.isEmpty {
            defer { event = "message"; data = [] }
            guard !data.isEmpty else { return nil }
            return (event, Data(data.joined(separator: "\n").utf8))
        }
        if line.hasPrefix(":") { return nil }
        let parts = line.split(separator: ":", maxSplits: 1, omittingEmptySubsequences: false)
        var value = parts.count > 1 ? String(parts[1]) : ""
        if value.first == " " { value.removeFirst() }
        if parts[0] == "event" { event = value }
        if parts[0] == "data" {
            // Bounded frame size; reconnect + snapshot instead of retaining arbitrary data.
            guard data.reduce(0, { $0 + $1.utf8.count }) + value.utf8.count <= 4 * 1024 * 1024 else {
                throw ClientError.message("Live event exceeded the size limit.")
            }
            data.append(value)
        }
        return nil
    }
}
