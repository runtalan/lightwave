import SwiftUI

/// The control HUD: the desktop's pads in the desktop's keypad layout, plus
/// the palette, mode, and dimmer controls. Read-only as far as setup goes.
struct HUDView: View {
    @Environment(LightwaveClient.self) private var client
    @State private var showConnect = false

    private var state: HUDState { client.state }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(spacing: 18) {
                    header
                    StatusBanner()
                    keys
                    grid
                    DimmerSlider(
                        label: "brightness",
                        value: state.brightness,
                        range: 0...100,
                        step: 1,
                        caption: { "\($0)%" },
                        send: { client.sendLatest("SetBrightness", $0) }
                    )
                    if state.warmMode {
                        DimmerSlider(
                            label: "warmth",
                            value: min(6500, max(2000, state.warmness)),
                            range: 2000...6500,
                            step: 100,
                            reversed: true,
                            caption: { "\((6500 - $0) * 100 / 4500)% · \($0)K" },
                            send: { client.sendLatest("SetWarmness", $0) }
                        )
                    }
                }
                .padding(.horizontal, 18)
                .padding(.bottom, 24)
            }
            .scrollBounceBehavior(.basedOnSize)
            .background(backdrop)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button { showConnect = true } label: {
                        Image(systemName: "antenna.radiowaves.left.and.right")
                    }
                    .accessibilityLabel("Connection")
                }
            }
            .toolbarBackground(.hidden, for: .navigationBar)
            .refreshable { try? await client.refresh() }
            .sheet(isPresented: $showConnect) {
                NavigationStack { ConnectView() }
            }
        }
    }

    private var backdrop: some View {
        ZStack {
            Theme.ink
            RadialGradient(colors: [Theme.neon.opacity(0.22), .clear],
                           center: .top, startRadius: 0, endRadius: 520)
        }
        .ignoresSafeArea()
    }

    // MARK: Header

    private var header: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("LIGHTWAVE")
                .font(.caption2.weight(.semibold))
                .tracking(3)
                .foregroundStyle(Theme.ice.opacity(0.6))
            if state.warmMode {
                Button { client.send("ToggleWarmMode") } label: {
                    Text("Warmness").font(.largeTitle.weight(.bold))
                }
                .buttonStyle(.plain)
            } else {
                Menu {
                    Picker("Palette", selection: Binding(
                        get: { state.paletteIndex },
                        set: { client.send("SetPalette", $0) }
                    )) {
                        ForEach(Array(state.paletteNames.enumerated()), id: \.offset) { i, name in
                            Text(name).tag(i)
                        }
                    }
                } label: {
                    HStack(spacing: 6) {
                        Text(state.paletteName.isEmpty ? "Lightwave" : state.paletteName)
                            .font(.largeTitle.weight(.bold))
                            .lineLimit(1)
                            .minimumScaleFactor(0.6)
                        Image(systemName: "chevron.down")
                            .font(.title3.weight(.semibold))
                            .foregroundStyle(Theme.neon)
                    }
                }
                .buttonStyle(.plain)
                .disabled(state.paletteNames.isEmpty)
            }
            HStack(spacing: 8) {
                Chip(text: state.warmMode ? "WARMNESS" : "PALETTES", on: state.warmMode)
                Chip(text: state.gradient ? "GRADIENT" : "SINGLE", on: state.gradient)
                if state.dancing { Chip(text: "FADING", on: true) }
            }
        }
        .foregroundStyle(Theme.ice)
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    // MARK: Keys

    private var keys: some View {
        HStack(spacing: 8) {
            KeyButton(cap: "*", label: state.warmMode ? "palettes" : "warmness", live: state.warmMode) {
                client.send("ToggleWarmMode")
            }
            KeyButton(cap: "−", label: state.warmMode ? "cooler" : "palette") {
                client.send("CycleColor", -1)
            }
            KeyButton(cap: "+", label: state.warmMode ? "warmer" : "palette") {
                client.send("CycleColor", 1)
            }
            KeyButton(cap: "/", label: state.gradient ? "gradient" : "single", live: state.gradient) {
                client.send("ToggleGradient")
            }
            KeyButton(cap: "0", label: "all") {
                client.send("ToggleAll")
            }
        }
    }

    // MARK: Pads

    private var grid: some View {
        let cols = Array(repeating: GridItem(.flexible(), spacing: 10), count: 3)
        return LazyVGrid(columns: cols, spacing: 10) {
            ForEach(HUDState.numpadOrder, id: \.self) { n in
                PadTile(slot: state.slot(n)) { client.send("ToggleSlot", n) }
            }
        }
        .redacted(reason: client.hasState ? [] : .placeholder)
    }
}

// MARK: - Pieces

struct PadTile: View {
    let slot: Slot
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            VStack(spacing: 4) {
                Text("\(slot.number)")
                    .font(.system(.title2, design: .rounded).weight(.bold))
                    .foregroundStyle(slot.active ? .white : Theme.ice.opacity(0.7))
                Text(slot.mapped ? slot.name : "—")
                    .font(.footnote.weight(.semibold))
                    .multilineTextAlignment(.center)
                    .lineLimit(2)
                    .minimumScaleFactor(0.8)
                if slot.mapped {
                    if slot.ip.isEmpty {
                        Tag(text: "no link", color: Theme.amber)
                    } else if slot.overBluetooth {
                        Tag(text: "BLE", color: Color(red: 0.34, green: 0.84, blue: 1.0))
                    } else if !slot.model.isEmpty {
                        Tag(text: slot.model, color: Theme.ice.opacity(0.55))
                    }
                }
            }
            .foregroundStyle(Theme.ice)
            .padding(8)
            .frame(maxWidth: .infinity, minHeight: 104)
            .background(tileBackground)
            .overlay(
                RoundedRectangle(cornerRadius: 16)
                    .strokeBorder(slot.active ? Theme.neon : Theme.ice.opacity(0.14), lineWidth: 1)
            )
            .shadow(color: slot.active ? Theme.neon.opacity(0.6) : .clear, radius: 14)
            .opacity(slot.mapped ? 1 : 0.42)
        }
        .buttonStyle(PressStyle())
        .disabled(!slot.mapped)
        .sensoryFeedback(.impact(weight: .light), trigger: slot.active)
        .animation(.easeOut(duration: 0.2), value: slot.active)
        .accessibilityLabel("Pad \(slot.number), \(slot.mapped ? slot.name : "unmapped")")
        .accessibilityValue(slot.active ? "on" : "off")
    }

    @ViewBuilder
    private var tileBackground: some View {
        let shape = RoundedRectangle(cornerRadius: 16)
        if slot.active {
            shape.fill(
                RadialGradient(colors: [Theme.magenta.opacity(0.45), Theme.neon.opacity(0.35), Theme.panel],
                               center: .center, startRadius: 4, endRadius: 90)
            )
        } else {
            shape.fill(Theme.panel.opacity(0.85))
        }
    }
}

struct KeyButton: View {
    let cap: String
    let label: String
    var live = false
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            VStack(spacing: 4) {
                Text(cap)
                    .font(.system(.title3, design: .monospaced).weight(.bold))
                    .foregroundStyle(live ? Theme.magenta : Theme.ice)
                Text(label)
                    .font(.caption2)
                    .foregroundStyle(Theme.ice.opacity(0.65))
                    .lineLimit(1)
                    .minimumScaleFactor(0.7)
            }
            .frame(maxWidth: .infinity, minHeight: 56)
            .background(RoundedRectangle(cornerRadius: 12).fill(Theme.panel.opacity(0.85)))
            .overlay(
                RoundedRectangle(cornerRadius: 12)
                    .strokeBorder(live ? Theme.magenta.opacity(0.7) : Theme.ice.opacity(0.12), lineWidth: 1)
            )
        }
        .buttonStyle(PressStyle())
    }
}

/// A slider that holds the finger's value while dragging, so state pushes
/// arriving mid-drag don't yank the thumb back.
struct DimmerSlider: View {
    let label: String
    let value: Int
    let range: ClosedRange<Int>
    let step: Int
    var reversed = false
    let caption: (Int) -> String
    let send: (Int) -> Void

    @State private var live: Double?

    var body: some View {
        let shown = live.map { Int($0.rounded()) } ?? value
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Text(label.uppercased())
                    .font(.caption2.weight(.semibold))
                    .tracking(2)
                Spacer()
                Text(caption(shown))
                    .font(.caption.monospacedDigit())
            }
            .foregroundStyle(Theme.ice.opacity(0.7))
            Slider(
                value: Binding(
                    get: { Double(reversed ? range.upperBound + range.lowerBound - shown : shown) },
                    set: { raw in
                        let v = reversed ? Double(range.upperBound + range.lowerBound) - raw : raw
                        live = v
                        send(Int(v.rounded()))
                    }
                ),
                in: Double(range.lowerBound)...Double(range.upperBound),
                step: Double(step),
                onEditingChanged: { editing in if !editing { live = nil } }
            )
        }
        .padding(14)
        .background(RoundedRectangle(cornerRadius: 14).fill(Theme.panel.opacity(0.85)))
    }
}

struct StatusBanner: View {
    @Environment(LightwaveClient.self) private var client

    var body: some View {
        switch client.status {
        case .live, .idle:
            EmptyView()
        case .connecting:
            banner(Text("Connecting to your Mac…"), icon: "dot.radiowaves.left.and.right", color: Theme.ice)
        case .failed(let why):
            banner(Text(why), icon: "exclamationmark.triangle.fill", color: Theme.amber)
        }
    }

    private func banner(_ text: Text, icon: String, color: Color) -> some View {
        HStack(alignment: .top, spacing: 8) {
            Image(systemName: icon)
            text.fixedSize(horizontal: false, vertical: true)
            Spacer(minLength: 0)
        }
        .font(.footnote)
        .foregroundStyle(color)
        .padding(12)
        .background(RoundedRectangle(cornerRadius: 12).fill(color.opacity(0.1)))
    }
}

struct Chip: View {
    let text: String
    let on: Bool

    var body: some View {
        Text(text)
            .font(.caption2.weight(.bold))
            .tracking(1.5)
            .padding(.horizontal, 8)
            .padding(.vertical, 4)
            .foregroundStyle(on ? Theme.ink : Theme.ice.opacity(0.7))
            .background(Capsule().fill(on ? Theme.magenta : Theme.panel))
    }
}

struct Tag: View {
    let text: String
    let color: Color

    var body: some View {
        Text(text.uppercased())
            .font(.system(size: 9, weight: .semibold))
            .tracking(1)
            .foregroundStyle(color)
    }
}

struct PressStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .scaleEffect(configuration.isPressed ? 0.96 : 1)
            .animation(.easeOut(duration: 0.12), value: configuration.isPressed)
    }
}
