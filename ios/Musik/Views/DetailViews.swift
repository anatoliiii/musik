import SwiftUI

struct ArtistDetailView: View {
    @EnvironmentObject var app: AppState
    @EnvironmentObject var player: PlayerController
    let artist: String

    @State private var tracks: [Track] = []
    @State private var similar: [ArtistItem] = []
    @State private var loaded = false

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 22) {
                DetailHeader(
                    coverTrackId: tracks.first?.id,
                    title: artist,
                    subtitle: "\(tracks.count) треков",
                    round: true,
                    isFavorite: app.favoriteArtists.contains(artist),
                    onPlay: { player.play(["artist": artist]) },
                    onRadio: radioAction,
                    onFavorite: { Task { await app.toggleFavoriteArtist(artist) } }
                )

                let albums = albumsOfArtist
                if albums.count > 1 {
                    Shelf(title: "Альбомы") {
                        ForEach(albums, id: \.self) { album in
                            AlbumCard(album: AlbumItem(artist: artist, album: album,
                                                       tracks: tracks.filter { $0.album == album }.count,
                                                       coverTrackId: tracks.first { $0.album == album }?.id))
                        }
                    }
                }

                TrackList(tracks: tracks, loaded: loaded) { t in
                    player.play(["artist": artist, "start_track_id": t.id])
                }

                if !similar.isEmpty {
                    Shelf(title: "Похожие артисты") { ForEach(similar) { ArtistCard(artist: $0) } }
                }
            }
            .padding(.bottom, 24)
        }
        .background(Theme.backdrop)
        .navigationTitle(artist)
        .navigationBarTitleDisplayMode(.inline)
        .task {
            guard !loaded else { return }
            let api = app.api
            async let t = try? api.library(artist: artist)
            async let s = try? api.similarArtists(artist)
            tracks = await t ?? []
            similar = await s ?? []
            loaded = true
        }
    }

    private var radioAction: (() -> Void)? {
        guard let first = tracks.first else { return nil }
        return { player.startRadio(seed: first.id) }
    }

    private var albumsOfArtist: [String] {
        var seen = Set<String>()
        return tracks.compactMap { $0.album.nonEmpty }.filter { seen.insert($0).inserted }
    }
}

struct AlbumDetailView: View {
    @EnvironmentObject var app: AppState
    @EnvironmentObject var player: PlayerController
    let artist: String?
    let album: String

    @State private var tracks: [Track] = []
    @State private var similar: [AlbumItem] = []
    @State private var loaded = false

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 22) {
                DetailHeader(
                    coverTrackId: tracks.first?.id,
                    title: album,
                    subtitle: [artist ?? tracks.first?.artist ?? "", "\(tracks.count) треков"]
                        .filter { !$0.isEmpty }.joined(separator: " · "),
                    round: false,
                    isFavorite: app.favoriteAlbums.contains(albumKey(artist, album)),
                    onPlay: { player.play(["artist": artist ?? "", "album": album]) },
                    onRadio: radioAction,
                    onFavorite: { Task { await app.toggleFavoriteAlbum(artist: artist, album: album) } }
                )

                if let name = artist.nonEmpty ?? tracks.first?.artist.nonEmpty {
                    NavigationLink(value: Route.artist(name)) {
                        Label(name, systemImage: "music.mic")
                    }
                    .buttonStyle(ChipButtonStyle())
                    .padding(.horizontal, 16)
                }

                TrackList(tracks: tracks, loaded: loaded, numbered: true) { t in
                    player.play(["artist": artist ?? "", "album": album, "start_track_id": t.id])
                }

                if !similar.isEmpty {
                    Shelf(title: "Похожие альбомы") { ForEach(similar) { AlbumCard(album: $0) } }
                }
            }
            .padding(.bottom, 24)
        }
        .background(Theme.backdrop)
        .navigationTitle(album)
        .navigationBarTitleDisplayMode(.inline)
        .task {
            guard !loaded else { return }
            let api = app.api
            async let t = try? api.library(artist: artist, album: album)
            async let s = try? api.similarAlbums(artist: artist, album: album)
            tracks = await t ?? []
            similar = await s ?? []
            loaded = true
        }
    }

    private var radioAction: (() -> Void)? {
        guard let first = tracks.first else { return nil }
        return { player.startRadio(seed: first.id) }
    }
}

private struct DetailHeader: View {
    let coverTrackId: Int?
    let title: String
    let subtitle: String
    let round: Bool
    let isFavorite: Bool
    let onPlay: () -> Void
    let onRadio: (() -> Void)?
    let onFavorite: () -> Void

    var body: some View {
        VStack(spacing: 14) {
            ArtworkView(trackId: coverTrackId, size: 640, fallback: title, cornerRadius: round ? 110 : 18)
                .frame(width: 220, height: 220)
                .shadow(color: .black.opacity(0.5), radius: 20, y: 10)
            VStack(spacing: 4) {
                Text(title).font(.title2.weight(.bold)).multilineTextAlignment(.center)
                Text(subtitle).font(.subheadline).foregroundStyle(Theme.muted)
            }
            HStack(spacing: 10) {
                Button(action: onPlay) { Label("Играть", systemImage: "play.fill") }
                    .buttonStyle(PrimaryButtonStyle())
                if let onRadio {
                    Button(action: onRadio) { Label("Радио", systemImage: "dot.radiowaves.left.and.right") }
                        .buttonStyle(ChipButtonStyle())
                }
                Button(action: onFavorite) {
                    Image(systemName: isFavorite ? "heart.fill" : "heart")
                }
                .buttonStyle(ChipButtonStyle(active: isFavorite))
            }
        }
        .frame(maxWidth: .infinity)
        .padding(.horizontal, 16)
        .padding(.top, 12)
    }
}

private struct TrackList: View {
    let tracks: [Track]
    let loaded: Bool
    var numbered = false
    let onTap: (Track) -> Void

    var body: some View {
        LazyVStack(spacing: 14) {
            if !loaded {
                ProgressView()
            } else if tracks.isEmpty {
                EmptyHint(text: "Треков не найдено")
            }
            ForEach(Array(tracks.enumerated()), id: \.element.id) { i, t in
                TrackRow(track: t, index: i, showArtwork: !numbered) { onTap(t) }
            }
        }
        .padding(.horizontal, 16)
    }
}
