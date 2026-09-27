import SwiftUI

/// Where the Mac is — the only thing the phone is told. Pads, names and
/// palettes all come from the desktop app.
struct ConnectView: View {
    var firstRun = false
    @Environment(LightwaveClient.self) private var client
    @Environment(Discovery.self) private var discovery
    @Environment(\.dismiss) private var dismiss
    @State private var address = ""
    @State private var token = ""
    @State private var resolving: String?

    var body: some View {
        Form {
            Section {
                if discovery.macs.isEmpty {
                    HStack(spacing: 10) {
                        ProgressView()
                        Text("Looking for Lightwave on this network…")
                            .foregroundStyle(.secondary)
                    }
                }
                ForEach(discovery.macs) { mac in
                    Button {
                        Task { await pick(mac) }
                    } label: {
                        HStack {
                            Label(mac.name, systemImage: "desktopcomputer")
                            Spacer()
                            if resolving == mac.name {
                                ProgressView()
                            } else if mac.name == client.serviceName {
                                Image(systemName: "checkmark").foregroundStyle(Theme.neon)
                            }
                        }
                    }
                }
            } header: {
                Text("On this network")
            } footer: {
                Text("Switch on Config → Remote in Lightwave on your Mac and it appears here.")
            }

            Section {
                TextField("192.168.1.20:8787", text: $address)
                    .keyboardType(.URL)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                SecureField("Token (only if you set one)", text: $token)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                Button("Connect") {
                    client.serviceName = ""
                    client.address = address.trimmingCharacters(in: .whitespacesAndNewlines)
                    client.token = token
                    client.restart()
                    if !firstRun { dismiss() }
                }
                .disabled(address.trimmingCharacters(in: .whitespaces).isEmpty)
            } header: {
                Text("Or type the address")
            } footer: {
                Text("Use this over a VPN such as Tailscale, where Bonjour doesn't reach. Lightwave's Remote tab lists the address.")
            }
        }
        .navigationTitle(firstRun ? "Find your Mac" : "Connection")
        .navigationBarTitleDisplayMode(firstRun ? .large : .inline)
        .toolbar {
            if !firstRun {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
                }
            }
        }
        .onAppear {
            address = client.address
            token = client.token
            discovery.start()
        }
    }

    private func pick(_ mac: Discovery.Mac) async {
        resolving = mac.name
        defer { resolving = nil }
        guard let addr = await Discovery.resolve(mac.endpoint) else { return }
        client.serviceName = mac.name
        client.address = addr
        client.token = token
        address = addr
        client.restart()
        if !firstRun { dismiss() }
    }
}
