import AppKit
import CoreBluetooth
import Foundation

// A short-lived, discovery-only helper. No pairing, GATT connections or writes.
// The helper has its own app identity because Stream Deck lacks Bluetooth usage
// metadata. Results stay in a private directory created by the plugin.
final class Scanner: NSObject, CBCentralManagerDelegate {
    let report: URL
    let owner: Int32
    var central: CBCentralManager!
    var devices: [String: [String: Any]] = [:]
    var peripherals: [UUID: CBPeripheral] = [:]
    var scanTimer: Timer?
    var completed = false
    let model = try! NSRegularExpression(pattern: "H[0-9]{2}[0-9A-Z]{2}")

    init(report: URL, owner: Int32) {
        self.report = report
        self.owner = owner
        super.init()
        publish("Waiting for Bluetooth permission…")
        central = CBCentralManager(delegate: self, queue: nil,
            options: [CBCentralManagerOptionShowPowerAlertKey: false])
        Timer.scheduledTimer(withTimeInterval: 45, repeats: false) { [weak self] _ in
            self?.finish("Bluetooth discovery timed out. Check Bluetooth permission and retry.")
        }
        Timer.scheduledTimer(withTimeInterval: 1, repeats: true) { [weak self] _ in
            guard let self = self else { return }
            if kill(self.owner, 0) != 0 || !FileManager.default.fileExists(atPath: self.report.deletingLastPathComponent().path) {
                self.central.stopScan()
                NSApplication.shared.terminate(nil)
            }
        }
    }

    func publish(_ status: String, done: Bool = false) {
        let data: [String: Any] = ["status": status, "done": done,
                                  "devices": devices.keys.sorted().compactMap { devices[$0] }]
        do { try JSONSerialization.data(withJSONObject: data).write(to: report, options: .atomic) }
        catch { NSLog("Discovery report failed: %@", String(describing: error)) }
    }

    func finish(_ status: String) {
        if completed { return }
        completed = true
        scanTimer?.invalidate()
        central.stopScan()
        publish(status, done: true)
        NSApplication.shared.terminate(nil)
    }

    func centralManagerDidUpdateState(_ central: CBCentralManager) {
        switch central.state {
        case .poweredOn:
            central.scanForPeripherals(withServices: nil, options: [CBCentralManagerScanOptionAllowDuplicatesKey: true])
            publish("Searching for nearby Bluetooth devices…")
            scanTimer?.invalidate()
            scanTimer = Timer.scheduledTimer(withTimeInterval: 20, repeats: false) { [weak self] _ in
                self?.finish("Bluetooth scan complete. Discovered devices require a separate control implementation.")
            }
        case .unauthorized:
            finish("Allow Govee Lightwave Discovery in System Settings → Privacy & Security → Bluetooth, then retry.")
        case .poweredOff:
            finish("Bluetooth is off. Turn it on in System Settings, then retry.")
        case .unsupported:
            finish("This Mac has no supported Bluetooth adapter.")
        case .resetting, .unknown:
            publish("Waiting for the Bluetooth adapter…")
        @unknown default:
            finish("Bluetooth is unavailable.")
        }
    }

    func centralManager(_ central: CBCentralManager, didDiscover peripheral: CBPeripheral,
                        advertisementData: [String: Any], rssi RSSI: NSNumber) {
        let name = advertisementData[CBAdvertisementDataLocalNameKey] as? String ?? peripheral.name ?? ""
        let upper = name.uppercased()
        let range = NSRange(upper.startIndex..., in: upper)
        let match = model.firstMatch(in: upper, range: range)
        let services = advertisementData[CBAdvertisementDataServiceUUIDsKey] as? [CBUUID] ?? []
        let serviceMatch = services.contains { $0.uuidString.lowercased() == "00010203-0405-0607-0809-0a0b0c0d1910" }
        let manufacturer = advertisementData[CBAdvertisementDataManufacturerDataKey] as? Data ?? Data()
        let companyMatch = manufacturer.count >= 2 && manufacturer[0] == 0x88 && manufacturer[1] == 0xec
        let nameMatch = ["GOVEE", "IHOMENT_", "IHOM_", "GVH_", "GBK_", "MINGER_"].contains { upper.hasPrefix($0) }
        guard serviceMatch || companyMatch || nameMatch || match != nil else { return }
        let id = "ble:" + peripheral.identifier.uuidString
        let modelName = match.map { (upper as NSString).substring(with: $0.range) } ?? ""
        let old = devices[id]
        let resolvedName = name.isEmpty ? (old?["name"] as? String ?? "Govee Bluetooth device") : name
        let resolvedModel = modelName.isEmpty ? (old?["model"] as? String ?? "") : modelName
        peripherals[peripheral.identifier] = peripheral
        devices[id] = ["id":id, "name":resolvedName, "model":resolvedModel,
                       "rssi":RSSI.intValue, "transport":"bluetooth", "controllable":false]
        // Name-less advertisements can acquire names in subsequent scan responses.
        if old == nil || old?["name"] as? String != resolvedName || old?["model"] as? String != resolvedModel {
            publish("Searching for nearby Bluetooth devices…")
        }
    }
}

guard CommandLine.arguments.count == 3, let owner = Int32(CommandLine.arguments[2]) else { exit(2) }
let application = NSApplication.shared
application.setActivationPolicy(.accessory)
let scanner = Scanner(report: URL(fileURLWithPath: CommandLine.arguments[1]), owner: owner)
application.run()
withExtendedLifetime(scanner) {}
