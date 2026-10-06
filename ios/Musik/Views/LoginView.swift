import SwiftUI

struct LoginView: View {
    @EnvironmentObject var app: AppState
    @State private var baseURL = ""
    @State private var mode: AppState.LoginMode = .token
    @State private var secret = ""
    @State private var busy = false
    @State private var error: String?

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 22) {
                VStack(alignment: .leading, spacing: 6) {
                    Text("musik").font(.display(56))
                        .foregroundStyle(LinearGradient(colors: [Theme.accentBright, Theme.accent],
                                                        startPoint: .leading, endPoint: .trailing))
                    Text("Твой сервер, твой вкус. Подключись к Go player.")
                        .foregroundStyle(Theme.muted)
                }
                .padding(.top, 48)

                field("Адрес сервера") {
                    TextField("http://192.168.1.10:8787", text: $baseURL)
                        .keyboardType(.URL)
                        .textContentType(.URL)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                }

                Picker("Вход", selection: $mode) {
                    ForEach(AppState.LoginMode.allCases) { Text($0.rawValue).tag($0) }
                }
                .pickerStyle(.segmented)

                field(mode == .token ? "MUSIK_API_TOKEN" : "MUSIK_PASSWORD") {
                    SecureField(mode == .token ? "токен из .env сервера" : "пароль", text: $secret)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                }

                if let error {
                    Text(error).font(.footnote).foregroundStyle(.red)
                }

                Button {
                    Task { await submit() }
                } label: {
                    HStack {
                        if busy { ProgressView().tint(.black) }
                        Text("Подключиться")
                    }
                    .frame(maxWidth: .infinity)
                }
                .buttonStyle(PrimaryButtonStyle())
                .disabled(busy || baseURL.trimmingCharacters(in: .whitespaces).isEmpty)

                Text("Для LAN запусти player с MUSIK_PLAYER_ADDR=0.0.0.0:8787. Токен рекомендуется: он хранится в Keychain и работает и для потока, и для обложек.")
                    .font(.footnote)
                    .foregroundStyle(Theme.muted)
            }
            .padding(24)
        }
        .scrollDismissesKeyboard(.interactively)
        .onAppear { if baseURL.isEmpty { baseURL = app.settings.baseURL } }
    }

    private func field<Content: View>(_ title: String, @ViewBuilder _ content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(title).font(.caption.weight(.semibold)).foregroundStyle(Theme.muted)
            content()
                .padding(14)
                .background(Theme.surface, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                .overlay(RoundedRectangle(cornerRadius: 12, style: .continuous).stroke(Color.white.opacity(0.08)))
        }
    }

    private func submit() async {
        busy = true
        error = nil
        defer { busy = false }
        do {
            try await app.login(baseURL: baseURL, mode: mode, secret: secret)
            secret = ""
        } catch is CancellationError {
        } catch {
            self.error = error.localizedDescription
        }
    }
}
