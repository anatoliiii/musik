import Foundation
import Security
import UIKit

/// Persistent client settings: server URL and session id in UserDefaults,
/// the API token in the Keychain.
final class Settings: @unchecked Sendable {
    static let shared = Settings()

    private let defaults = UserDefaults.standard

    var baseURL: String {
        get { defaults.string(forKey: "baseURL") ?? "" }
        set { defaults.set(Settings.normalize(newValue), forKey: "baseURL") }
    }

    var token: String? {
        get { Keychain.get("apiToken") }
        set { Keychain.set("apiToken", newValue) }
    }

    /// Last playback session, used to resume after the app was killed.
    var sessionId: String? {
        get { defaults.string(forKey: "sessionId") }
        set { defaults.set(newValue, forKey: "sessionId") }
    }

    /// Ask the server for its mobile AAC/MP3 profile instead of the original file.
    var mobileStream: Bool {
        get { defaults.object(forKey: "mobileStream") as? Bool ?? true }
        set { defaults.set(newValue, forKey: "mobileStream") }
    }

    /// Stable per-install id sent with playback events.
    var clientId: String {
        if let id = defaults.string(forKey: "clientId") { return id }
        let id = "ios-" + UUID().uuidString.lowercased()
        defaults.set(id, forKey: "clientId")
        return id
    }

    @MainActor var deviceId: String {
        let d = UIDevice.current
        return "\(d.model) \(d.systemName) \(d.systemVersion)"
    }

    static func normalize(_ raw: String) -> String {
        var s = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        while s.hasSuffix("/") { s.removeLast() }
        if !s.isEmpty && !s.contains("://") { s = "http://" + s }
        return s
    }
}

enum Keychain {
    private static let service = "app.musik.ios"

    static func get(_ key: String) -> String? {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: key,
            kSecReturnData as String: true,
            kSecMatchLimit as String: kSecMatchLimitOne,
        ]
        var out: AnyObject?
        if SecItemCopyMatching(query as CFDictionary, &out) == errSecSuccess,
           let data = out as? Data {
            return String(data: data, encoding: .utf8)
        }
        // Fallback for sideloaded builds whose signature lacks a keychain group.
        return UserDefaults.standard.string(forKey: "kc." + key)
    }

    static func set(_ key: String, _ value: String?) {
        let base: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: key,
        ]
        SecItemDelete(base as CFDictionary)
        UserDefaults.standard.removeObject(forKey: "kc." + key)
        guard let value, !value.isEmpty, let data = value.data(using: .utf8) else { return }
        var add = base
        add[kSecValueData as String] = data
        add[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlock
        if SecItemAdd(add as CFDictionary, nil) != errSecSuccess {
            UserDefaults.standard.set(value, forKey: "kc." + key)
        }
    }
}
