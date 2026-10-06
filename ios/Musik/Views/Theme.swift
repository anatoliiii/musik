import SwiftUI

/// Warm dark palette of the web UI (style.css): bg #100c0a, accent #e07a3a, teal #3d8f7a.
enum Theme {
    static let background = Color(hex: 0x100C0A)
    static let surface = Color(hex: 0x1C1512)
    static let surfaceHigh = Color(hex: 0x271D18)
    static let accent = Color(hex: 0xE07A3A)
    static let accentBright = Color(hex: 0xF29A55)
    static let teal = Color(hex: 0x3D8F7A)
    static let muted = Color(hex: 0xA89C94)

    static var backdrop: some View {
        ZStack {
            background
            RadialGradient(colors: [accent.opacity(0.18), .clear], center: .topTrailing,
                           startRadius: 10, endRadius: 420)
            RadialGradient(colors: [teal.opacity(0.14), .clear], center: .bottomLeading,
                           startRadius: 10, endRadius: 480)
        }
        .ignoresSafeArea()
    }

    static func placeholder(for seed: Int) -> [Color] {
        let palettes: [[Color]] = [
            [Color(hex: 0x6B3A1F), Color(hex: 0x2A1810)],
            [Color(hex: 0x2F5C50), Color(hex: 0x14221E)],
            [Color(hex: 0x7A4A2A), Color(hex: 0x3D2215)],
            [Color(hex: 0x3B3050), Color(hex: 0x1A1524)],
        ]
        return palettes[abs(seed) % palettes.count]
    }
}

extension Color {
    init(hex: UInt32) {
        self.init(.sRGB,
                  red: Double((hex >> 16) & 0xFF) / 255,
                  green: Double((hex >> 8) & 0xFF) / 255,
                  blue: Double(hex & 0xFF) / 255,
                  opacity: 1)
    }
}

extension Font {
    static func display(_ size: CGFloat) -> Font { .system(size: size, weight: .heavy, design: .rounded) }
}

struct PrimaryButtonStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.headline)
            .padding(.horizontal, 18)
            .padding(.vertical, 12)
            .background(
                LinearGradient(colors: [Theme.accentBright, Theme.accent], startPoint: .topLeading, endPoint: .bottomTrailing),
                in: Capsule()
            )
            .foregroundStyle(Color.black.opacity(0.85))
            .opacity(configuration.isPressed ? 0.8 : 1)
            .scaleEffect(configuration.isPressed ? 0.97 : 1)
    }
}

struct ChipButtonStyle: ButtonStyle {
    var active = false
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.subheadline.weight(.semibold))
            .padding(.horizontal, 14)
            .padding(.vertical, 10)
            .background(active ? Theme.accent.opacity(0.22) : Theme.surfaceHigh, in: Capsule())
            .overlay(Capsule().stroke(active ? Theme.accent : Color.white.opacity(0.08)))
            .foregroundStyle(active ? Theme.accentBright : .primary)
            .opacity(configuration.isPressed ? 0.75 : 1)
    }
}
