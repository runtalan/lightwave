import Foundation
import Observation

/// Talks to the desktop app's phone server: control calls are POSTs to
/// /_lw/call and state arrives on the /_lw/events SSE stream. Names, pad
/// positions and palettes all come from the desktop; nothing is configured
/// here beyond where the desktop is.
@MainActor
@Observable
final class LightwaveClient {
    enum Status: Equatable {
        case idle
        case connecting
        case live
        case failed(String)
    }

    private(set) var state = HUDState()
    private(set) var status = Status.idle
    private(set) var hasState = false

    /// "host:port", e.g. "192.168.1.20:8787" or "100.92.4.7:8787".
    var address: String {
        didSet { UserDefaults.standard.set(address, forKey: Keys.address) }
    }
    var token: String {
        didSet { UserDefaults.standard.set(token, forKey: Keys.token) }
    }
    /// Bonjour name of the Mac last picked, so a new DHCP address is followed.
    var serviceName: String {
        didSet { UserDefaults.standard.set(serviceName, forKey: Keys.service) }
    }

    private enum Keys {
        static let address = "lw.address"
        static let token = "lw.token"
        static let service = "lw.service"
    }

    private var streamTask: Task<Void, Never>?
    private let session: URLSession

    init() {
        let d = UserDefaults.standard
        address = d.string(forKey: Keys.address) ?? ""
        token = d.string(forKey: Keys.token) ?? ""
        serviceName = d.string(forKey: Keys.service) ?? ""
        let cfg = URLSessionConfiguration.default
        cfg.timeoutIntervalForRequest = 8
        cfg.waitsForConnectivity = false
        session = URLSession(configuration: cfg)
    }

    var isConfigured: Bool { !address.trimmingCharacters(in: .whitespaces).isEmpty }

    // MARK: Connection

    func start() {
        guard isConfigured, streamTask == nil else { return }
        streamTask = Task { [weak self] in await self?.run() }
    }

    func stop() {
        streamTask?.cancel()
        streamTask = nil
        if status != .idle { status = .idle }
    }

    func restart() {
        stop()
        start()
    }

    /// Fetch a snapshot, then follow the event stream, reconnecting with
    /// backoff after sleeps, Wi-Fi hand-offs, or a desktop restart.
    private func run() async {
        var delay: UInt64 = 1
        while !Task.isCancelled {
            status = .connecting
            do {
                try await refresh()
                status = .live
                delay = 1
                try await followEvents()
            } catch is CancellationError {
                return
            } catch {
                if Task.isCancelled { return }
                status = .failed(Self.describe(error))
            }
            try? await Task.sleep(nanoseconds: delay * 1_000_000_000)
            delay = min(delay * 2, 10)
        }
    }

    private func followEvents() async throws {
        var req = URLRequest(url: try url("/_lw/events"))
        req.timeoutInterval = 60 // the server pings every 25s
        req.setValue("text/event-stream", forHTTPHeaderField: "Accept")
        let (bytes, resp) = try await session.bytes(for: req)
        try Self.check(resp)
        for try await line in bytes.lines {
            guard line.hasPrefix("data:") else { continue }
            let body = Data(line.dropFirst(5).utf8)
            if let event = try? JSONDecoder().decode(Event.self, from: body), event.event == "state" {
                apply(event.data)
            }
        }
        throw URLError(.networkConnectionLost)
    }

    private struct Event: Decodable {
        let event: String
        let data: HUDState
    }

    private func apply(_ next: HUDState) {
        hasState = true
        if status != .live { status = .live }
        if next != state { state = next }
    }

    // MARK: Calls

    func refresh() async throws {
        if let s = try await call("GetState") { apply(s) }
    }

    @discardableResult
    func call(_ method: String, _ args: [Int] = []) async throws -> HUDState? {
        var req = URLRequest(url: try url("/_lw/call"))
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try JSONSerialization.data(withJSONObject: ["method": method, "args": args])
        let (data, resp) = try await session.data(for: req)
        try Self.check(resp)
        let reply = try JSONDecoder().decode(Reply.self, from: data)
        if let err = reply.error { throw ServerError(message: err) }
        if let s = reply.result { apply(s) }
        return reply.result
    }

    /// Fire-and-forget for button taps; the reply and the state push both
    /// carry the outcome.
    func send(_ method: String, _ args: Int...) {
        Task {
            do {
                try await call(method, args)
            } catch {
                status = .failed(Self.describe(error))
                if streamTask == nil { start() }
            }
        }
    }

    // Slider drags produce far more values than the lights can take. Keep one
    // request in flight and send only the newest value when it lands.
    private var pending: [String: Int] = [:]
    private var inFlight: Set<String> = []

    func sendLatest(_ method: String, _ value: Int) {
        pending[method] = value
        guard !inFlight.contains(method) else { return }
        inFlight.insert(method)
        Task {
            while let v = pending.removeValue(forKey: method) {
                _ = try? await call(method, [v])
            }
            inFlight.remove(method)
        }
    }

    private struct Reply: Decodable {
        let result: HUDState?
        let error: String?

        enum CodingKeys: String, CodingKey { case result, error }

        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            error = try c.decodeIfPresent(String.self, forKey: .error)
            // Not every call returns a full state (GetPaletteIndex returns a
            // number); those simply carry no snapshot.
            result = try? c.decodeIfPresent(HUDState.self, forKey: .result)
        }
    }

    struct ServerError: LocalizedError {
        let message: String
        var errorDescription: String? { message }
    }

    // MARK: Helpers

    private func url(_ path: String) throws -> URL {
        var raw = address.trimmingCharacters(in: .whitespacesAndNewlines)
        if !raw.contains("://") { raw = "http://" + raw }
        guard var comps = URLComponents(string: raw), comps.host?.isEmpty == false else {
            throw URLError(.badURL)
        }
        if comps.port == nil { comps.port = 8787 }
        comps.path = path
        let t = token.trimmingCharacters(in: .whitespacesAndNewlines)
        comps.queryItems = t.isEmpty ? nil : [URLQueryItem(name: "token", value: t)]
        guard let url = comps.url else { throw URLError(.badURL) }
        return url
    }

    private static func check(_ resp: URLResponse) throws {
        guard let http = resp as? HTTPURLResponse else { return }
        switch http.statusCode {
        case 200: return
        case 403: throw ServerError(message: "Refused — check the token in Lightwave's Remote settings.")
        default: throw ServerError(message: "Server replied \(http.statusCode).")
        }
    }

    private static func describe(_ error: Error) -> String {
        if let e = error as? ServerError { return e.message }
        if let e = error as? URLError {
            switch e.code {
            case .cannotConnectToHost, .timedOut, .cannotFindHost, .networkConnectionLost:
                return "Can't reach the Mac. Is Remote switched on in Lightwave's Config?"
            case .notConnectedToInternet:
                return "No network connection."
            default: break
            }
        }
        return error.localizedDescription
    }
}
