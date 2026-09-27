import SwiftUI

@main
struct LightwaveApp: App {
    @State private var client = LightwaveClient()
    @State private var discovery = Discovery()
    @Environment(\.scenePhase) private var phase

    var body: some Scene {
        WindowGroup {
            RootView()
                .environment(client)
                .environment(discovery)
                .preferredColorScheme(.dark)
                .tint(Theme.neon)
        }
        .onChange(of: phase) { _, now in
            switch now {
            case .active:
                discovery.start()
                client.start()
            case .background:
                // No stream while backgrounded; it reconnects on return.
                client.stop()
                discovery.stop()
            default:
                break
            }
        }
    }
}

enum Theme {
    static let neon = Color(red: 0.69, green: 0.15, blue: 1.0)
    static let magenta = Color(red: 1.0, green: 0.18, blue: 0.78)
    static let amber = Color(red: 1.0, green: 0.48, blue: 0.24)
    static let ice = Color(red: 0.91, green: 0.84, blue: 1.0)
    static let ink = Color(red: 0.04, green: 0.02, blue: 0.07)
    static let panel = Color(red: 0.08, green: 0.03, blue: 0.14)
}

struct RootView: View {
    @Environment(LightwaveClient.self) private var client
    @Environment(Discovery.self) private var discovery

    var body: some View {
        Group {
            if client.isConfigured {
                HUDView()
            } else {
                NavigationStack { ConnectView(firstRun: true) }
            }
        }
        .onAppear {
            discovery.start()
            client.start()
        }
        .task(id: discovery.macs) { await follow() }
    }

    /// Keeps the saved Mac reachable when its address changes, and connects
    /// on its own the first time exactly one Mac is on the network.
    private func follow() async {
        if !client.isConfigured, discovery.macs.count == 1, let mac = discovery.macs.first {
            await use(mac)
            return
        }
        guard !client.serviceName.isEmpty, client.status != .live,
              let mac = discovery.macs.first(where: { $0.name == client.serviceName }),
              let addr = await Discovery.resolve(mac.endpoint), addr != client.address
        else { return }
        client.address = addr
        client.restart()
    }

    private func use(_ mac: Discovery.Mac) async {
        guard let addr = await Discovery.resolve(mac.endpoint) else { return }
        client.serviceName = mac.name
        client.address = addr
        client.restart()
    }
}
