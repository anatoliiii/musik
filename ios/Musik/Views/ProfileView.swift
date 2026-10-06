import SwiftUI
import UIKit

struct ProfileView: View {
    @EnvironmentObject var app: AppState
    @EnvironmentObject var player: PlayerController

    @State private var profile: Profile?
    @State private var health: Health?
    @State private var shares: [ShareLink] = []
    @State private var shareItem: ShareItem?
    @State private var mobileStream = Settings.shared.mobileStream
    @State private var confirmLogout = false

    var body: some View {
        List {
            Section("Вкус") {
                VStack(alignment: .leading, spacing: 8) {
                    Text(Labels.maturity(profile?.maturity ?? player.maturity))
                        .font(.headline)
                    if let p = profile {
                        ProgressView(value: min(1, max(0, p.confidence ?? 0)))
                            .tint(Theme.accent)
                        Text("\(p.nPositive ?? 0) положительных сигналов из \(p.readyAt ?? 8) · \(p.nNegative ?? 0) скипов/дизлайков")
                            .font(.caption)
                            .foregroundStyle(Theme.muted)
                        if let explore = p.exploreRatio {
                            Text("Доля исследования: \(Int((explore * 100).rounded()))%")
                                .font(.caption)
                                .foregroundStyle(Theme.muted)
                        }
                    }
                }
                .padding(.vertical, 4)
                if let top = profile?.topArtists, !top.isEmpty {
                    ForEach(top, id: \.artist) { a in
                        NavigationLink(value: Route.artist(a.artist)) {
                            HStack {
                                Text(a.artist)
                                Spacer()
                                Text("\(a.count)").foregroundStyle(Theme.muted).monospacedDigit()
                            }
                        }
                    }
                }
            }

            Section {
                ForEach(shares) { share in
                    VStack(alignment: .leading, spacing: 6) {
                        Text(share.name ?? "musik radio").font(.subheadline.weight(.semibold))
                        Text(share.url).font(.caption.monospaced()).foregroundStyle(Theme.muted).lineLimit(1)
                        Text("Слушали: \(share.listenCount ?? 0)").font(.caption2).foregroundStyle(Theme.muted)
                    }
                    .swipeActions {
                        Button(role: .destructive) {
                            app.perform { try await revoke(share) }
                        } label: { Label("Отозвать", systemImage: "trash") }
                    }
                    .contextMenu {
                        Button {
                            UIPasteboard.general.string = share.url
                            app.show("Скопировано")
                        } label: { Label("Копировать", systemImage: "doc.on.doc") }
                        if let url = URL(string: share.url) {
                            Button { shareItem = ShareItem(url: url) } label: {
                                Label("Поделиться", systemImage: "square.and.arrow.up")
                            }
                        }
                        Button(role: .destructive) {
                            app.perform { try await revoke(share) }
                        } label: { Label("Отозвать", systemImage: "trash") }
                    }
                }
                Button {
                    app.perform { try await createShare() }
                } label: {
                    Label("Новая ссылка на эфир", systemImage: "plus.circle")
                }
            } header: {
                Text("Share radio")
            } footer: {
                Text("Непрерывный MP3-эфир для VLC или браузера. Слушатели не влияют на твой вкус. Долгое нажатие — копировать, свайп — отозвать.")
            }

            Section("Воспроизведение") {
                Toggle("Мобильный поток (AAC/MP3)", isOn: $mobileStream)
                    .onChange(of: mobileStream) { app.settings.mobileStream = $0 }
                Text("Сервер отдаёт перекодированную версию, если она готова, иначе — оригинал. Выключи, чтобы всегда слушать оригинал (FLAC и т. п.).")
                    .font(.caption)
                    .foregroundStyle(Theme.muted)
            }

            Section("Сервер") {
                LabeledContent("Адрес", value: app.settings.baseURL)
                if let h = health {
                    LabeledContent("Версия", value: [h.version, h.apiVersion].compactMap { $0 }.joined(separator: " · "))
                    if let n = h.tracks { LabeledContent("Треков", value: "\(n)") }
                }
                LabeledContent("Приложение", value: appVersion)
                Button("Выйти", role: .destructive) { confirmLogout = true }
            }
        }
        .scrollContentBackground(.hidden)
        .background(Theme.backdrop)
        .navigationTitle("Профиль")
        .refreshable { await load() }
        .task { await load() }
        .sheet(item: $shareItem) { item in
            ActivityView(items: [item.url]).presentationDetents([.medium, .large])
        }
        .confirmationDialog("Выйти из аккаунта?", isPresented: $confirmLogout, titleVisibility: .visible) {
            Button("Выйти", role: .destructive) { Task { await app.logout() } }
        }
    }

    private var appVersion: String {
        let info = Bundle.main.infoDictionary
        let v = info?["CFBundleShortVersionString"] as? String ?? "?"
        let b = info?["CFBundleVersion"] as? String ?? "?"
        return "\(v) (\(b))"
    }

    private func load() async {
        let api = app.api
        async let p = try? api.profile()
        async let h = try? api.health()
        async let s = try? api.shares()
        profile = await p ?? profile
        health = await h ?? health
        shares = await s ?? shares
    }

    private func createShare() async throws {
        let created = try await app.api.createShare()
        shares = (try? await app.api.shares()) ?? shares
        if let raw = created.url {
            UIPasteboard.general.string = raw
            app.show("Ссылка скопирована")
            if let url = URL(string: raw) { shareItem = ShareItem(url: url) }
        }
    }

    private func revoke(_ share: ShareLink) async throws {
        try await app.api.revokeShare(share.token)
        shares.removeAll { $0.token == share.token }
        app.show("Ссылка отозвана")
    }
}
