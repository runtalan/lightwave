import Foundation

/// One numbered pad, exactly as the desktop app names and positions it.
/// The phone never edits these; they arrive in every state push.
struct Slot: Decodable, Identifiable, Equatable {
    var number: Int
    var deviceId: String
    var name: String
    var model: String
    var ip: String
    var active: Bool
    var online: Bool

    var id: Int { number }
    var mapped: Bool { !deviceId.isEmpty }
    var overBluetooth: Bool { ip.hasPrefix("ble:") }

    static func empty(_ n: Int) -> Slot {
        Slot(number: n, deviceId: "", name: "unmapped", model: "", ip: "", active: false, online: false)
    }

    enum CodingKeys: String, CodingKey { case number, deviceId, name, model, ip, active, online }

    init(number: Int, deviceId: String, name: String, model: String, ip: String, active: Bool, online: Bool) {
        (self.number, self.deviceId, self.name, self.model, self.ip, self.active, self.online) =
            (number, deviceId, name, model, ip, active, online)
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        number = try c.decode(Int.self, forKey: .number)
        deviceId = try c.decodeIfPresent(String.self, forKey: .deviceId) ?? ""
        name = try c.decodeIfPresent(String.self, forKey: .name) ?? ""
        model = try c.decodeIfPresent(String.self, forKey: .model) ?? ""
        ip = try c.decodeIfPresent(String.self, forKey: .ip) ?? ""
        active = try c.decodeIfPresent(Bool.self, forKey: .active) ?? false
        online = try c.decodeIfPresent(Bool.self, forKey: .online) ?? false
    }
}

/// The subset of the desktop HUDState the phone renders. Missing fields keep
/// their defaults so an older or newer desktop build still decodes.
struct HUDState: Decodable, Equatable {
    var slots: [Slot] = (1...9).map(Slot.empty)
    var brightness = 80
    var paletteIndex = 0
    var paletteName = ""
    var paletteNames: [String] = []
    var warmMode = false
    var warmness = 3000
    var gradient = false
    var dancing = false

    /// Keypad layout, top row first — the same order the desktop grid uses.
    static let numpadOrder = [7, 8, 9, 4, 5, 6, 1, 2, 3]

    func slot(_ n: Int) -> Slot {
        if slots.indices.contains(n - 1), slots[n - 1].number == n { return slots[n - 1] }
        return slots.first { $0.number == n } ?? .empty(n)
    }

    enum CodingKeys: String, CodingKey {
        case slots, brightness, paletteIndex, paletteName, paletteNames, warmMode, warmness, gradient, dancing
    }

    init() {}

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        let d = HUDState()
        let decoded = try c.decodeIfPresent([Slot].self, forKey: .slots) ?? []
        slots = decoded.isEmpty ? d.slots : decoded
        brightness = try c.decodeIfPresent(Int.self, forKey: .brightness) ?? d.brightness
        paletteIndex = try c.decodeIfPresent(Int.self, forKey: .paletteIndex) ?? d.paletteIndex
        paletteName = try c.decodeIfPresent(String.self, forKey: .paletteName) ?? d.paletteName
        paletteNames = try c.decodeIfPresent([String].self, forKey: .paletteNames) ?? d.paletteNames
        warmMode = try c.decodeIfPresent(Bool.self, forKey: .warmMode) ?? d.warmMode
        warmness = try c.decodeIfPresent(Int.self, forKey: .warmness) ?? d.warmness
        gradient = try c.decodeIfPresent(Bool.self, forKey: .gradient) ?? d.gradient
        dancing = try c.decodeIfPresent(Bool.self, forKey: .dancing) ?? d.dancing
    }
}
