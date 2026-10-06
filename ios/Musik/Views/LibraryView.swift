import SwiftUI

struct LibraryView: View {
    enum Segment: String, CaseIterable, Identifiable {
        case tracks = "Треки"
        case artists = "Артисты"
        case albums = "Альбомы"
        case favorites = "Любимое"
        var id: String { rawValue }
    }

    @EnvironmentObject var app: AppState
    @EnvironmentObject var player: PlayerController

    @State private var segment: Segment = .tracks
    @State private var query = ""
    @State private var tracks: [Track] = []
    @State private var artists: [ArtistItem] = []
    @State private var albums: [AlbumItem] = []
    @State private var favorites = FavoritesResponse()
    @State private var loaded = false

    var body: some View {
        List {
            Picker("Раздел", selection: $segment) {
                ForEach(Segment.allCases) { Text($0.rawValue).tag($0) }
            }
            .pickerStyle(.segmented)
            .listRowBackground(Color.clear)
            .listRowSeparator(.hidden)

            if !loaded {
                ProgressView().frame(maxWidth: .infinity).listRowBackground(Color.clear)
            }

            switch segment {
            case .tracks: tracksSection
            case .artists: artistsSection
            case .albums: albumsSection
            case .favorites: favoritesSection
            }
        }
        .listStyle(.plain)
        .scrollContentBackground(.hidden)
        .background(Theme.backdrop)
        .navigationTitle("Библиотека")
        .searchable(text: $query, prompt: "Поиск")
        .refreshable { await load() }
        .task { if !loaded { await load() } }
        .onChange(of: app.homeRevision) { _ in Task { await loadFavorites() } }
    }

    // MARK: Sections

    @ViewBuilder
    private var tracksSection: some View {
        let list = filteredTracks
        ForEach(Array(list.enumerated()), id: \.element.id) { i, t in
            TrackRow(track: t) { playFromLibrary(list, at: i) }
                .listRowBackground(Color.clear)
        }
        if loaded && list.isEmpty {
            EmptyHint(text: query.isEmpty ? "Треков нет" : "Ничего не найдено").listRowBackground(Color.clear)
        }
    }

    @ViewBuilder
    private var artistsSection: some View {
        ForEach(artists.filter { matches($0.artist) }) { a in
            NavigationLink(value: Route.artist(a.artist)) {
                HStack(spacing: 12) {
                    ArtworkView(trackId: a.coverTrackId, size: 96, fallback: a.artist, cornerRadius: 22)
                        .frame(width: 44, height: 44)
                    VStack(alignment: .leading) {
                        Text(a.artist).lineLimit(1)
                        Text("\(a.tracks ?? 0) треков").font(.caption).foregroundStyle(Theme.muted)
                    }
                    Spacer()
                    if app.favoriteArtists.contains(a.artist) {
                        Image(systemName: "heart.fill").font(.caption).foregroundStyle(Theme.accent)
                    }
                }
            }
            .listRowBackground(Color.clear)
        }
    }

    @ViewBuilder
    private var albumsSection: some View {
        ForEach(albums.filter { matches($0.album) || matches($0.artist ?? "") }) { a in
            NavigationLink(value: Route.album(artist: a.artist, album: a.album)) {
                HStack(spacing: 12) {
                    ArtworkView(trackId: a.coverTrackId, size: 96, fallback: a.album, cornerRadius: 6)
                        .frame(width: 44, height: 44)
                    VStack(alignment: .leading) {
                        Text(a.album).lineLimit(1)
                        Text([a.artist ?? "", "\(a.tracks ?? 0) треков", a.game == true ? "OST" : ""]
                            .filter { !$0.isEmpty }.joined(separator: " · "))
                            .font(.caption).foregroundStyle(Theme.muted).lineLimit(1)
                    }
                    Spacer()
                    if app.isFavorite(album: a) {
                        Image(systemName: "heart.fill").font(.caption).foregroundStyle(Theme.accent)
                    }
                }
            }
            .listRowBackground(Color.clear)
        }
    }

    @ViewBuilder
    private var favoritesSection: some View {
        let favTracks = (favorites.tracks ?? []).map(\.asTrack).filter { matchesTrack($0) }
        Section("Песни") {
            ForEach(favTracks) { t in
                TrackRow(track: t) { player.playMix("favorites", startTrackId: t.id) }
            }
            if favTracks.isEmpty { EmptyHint(text: "Жми ♥ на треке") }
        }
        .listRowBackground(Color.clear)
        Section("Артисты") {
            ForEach((favorites.artists ?? []).filter { matches($0.artist) }) { a in
                NavigationLink(a.artist, value: Route.artist(a.artist))
            }
        }
        .listRowBackground(Color.clear)
        Section("Альбомы") {
            ForEach((favorites.albums ?? []).filter { matches($0.album) }) { a in
                NavigationLink(value: Route.album(artist: a.artist, album: a.album)) {
                    VStack(alignment: .leading) {
                        Text(a.album)
                        Text(a.artist ?? "").font(.caption).foregroundStyle(Theme.muted)
                    }
                }
            }
        }
        .listRowBackground(Color.clear)
    }

    // MARK: Helpers

    private var filteredTracks: [Track] { tracks.filter { matchesTrack($0) } }

    private func matches(_ s: String) -> Bool {
        let q = query.trimmingCharacters(in: .whitespaces)
        return q.isEmpty || s.localizedCaseInsensitiveContains(q)
    }

    private func matchesTrack(_ t: Track) -> Bool {
        matches(t.title ?? "") || matches(t.artist ?? "") || matches(t.album ?? "")
    }

    /// Plays the visible list from the tapped row, so playback continues through it.
    private func playFromLibrary(_ list: [Track], at index: Int) {
        let window = list[index..<min(list.count, index + 500)]
        player.play([
            "track_ids": window.map(\.id),
            "name": query.isEmpty ? "Библиотека" : "Поиск: \(query)",
        ])
    }

    private func load() async {
        let api = app.api
        async let t = try? api.library()
        async let ar = try? api.artists()
        async let al = try? api.albums()
        async let f = try? api.favorites()
        tracks = await t ?? tracks
        artists = await ar ?? artists
        albums = await al ?? albums
        favorites = await f ?? favorites
        loaded = true
    }

    private func loadFavorites() async {
        if let f = try? await app.api.favorites() { favorites = f }
    }
}
