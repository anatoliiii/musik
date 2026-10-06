import SwiftUI
import UIKit

struct HomeView: View {
    @EnvironmentObject var app: AppState
    @EnvironmentObject var player: PlayerController

    @State private var mixes: [Mix] = []
    @State private var favorites = FavoritesResponse()
    @State private var recs = RecommendResponse()
    @State private var newTips: [DiscoverTip] = []
    @State private var oldTips: [DiscoverTip] = []
    @State private var artists: [ArtistItem] = []
    @State private var albums: [AlbumItem] = []
    @State private var tracks: [Track] = []
    @State private var maturity: String?
    @State private var loaded = false
    @State private var refreshingMixes = false
    @State private var shareItem: ShareItem?

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 28) {
                header
                if !loaded {
                    ProgressView().frame(maxWidth: .infinity).padding(.top, 40)
                }
                shelves
            }
            .padding(.bottom, 24)
        }
        .refreshable { await load() }
        .task { if !loaded { await load() } }
        .onChange(of: app.homeRevision) { _ in Task { await loadFavorites() } }
        .navigationBarHidden(true)
        .sheet(item: $shareItem) { item in
            ActivityView(items: [item.url])
                .presentationDetents([.medium, .large])
        }
    }

    // MARK: Header

    private var header: some View {
        VStack(alignment: .leading, spacing: 14) {
            VStack(alignment: .leading, spacing: 4) {
                Text("musik").font(.display(40))
                Text(Labels.maturity(player.maturity ?? maturity))
                    .font(.footnote)
                    .foregroundStyle(Theme.teal)
            }
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 10) {
                    Button {
                        player.startRadio()
                    } label: {
                        Label("Радио", systemImage: "dot.radiowaves.left.and.right")
                    }
                    .buttonStyle(PrimaryButtonStyle())

                    Button {
                        app.perform { try await shareRadio() }
                    } label: {
                        Label("Поделиться", systemImage: "square.and.arrow.up")
                    }
                    .buttonStyle(ChipButtonStyle())

                    Button {
                        Task { await refreshMixes() }
                    } label: {
                        HStack(spacing: 6) {
                            if refreshingMixes { ProgressView().controlSize(.small) }
                            Text(refreshingMixes ? "Собираю…" : "Обновить миксы")
                        }
                    }
                    .buttonStyle(ChipButtonStyle())
                    .disabled(refreshingMixes)
                }
            }
        }
        .padding(.horizontal, 16)
        .padding(.top, 12)
    }

    // MARK: Shelves

    // Split into groups to stay well under the ViewBuilder child limit.
    @ViewBuilder
    private var shelves: some View {
        mixShelves
        favoriteShelves
        discoveryShelves
        catalogShelves
    }

    @ViewBuilder
    private var mixShelves: some View {
        let main = mixes.filter { !$0.isWeekday }
        let week = mixes.filter(\.isWeekday)
        if !main.isEmpty {
            Shelf(title: "Для тебя") {
                ForEach(main) { mix in MixCard(mix: mix) { playMix(mix) } }
            }
        }
        if !week.isEmpty {
            Shelf(title: "Дни недели") {
                ForEach(week) { mix in MixCard(mix: mix) { playMix(mix) } }
            }
        }
    }

    @ViewBuilder
    private var favoriteShelves: some View {
        let favTracks = (favorites.tracks ?? []).map(\.asTrack)
        if !favTracks.isEmpty {
            Shelf(title: "Любимые песни") {
                ForEach(favTracks) { t in
                    TrackCard(track: t) { player.playMix("favorites", startTrackId: t.id) }
                        .contextMenu { TrackMenu(track: t) }
                }
            }
        }
        if let favArtists = favorites.artists, !favArtists.isEmpty {
            Shelf(title: "Любимые артисты") { ForEach(favArtists) { ArtistCard(artist: $0) } }
        }
        if let favAlbums = favorites.albums, !favAlbums.isEmpty {
            Shelf(title: "Любимые альбомы") { ForEach(favAlbums) { AlbumCard(album: $0) } }
        }
    }

    @ViewBuilder
    private var discoveryShelves: some View {
        if let recTracks = recs.tracks, !recTracks.isEmpty {
            Shelf(title: "Похожее на любимое", subtitle: "по звучанию (CLAP)") {
                ForEach(recTracks) { t in
                    TrackCard(track: t) {
                        player.play(["track_ids": recTracks.map(\.id), "start_track_id": t.id,
                                     "name": "Похожее на любимое"])
                    }
                    .contextMenu { TrackMenu(track: t) }
                }
            }
        } else if loaded, let hint = recs.hint {
            EmptyHint(text: hint)
        }

        if !newTips.isEmpty {
            Shelf(title: "Открытия", subtitle: "новое в библиотеке") {
                ForEach(newTips) { TipCard(tip: $0) }
            }
        }
        if !oldTips.isEmpty {
            Shelf(title: "Давно не звучало") {
                ForEach(oldTips) { TipCard(tip: $0) }
            }
        }
    }

    @ViewBuilder
    private var catalogShelves: some View {
        if !artists.isEmpty {
            Shelf(title: "Артисты") { ForEach(artists) { ArtistCard(artist: $0) } }
        }
        if !albums.isEmpty {
            Shelf(title: "Альбомы") { ForEach(albums) { AlbumCard(album: $0) } }
        }
        if !tracks.isEmpty {
            Shelf(title: "Треки") {
                ForEach(tracks) { t in
                    TrackCard(track: t) { player.play(["track_id": t.id, "name": t.displayTitle]) }
                        .contextMenu { TrackMenu(track: t) }
                }
            }
        }
        if loaded && mixes.isEmpty && tracks.isEmpty {
            EmptyHint(text: "Библиотека пуста или сервер ещё индексирует треки.")
        }
    }

    private func playMix(_ mix: Mix) {
        guard mix.ready != false else {
            app.show(mix.kind == "later" || mix.kind == "favorites" ? mix.metaLabel : "Микс не собран — нажми «Обновить миксы»")
            return
        }
        player.playMix(mix.kind)
    }

    // MARK: Data

    private func load() async {
        let api = app.api
        async let m = try? api.mixes()
        async let r = try? api.recommendFavorites()
        async let n = try? api.discover(resurfaced: false)
        async let o = try? api.discover(resurfaced: true)
        async let ar = try? api.artists(limit: 28)
        async let al = try? api.albums(limit: 28)
        async let tr = try? api.library(limit: 36)
        async let p = try? api.profile()
        async let f = try? api.favorites()

        mixes = await m ?? mixes
        recs = await r ?? recs
        newTips = await n ?? newTips
        oldTips = await o ?? oldTips
        artists = await ar ?? artists
        albums = await al ?? albums
        tracks = await tr ?? tracks
        maturity = (await p)?.maturity ?? maturity
        if let fav = await f { applyFavorites(fav) }
        loaded = true
    }

    private func loadFavorites() async {
        let api = app.api
        async let f = try? api.favorites()
        async let m = try? api.mixes()
        async let r = try? api.recommendFavorites()
        if let fav = await f { applyFavorites(fav) }
        mixes = await m ?? mixes
        recs = await r ?? recs
    }

    private func applyFavorites(_ fav: FavoritesResponse) {
        favorites = fav
        app.favoriteTracks = Set((fav.tracks ?? []).map(\.trackId))
        app.favoriteArtists = Set((fav.artists ?? []).map(\.artist))
        app.favoriteAlbums = Set((fav.albums ?? []).map(\.id))
    }

    private func shareRadio() async throws {
        let created = try await app.api.createShare()
        guard let raw = created.url, let url = URL(string: raw) else {
            throw APIError(status: 0, code: "share", message: "Сервер не вернул ссылку")
        }
        UIPasteboard.general.string = raw
        app.show("Ссылка на эфир скопирована")
        shareItem = ShareItem(url: url)
    }

    private func refreshMixes() async {
        refreshingMixes = true
        defer { refreshingMixes = false }
        do {
            guard let id = try await app.api.enqueueJob("mix_pack") else {
                app.show("Обновление поставлено в очередь")
                return
            }
            app.show("Собираю миксы…")
            for _ in 0..<300 {
                try await Task.sleep(nanoseconds: 2_000_000_000)
                let job = try await app.api.job(id)
                switch job.status {
                case "done":
                    app.show("Миксы обновлены")
                    await load()
                    return
                case "failed", "error", "cancelled":
                    app.show(job.error.nonEmpty ?? "Не удалось обновить миксы")
                    return
                default:
                    continue
                }
            }
        } catch {
            app.show(error)
        }
    }
}

struct TipCard: View {
    @EnvironmentObject var player: PlayerController
    let tip: DiscoverTip

    var body: some View {
        Button {
            if let ids = tip.trackIds, !ids.isEmpty {
                player.play(["track_ids": ids, "name": tip.album ?? tip.artist ?? "Открытие"])
            } else {
                player.play(["artist": tip.artist ?? "", "album": tip.album ?? ""])
            }
        } label: {
            VStack(alignment: .leading, spacing: 6) {
                ArtworkView(trackId: tip.coverTrackId, size: 256, fallback: tip.album ?? "♪", cornerRadius: 12)
                    .frame(width: 160, height: 160)
                Text(tip.album ?? "—").font(.subheadline.weight(.semibold)).lineLimit(1)
                Text(tip.artist ?? "").font(.caption).foregroundStyle(Theme.muted).lineLimit(1)
                if let why = tip.explanation.nonEmpty {
                    Text(why).font(.caption2).foregroundStyle(Theme.teal).lineLimit(2)
                }
            }
            .frame(width: 160, alignment: .leading)
        }
        .buttonStyle(.plain)
    }
}
