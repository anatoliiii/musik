import Foundation

// DTOs for the Go player API. Keys are decoded with `.convertFromSnakeCase`,
// so `impression_id` maps to `impressionId`, `cover_track_id` to `coverTrackId`, etc.
// Almost every field is optional: the server omits empty values.

struct Track: Decodable, Identifiable, Hashable {
    var id: Int
    var artist: String?
    var title: String?
    var album: String?
    var duration: Double?
    var stream: String?
    var artwork: String?
    var impressionId: String?
    var requestId: String?
    var source: String?
    var position: Int?
    var current: Bool?
    var explanation: String?

    var displayTitle: String { title.nonEmpty ?? "#\(id)" }
    var displayArtist: String { artist ?? "" }
    var subtitle: String { [artist, album].compactMap { $0.nonEmpty }.joined(separator: " · ") }
}

/// An upcoming radio item (`sess.Queue`), keyed by `track_id` rather than `id`.
struct QueueItem: Decodable, Hashable {
    var trackId: Int
    var artist: String?
    var title: String?
    var album: String?
    var duration: Double?
    var source: String?
    var impressionId: String?
    var explanation: String?
    var newBoost: Bool?

    var asTrack: Track {
        Track(id: trackId, artist: artist, title: title, album: album, duration: duration,
              stream: "/api/stream/\(trackId)", artwork: "/api/artwork/\(trackId)",
              impressionId: impressionId, source: source)
    }
}

/// Response of radio/start, play, mixes/{kind}/play, session/jump|back and now.
struct PlayPayload: Decodable {
    var sessionId: String?
    var mode: String?
    var kind: String?
    var name: String?
    var index: Int?
    var count: Int?
    var current: Track?
    var queue: [QueueItem]?
    var tracks: [Track]?
    var fixed: Bool?
    var maturity: String?
}

/// Response of POST /api/events. `current` is a bare id here, so it is not decoded.
struct EventResult: Decodable {
    var sessionId: String?
    var maturity: String?
    var next: Track?
    var queue: [QueueItem]?
    var tracks: [Track]?
    var ended: Bool?
    var index: Int?
    var name: String?
    var fixed: Bool?
    var rating: String?
    var ignored: Bool?
}

struct Mix: Decodable, Identifiable, Hashable {
    var kind: String
    var title: String
    var subtitle: String?
    var highlight: Bool?
    var today: Bool?
    var tracks: Int?
    var ready: Bool?
    var coverTrackId: Int?
    var stale: Bool?

    var id: String { kind }
    var isWeekday: Bool { kind.hasPrefix("weekday_") }

    var metaLabel: String {
        let n = tracks ?? 0
        switch kind {
        case "later": return n > 0 ? "\(n) в очереди" : "пусто — добавь с плеера"
        case "favorites": return n > 0 ? "\(n) ♥" : "жми ♥ на треке"
        default: return ready == true ? "\(n) треков" : "нажми «Обновить миксы»"
        }
    }
}

struct MixesResponse: Decodable {
    var mixes: [Mix]
}

struct ArtistItem: Decodable, Identifiable, Hashable {
    var artist: String
    var tracks: Int?
    var coverTrackId: Int?
    var artwork: String?
    var explanation: String?

    var id: String { artist }
}

struct AlbumItem: Decodable, Identifiable, Hashable {
    var artist: String?
    var album: String
    var tracks: Int?
    var coverTrackId: Int?
    var artwork: String?
    var game: Bool?
    var explanation: String?

    var id: String { albumKey(artist, album) }
}

func albumKey(_ artist: String?, _ album: String) -> String {
    (artist ?? "").lowercased() + "\u{1F}" + album.lowercased()
}

struct ArtistsResponse: Decodable { var artists: [ArtistItem]? }
struct AlbumsResponse: Decodable { var albums: [AlbumItem]? }

struct FavoriteTrack: Decodable, Hashable {
    var trackId: Int
    var artist: String?
    var title: String?
    var duration: Double?
    var track: Track?

    var asTrack: Track {
        track ?? Track(id: trackId, artist: artist, title: title, duration: duration)
    }
}

struct FavoritesResponse: Decodable {
    var tracks: [FavoriteTrack]?
    var artists: [ArtistItem]?
    var albums: [AlbumItem]?
}

struct RecommendResponse: Decodable {
    var tracks: [Track]?
    var artists: [ArtistItem]?
    var albums: [AlbumItem]?
    var empty: Bool?
    var hint: String?
}

struct DiscoverTip: Decodable, Identifiable, Hashable {
    var id: Int
    var kind: String?
    var artist: String?
    var album: String?
    var explanation: String?
    var trackIds: [Int]?
    var tracks: [Track?]?

    var coverTrackId: Int? { trackIds?.first }
}

struct DiscoverResponse: Decodable { var tips: [DiscoverTip]? }

struct FavoriteToggleResult: Decodable {
    var favorited: Bool
    var type: String?
}

struct Health: Decodable {
    var ok: Bool?
    var version: String?
    var apiVersion: String?
    var tracks: Int?
}

struct AuthMe: Decodable {
    var ok: Bool
    var authEnabled: Bool?
}

struct ArtistCount: Decodable, Hashable {
    var artist: String
    var count: Int
}

struct Profile: Decodable {
    var maturity: String?
    var nPositive: Int?
    var nNegative: Int?
    var readyAt: Int?
    var formingAt: Int?
    var confidence: Double?
    var exploreRatio: Double?
    var topArtists: [ArtistCount]?
}

struct ShareLink: Decodable, Identifiable, Hashable {
    var token: String
    var name: String?
    var createdAt: String?
    var lastListenAt: String?
    var listenCount: Int?
    var active: Bool?
    var url: String

    var id: String { token }
}

struct SharesResponse: Decodable { var shares: [ShareLink]? }
struct ShareCreated: Decodable { var token: String?; var url: String? }

struct JobRef: Decodable {
    var id: Int?
    var jobId: Int?
}

struct JobStatus: Decodable {
    var id: Int?
    var status: String?
    var error: String?
}

struct Lyrics: Decodable {
    var status: String?
    var plainLyrics: String?
    var syncedLyrics: String?
}

struct OK: Decodable { var ok: Bool? }

// MARK: - Labels shared with the web UI

enum Labels {
    static func maturity(_ m: String?) -> String {
        switch m {
        case "discovering": return "изучаем вкус · слушай и скипай"
        case "forming": return "вкус формируется"
        case "ready": return "вкус готов"
        default: return "твой локальный микс"
        }
    }

    static func source(_ s: String?) -> String? {
        guard let s, !s.isEmpty else { return nil }
        let map = [
            "exploit": "похоже на тебя",
            "transition": "часто после этой",
            "explore_adjacent": "чуть в сторону",
            "resurface": "давно не звучало",
            "new_in_library": "новое в библиотеке",
            "wildcard": "для разнообразия",
            "explore": "чуть в сторону",
            "radio_start": "старт радио",
            "refill": "добор очереди",
            "manual": "ты выбрал",
            "aggregate": "общая доля",
        ]
        return map[s] ?? s
    }

    static func why(_ item: QueueItem) -> String? {
        guard let reason = source(item.source) else { return nil }
        if item.newBoost == true && item.source != "new_in_library" { return reason + " · свежее" }
        return reason
    }
}

func formatTime(_ seconds: Double?) -> String {
    guard let s = seconds, s.isFinite, s > 0 else { return "0:00" }
    let total = Int(s.rounded(.down))
    return String(format: "%d:%02d", total / 60, total % 60)
}

extension Optional where Wrapped == String {
    var nonEmpty: String? {
        guard let s = self?.trimmingCharacters(in: .whitespaces), !s.isEmpty else { return nil }
        return s
    }
}
