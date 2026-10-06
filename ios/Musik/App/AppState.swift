import Foundation
import SwiftUI

/// App-wide state: login gate, toasts and favorite sets shared by every screen.
@MainActor
final class AppState: ObservableObject {
    enum Phase { case checking, login, ready }

    @Published var phase: Phase = .checking
    @Published var toast: String?
    @Published var favoriteTracks: Set<Int> = []
    @Published var favoriteArtists: Set<String> = []
    @Published var favoriteAlbums: Set<String> = []
    /// Bumped when shelves should reload (favorites or mixes changed).
    @Published var homeRevision = 0

    let settings: Settings
    let api: APIClient
    let player: PlayerController

    private var toastTask: Task<Void, Never>?

    init() {
        let settings = Settings.shared
        let api = APIClient(settings: settings)
        self.settings = settings
        self.api = api
        self.player = PlayerController(api: api, settings: settings)
        ImageLoader.shared.api = api
        player.app = self
        api.onUnauthorized = { [weak self] in self?.handleUnauthorized() }
    }

    // MARK: Boot & auth

    func bootstrap() async {
        guard !settings.baseURL.isEmpty else {
            phase = .login
            return
        }
        do {
            let me = try await api.authMe()
            if me.ok || me.authEnabled == false {
                phase = .ready
                await refreshFavorites()
                await player.restore()
            } else {
                phase = .login
            }
        } catch {
            // Server unreachable: still show the login screen with the saved URL.
            phase = .login
            show(error)
        }
    }

    enum LoginMode: String, CaseIterable, Identifiable {
        case token = "API token"
        case password = "Пароль"
        var id: String { rawValue }
    }

    func login(baseURL: String, mode: LoginMode, secret: String) async throws {
        settings.baseURL = baseURL
        let secret = secret.trimmingCharacters(in: .whitespacesAndNewlines)
        switch mode {
        case .token:
            settings.token = secret
        case .password:
            settings.token = nil
            try await api.login(password: secret)
        }
        _ = try await api.health()
        let me = try await api.authMe()
        guard me.ok || me.authEnabled == false else {
            settings.token = nil
            throw APIError(status: 401, code: "auth_required",
                           message: mode == .token ? "Сервер не принял токен" : "Не удалось войти")
        }
        phase = .ready
        await refreshFavorites()
    }

    func logout() async {
        player.stop(clearSession: true)
        await api.logout()
        settings.token = nil
        HTTPCookieStorage.shared.cookies?.forEach { HTTPCookieStorage.shared.deleteCookie($0) }
        ImageLoader.shared.clear()
        favoriteTracks = []
        favoriteArtists = []
        favoriteAlbums = []
        phase = .login
    }

    private func handleUnauthorized() {
        guard phase == .ready else { return }
        player.stop(clearSession: false)
        phase = .login
        show("Сессия истекла — войди снова")
    }

    // MARK: Favorites

    func refreshFavorites() async {
        guard let fav = try? await api.favorites() else { return }
        favoriteTracks = Set((fav.tracks ?? []).map(\.trackId))
        favoriteArtists = Set((fav.artists ?? []).map(\.artist))
        favoriteAlbums = Set((fav.albums ?? []).map(\.id))
    }

    func isFavorite(album: AlbumItem) -> Bool { favoriteAlbums.contains(album.id) }

    /// Toggles a track heart; returns the new state.
    @discardableResult
    func toggleFavoriteTrack(_ id: Int) async -> Bool? {
        do {
            let r = try await api.toggleFavorite(["type": "track", "track_id": id])
            if r.favorited { favoriteTracks.insert(id) } else { favoriteTracks.remove(id) }
            show(r.favorited ? "Любимая песня" : "Песня убрана из любимых")
            homeRevision += 1
            return r.favorited
        } catch {
            show(error)
            return nil
        }
    }

    func toggleFavoriteArtist(_ artist: String) async {
        do {
            let r = try await api.toggleFavorite(["type": "artist", "artist": artist])
            if r.favorited { favoriteArtists.insert(artist) } else { favoriteArtists.remove(artist) }
            show(r.favorited ? "Любимый артист" : "Артист убран")
            homeRevision += 1
        } catch {
            show(error)
        }
    }

    func toggleFavoriteAlbum(artist: String?, album: String) async {
        do {
            let r = try await api.toggleFavorite(["type": "album", "artist": artist ?? "", "album": album])
            let key = albumKey(artist, album)
            if r.favorited { favoriteAlbums.insert(key) } else { favoriteAlbums.remove(key) }
            show(r.favorited ? "Любимый альбом" : "Альбом убран")
            homeRevision += 1
        } catch {
            show(error)
        }
    }

    func addLater(_ trackId: Int) async {
        do {
            try await api.addLater(trackId: trackId)
            show("В «Потом»")
            homeRevision += 1
        } catch {
            show(error)
        }
    }

    // MARK: Toasts

    func show(_ message: String) {
        toastTask?.cancel()
        withAnimation(.spring(response: 0.3, dampingFraction: 0.85)) { toast = message }
        toastTask = Task { [weak self] in
            try? await Task.sleep(nanoseconds: 2_600_000_000)
            guard !Task.isCancelled else { return }
            withAnimation(.easeOut(duration: 0.3)) { self?.toast = nil }
        }
    }

    func show(_ error: Error) {
        if error is CancellationError { return }
        show(error.localizedDescription)
    }

    /// Runs an async action and turns any error into a toast.
    func perform(_ action: @escaping () async throws -> Void) {
        Task {
            do { try await action() } catch { show(error) }
        }
    }
}
