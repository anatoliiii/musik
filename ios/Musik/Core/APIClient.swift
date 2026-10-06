import Foundation

struct APIError: LocalizedError {
    let status: Int
    let code: String?
    let message: String

    var errorDescription: String? { message }
    var isUnauthorized: Bool { status == 401 }
    var isNotFound: Bool { status == 404 }
}

private struct ErrorEnvelope: Decodable {
    let error: String?
    let code: String?
}

/// Thin JSON client for the Go player (`:8787`). Auth is a Bearer token when one is
/// stored, otherwise the `musik_session` cookie set by password login.
final class APIClient: @unchecked Sendable {
    let settings: Settings
    let session: URLSession
    /// Called on the main actor when the server answers 401 to an authed request.
    var onUnauthorized: (@MainActor () -> Void)?

    private let decoder: JSONDecoder = {
        let d = JSONDecoder()
        d.keyDecodingStrategy = .convertFromSnakeCase
        return d
    }()

    init(settings: Settings) {
        self.settings = settings
        let cfg = URLSessionConfiguration.default
        cfg.httpCookieStorage = .shared
        cfg.httpCookieAcceptPolicy = .always
        cfg.urlCache = URLCache(memoryCapacity: 32 << 20, diskCapacity: 256 << 20)
        cfg.timeoutIntervalForRequest = 20
        session = URLSession(configuration: cfg)
    }

    // MARK: URLs

    func url(_ path: String, query: [URLQueryItem] = []) throws -> URL {
        let base = settings.baseURL
        guard !base.isEmpty, var comps = URLComponents(string: base + path) else {
            throw APIError(status: 0, code: "bad_url", message: "Неверный адрес сервера")
        }
        if !query.isEmpty {
            comps.queryItems = (comps.queryItems ?? []) + query
            // Go decodes "+" in a query as a space; keep literal pluses literal.
            comps.percentEncodedQuery = comps.percentEncodedQuery?
                .replacingOccurrences(of: "+", with: "%2B")
        }
        guard let url = comps.url else {
            throw APIError(status: 0, code: "bad_url", message: "Неверный адрес сервера")
        }
        return url
    }

    func authHeaders() -> [String: String] {
        if let token = settings.token, !token.isEmpty {
            return ["Authorization": "Bearer \(token)"]
        }
        return [:]
    }

    func cookies(for url: URL) -> [HTTPCookie] {
        HTTPCookieStorage.shared.cookies(for: url) ?? []
    }

    func artworkURL(trackId: Int, width: Int) -> URL? {
        try? url("/api/artwork/\(trackId)", query: [URLQueryItem(name: "w", value: String(width))])
    }

    func streamURL(for track: Track) -> URL? {
        let path = track.stream ?? "/api/stream/\(track.id)"
        let query = settings.mobileStream ? [URLQueryItem(name: "q", value: "mobile")] : []
        return try? url(path, query: query)
    }

    // MARK: Requests

    func send(_ method: String, _ path: String, query: [URLQueryItem] = [],
              body: [String: Any]? = nil, redirectOn401: Bool = true) async throws -> Data {
        var req = URLRequest(url: try url(path, query: query))
        req.httpMethod = method
        req.setValue("application/json", forHTTPHeaderField: "Accept")
        for (k, v) in authHeaders() { req.setValue(v, forHTTPHeaderField: k) }
        if method != "GET" {
            req.setValue("application/json", forHTTPHeaderField: "Content-Type")
            req.httpBody = try JSONSerialization.data(withJSONObject: body ?? [:])
        }
        let data: Data
        let resp: URLResponse
        do {
            (data, resp) = try await session.data(for: req)
        } catch let e as URLError where e.code == .cancelled {
            throw CancellationError()
        } catch let e as URLError {
            throw APIError(status: 0, code: "network", message: Self.describe(e))
        }
        let status = (resp as? HTTPURLResponse)?.statusCode ?? 0
        guard (200..<300).contains(status) else {
            let env = try? decoder.decode(ErrorEnvelope.self, from: data)
            if status == 401 && redirectOn401, let handler = onUnauthorized {
                await handler()
            }
            throw APIError(status: status, code: env?.code,
                           message: env?.error ?? "Ошибка сервера (HTTP \(status))")
        }
        return data
    }

    func decode<T: Decodable>(_ type: T.Type, from data: Data) throws -> T {
        do {
            return try decoder.decode(type, from: data)
        } catch {
            throw APIError(status: 0, code: "decode", message: "Неожиданный ответ сервера")
        }
    }

    func get<T: Decodable>(_ path: String, query: [URLQueryItem] = [], as type: T.Type = T.self) async throws -> T {
        try decode(type, from: try await send("GET", path, query: query))
    }

    func post<T: Decodable>(_ path: String, _ body: [String: Any]? = nil, as type: T.Type = T.self) async throws -> T {
        try decode(type, from: try await send("POST", path, body: body))
    }

    /// Fetches raw bytes (artwork) with the same auth as API calls.
    func data(from url: URL) async throws -> Data {
        var req = URLRequest(url: url)
        for (k, v) in authHeaders() { req.setValue(v, forHTTPHeaderField: k) }
        let (data, resp) = try await session.data(for: req)
        let status = (resp as? HTTPURLResponse)?.statusCode ?? 0
        guard (200..<300).contains(status) else {
            throw APIError(status: status, code: nil, message: "HTTP \(status)")
        }
        return data
    }

    private static func describe(_ e: URLError) -> String {
        switch e.code {
        case .notConnectedToInternet: return "Нет сети"
        case .timedOut: return "Сервер не отвечает"
        case .cannotConnectToHost, .cannotFindHost: return "Не удаётся подключиться к серверу"
        default: return e.localizedDescription
        }
    }
}
