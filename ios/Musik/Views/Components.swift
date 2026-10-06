import SwiftUI
import UIKit

// MARK: - Navigation

enum Route: Hashable {
    case artist(String)
    case album(artist: String?, album: String)
}

extension View {
    func withRoutes() -> some View {
        navigationDestination(for: Route.self) { route in
            switch route {
            case .artist(let name): ArtistDetailView(artist: name)
            case .album(let artist, let album): AlbumDetailView(artist: artist, album: album)
            }
        }
    }
}

// MARK: - Shelves

struct Shelf<Content: View>: View {
    let title: String
    var subtitle: String?
    @ViewBuilder var content: () -> Content

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            VStack(alignment: .leading, spacing: 2) {
                Text(title).font(.title3.weight(.bold))
                if let subtitle {
                    Text(subtitle).font(.footnote).foregroundStyle(Theme.muted)
                }
            }
            .padding(.horizontal, 16)
            ScrollView(.horizontal, showsIndicators: false) {
                LazyHStack(alignment: .top, spacing: 14) {
                    content()
                }
                .padding(.horizontal, 16)
            }
        }
    }
}

struct MixCard: View {
    let mix: Mix
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            VStack(alignment: .leading, spacing: 6) {
                ZStack(alignment: .bottomLeading) {
                    if let cover = mix.coverTrackId {
                        ArtworkView(trackId: cover, size: 256, fallback: mix.title, cornerRadius: 14)
                    } else {
                        RoundedRectangle(cornerRadius: 14, style: .continuous)
                            .fill(LinearGradient(colors: Theme.placeholder(for: mix.kind.count),
                                                 startPoint: .topLeading, endPoint: .bottomTrailing))
                            .aspectRatio(1, contentMode: .fit)
                    }
                    if mix.today == true {
                        Text("сегодня")
                            .font(.caption2.weight(.bold))
                            .padding(.horizontal, 8).padding(.vertical, 4)
                            .background(Theme.accent, in: Capsule())
                            .foregroundStyle(.black)
                            .padding(8)
                    }
                }
                .frame(width: 150, height: 150)
                .overlay(
                    RoundedRectangle(cornerRadius: 14, style: .continuous)
                        .stroke(mix.highlight == true ? Theme.accent.opacity(0.7) : .clear, lineWidth: 2)
                )
                Text(mix.title).font(.subheadline.weight(.semibold)).lineLimit(1)
                Text(mix.metaLabel).font(.caption).foregroundStyle(Theme.muted).lineLimit(1)
            }
            .frame(width: 150, alignment: .leading)
            .opacity(mix.ready == false ? 0.55 : 1)
        }
        .buttonStyle(.plain)
    }
}

struct TrackCard: View {
    let track: Track
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            VStack(alignment: .leading, spacing: 6) {
                ArtworkView(trackId: track.id, size: 256, fallback: track.displayTitle, cornerRadius: 12)
                    .frame(width: 132, height: 132)
                Text(track.displayTitle).font(.subheadline.weight(.semibold)).lineLimit(1)
                Text(track.displayArtist).font(.caption).foregroundStyle(Theme.muted).lineLimit(1)
            }
            .frame(width: 132, alignment: .leading)
        }
        .buttonStyle(.plain)
    }
}

struct ArtistCard: View {
    let artist: ArtistItem

    var body: some View {
        NavigationLink(value: Route.artist(artist.artist)) {
            VStack(spacing: 6) {
                ArtworkView(trackId: artist.coverTrackId, size: 256, fallback: artist.artist, cornerRadius: 60)
                    .frame(width: 112, height: 112)
                Text(artist.artist).font(.subheadline.weight(.semibold)).lineLimit(1)
                if let n = artist.tracks {
                    Text("\(n) треков").font(.caption).foregroundStyle(Theme.muted)
                }
            }
            .frame(width: 116)
        }
        .buttonStyle(.plain)
    }
}

struct AlbumCard: View {
    let album: AlbumItem

    var body: some View {
        NavigationLink(value: Route.album(artist: album.artist, album: album.album)) {
            VStack(alignment: .leading, spacing: 6) {
                ArtworkView(trackId: album.coverTrackId, size: 256, fallback: album.album, cornerRadius: 12)
                    .frame(width: 140, height: 140)
                Text(album.album).font(.subheadline.weight(.semibold)).lineLimit(1)
                Text(album.artist ?? "").font(.caption).foregroundStyle(Theme.muted).lineLimit(1)
            }
            .frame(width: 140, alignment: .leading)
        }
        .buttonStyle(.plain)
    }
}

// MARK: - Rows

struct TrackRow: View {
    @EnvironmentObject var app: AppState
    @EnvironmentObject var player: PlayerController
    let track: Track
    var index: Int?
    var showArtwork = true
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            HStack(spacing: 12) {
                if showArtwork {
                    ArtworkView(trackId: track.id, size: 96, fallback: track.displayTitle, cornerRadius: 6)
                        .frame(width: 44, height: 44)
                } else if let index {
                    Text("\(index + 1)")
                        .font(.footnote.monospacedDigit())
                        .foregroundStyle(Theme.muted)
                        .frame(width: 28)
                }
                VStack(alignment: .leading, spacing: 2) {
                    Text(track.displayTitle)
                        .font(.body.weight(isCurrent ? .bold : .regular))
                        .foregroundStyle(isCurrent ? Theme.accentBright : .primary)
                        .lineLimit(1)
                    Text(track.subtitle).font(.caption).foregroundStyle(Theme.muted).lineLimit(1)
                }
                Spacer(minLength: 8)
                if app.favoriteTracks.contains(track.id) {
                    Image(systemName: "heart.fill").font(.caption).foregroundStyle(Theme.accent)
                }
                Text(formatTime(track.duration))
                    .font(.caption.monospacedDigit())
                    .foregroundStyle(Theme.muted)
            }
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .contextMenu { TrackMenu(track: track) }
    }

    private var isCurrent: Bool { player.current?.id == track.id }
}

struct TrackMenu: View {
    @EnvironmentObject var app: AppState
    @EnvironmentObject var player: PlayerController
    let track: Track

    var body: some View {
        Button {
            player.play(["track_id": track.id, "name": track.displayTitle])
        } label: { Label("Играть трек", systemImage: "play.fill") }
        Button {
            player.startRadio(seed: track.id)
        } label: { Label("Радио от трека", systemImage: "dot.radiowaves.left.and.right") }
        Button {
            Task { await app.addLater(track.id) }
        } label: { Label("Потом", systemImage: "clock") }
        Button {
            Task { await app.toggleFavoriteTrack(track.id) }
        } label: {
            app.favoriteTracks.contains(track.id)
                ? Label("Убрать из любимых", systemImage: "heart.slash")
                : Label("В любимые", systemImage: "heart")
        }
        if let artist = track.artist.nonEmpty {
            Button {
                player.play(["artist": artist])
            } label: { Label("Весь артист", systemImage: "music.mic") }
        }
        if let album = track.album.nonEmpty {
            Button {
                player.play(["artist": track.artist ?? "", "album": album])
            } label: { Label("Весь альбом", systemImage: "square.stack") }
        }
    }
}

// MARK: - Misc

struct ToastView: View {
    @EnvironmentObject var app: AppState

    var body: some View {
        if let message = app.toast {
            Text(message)
                .font(.subheadline.weight(.semibold))
                .multilineTextAlignment(.center)
                .padding(.horizontal, 16).padding(.vertical, 10)
                .background(.ultraThinMaterial, in: Capsule())
                .overlay(Capsule().stroke(Theme.accent.opacity(0.35)))
                .padding(.top, 8)
                .padding(.horizontal, 24)
                .transition(.move(edge: .top).combined(with: .opacity))
                .onTapGesture { app.toast = nil }
        }
    }
}

struct EmptyHint: View {
    let text: String
    var body: some View {
        Text(text)
            .font(.footnote)
            .foregroundStyle(Theme.muted)
            .padding(.horizontal, 16)
    }
}

struct ShareItem: Identifiable {
    let url: URL
    var id: String { url.absoluteString }
}

struct ActivityView: UIViewControllerRepresentable {
    let items: [Any]

    func makeUIViewController(context: Context) -> UIActivityViewController {
        UIActivityViewController(activityItems: items, applicationActivities: nil)
    }

    func updateUIViewController(_ controller: UIActivityViewController, context: Context) {}
}
