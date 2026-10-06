import SwiftUI

struct MiniPlayer: View {
    @EnvironmentObject var player: PlayerController
    let open: () -> Void

    var body: some View {
        if let track = player.current {
            VStack(spacing: 0) {
                HStack(spacing: 12) {
                    ArtworkView(trackId: track.id, size: 96, fallback: track.displayTitle, cornerRadius: 8)
                        .frame(width: 44, height: 44)
                    VStack(alignment: .leading, spacing: 2) {
                        Text(track.displayTitle).font(.subheadline.weight(.semibold)).lineLimit(1)
                        Text(track.displayArtist).font(.caption).foregroundStyle(Theme.muted).lineLimit(1)
                    }
                    Spacer(minLength: 4)
                    Button { player.togglePlay() } label: {
                        Image(systemName: player.isPlaying ? "pause.fill" : "play.fill")
                            .font(.title3)
                            .frame(width: 40, height: 40)
                    }
                    Button { player.skip() } label: {
                        Image(systemName: "forward.fill")
                            .font(.title3)
                            .frame(width: 40, height: 40)
                    }
                    .disabled(player.busy)
                }
                .padding(.horizontal, 12)
                .padding(.vertical, 8)
                GeometryReader { geo in
                    Rectangle()
                        .fill(Theme.accent)
                        .frame(width: geo.size.width * progress)
                }
                .frame(height: 2)
            }
            .foregroundStyle(.primary)
            .background(.ultraThinMaterial)
            .background(Theme.surface.opacity(0.6))
            .contentShape(Rectangle())
            .onTapGesture(perform: open)
        }
    }

    private var progress: CGFloat {
        guard player.duration > 0 else { return 0 }
        return CGFloat(min(1, max(0, player.position / player.duration)))
    }
}
