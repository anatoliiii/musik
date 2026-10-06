import SwiftUI

@main
struct MusikApp: App {
    @StateObject private var app = AppState()

    var body: some Scene {
        WindowGroup {
            RootView()
                .environmentObject(app)
                .environmentObject(app.player)
                .preferredColorScheme(.dark)
                .tint(Theme.accent)
        }
    }
}

struct RootView: View {
    @EnvironmentObject var app: AppState

    var body: some View {
        ZStack {
            Theme.backdrop
            switch app.phase {
            case .checking:
                VStack(spacing: 16) {
                    Text("musik").font(.display(44))
                    ProgressView()
                }
            case .login:
                LoginView()
            case .ready:
                MainView()
            }
        }
        .overlay(alignment: .top) { ToastView() }
        .task { await app.bootstrap() }
    }
}

struct MainView: View {
    @EnvironmentObject var app: AppState
    @EnvironmentObject var player: PlayerController
    @State private var showPlayer = false

    var body: some View {
        TabView {
            tab { HomeView() }
                .tabItem { Label("Главная", systemImage: "house.fill") }
            tab { LibraryView() }
                .tabItem { Label("Библиотека", systemImage: "music.note.list") }
            tab { ProfileView() }
                .tabItem { Label("Профиль", systemImage: "person.crop.circle") }
        }
        .sheet(isPresented: $showPlayer) {
            NowPlayingView()
                .environmentObject(app)
                .environmentObject(player)
                .presentationDragIndicator(.visible)
        }
    }

    private func tab<Content: View>(@ViewBuilder _ content: () -> Content) -> some View {
        NavigationStack {
            content()
                .withRoutes()
        }
        .safeAreaInset(edge: .bottom, spacing: 0) {
            if player.current != nil {
                MiniPlayer { showPlayer = true }
                    .transition(.move(edge: .bottom).combined(with: .opacity))
            }
        }
        .animation(.easeOut(duration: 0.25), value: player.current?.id)
    }
}
