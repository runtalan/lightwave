# Product and experience

Scope update: Bluetooth discovery is now implemented; Bluetooth control still requires a separate implementation and device validation. The requested individual-light-first configuration flow supersedes the setup sequence below; see [UI/UX plan](UI-UX-PLAN.md). Discovery status and tested behavior are recorded in [Discovery repair](DISCOVERY.md).

## Positioning

Govee Lightwave is a premium lighting instrument for Stream Deck: physical control, a coherent room palette, and useful live feedback. Target customers are creators, desk enthusiasts, and people who already own LAN-capable Govee lights and want polished controls next to their other Stream Deck actions.

Suggested listing headline: **Your room. One touch.**

Suggested description: “Bring your Govee lights onto Stream Deck. Control brightness and color, compose room palettes, and recall your favorite lighting with illuminated keys and tactile dials. Runs inside Stream Deck and sends lighting commands directly over your local network.”

The full solution includes discovery, naming, rooms, scenes, control, feedback, troubleshooting, and starter profiles. It does not imply every feature or device in Govee’s ecosystem.

## Launch scope

| Include in version 1 | Product behavior |
|---|---|
| Local discovery and manual address | Find responding lights, verify identity, remember them through IP changes |
| Local names and rooms | Rename without a Govee API key; organize devices independently of physical keys |
| Power, brightness, RGB, supported white temperature | Per-device, room, or explicit all-managed-lights targeting |
| Curated palettes | Carry forward the existing 13 palettes; distribute related whole-device colors across a room |
| Saved scenes | Store chosen power, brightness, and supported color settings locally; recall from a key |
| Ambient color fade | Slowly move through palettes with bounded network traffic |
| Light Sweep | Bring room members up/down in a saved order with keys or a dial |
| Live status | Distinguish on, off, mixed, pending, unavailable, and unconfigured |
| Stream Deck + support | Useful brightness, temperature, palette, and sweep dial behavior |
| Starter profiles and diagnostics | Finish setup and resolve typical network problems inside Stream Deck |

Defer Bluetooth, Govee cloud integration, music/audio sync, screen capture, device firmware updates, remote web access, MIDI, scheduling, and cloud account synchronization. There is no nine-light pad-map restriction. Initially validate performance with up to 20 managed lights; this is a test target, not a proven capacity claim.

Per-strip segment gradients and proprietary Govee scenes are outside the launch promise. Existing code uses model-specific `ptReal` packets, and model-prefix heuristics do not establish compatibility. A later advanced-effects release requires exact model/firmware testing. **Room palette spread** means different whole-device colors across lamps; it must never be labeled as a strip gradient.

## Actions

Use nine configurable actions under the Govee Lightwave category. All relevant actions share a target selector: device, room, or all managed lights. Newly discovered lights are not automatically enrolled in “all.”

| Action | Key behavior | Dial / touch behavior | Settings |
|---|---|---|---|
| Power | Toggle, explicit on, or explicit off | Not required | Target; operation |
| Brightness | Set level or step up/down | Rotate adjusts; push toggles target power | Target; step; preset; power-on policy |
| Color | Apply RGB swatch | Not required | Target; swatch; power-on policy |
| White Temperature | Apply selected temperature | Rotate changes supported temperature | Target; temperature; step |
| Palette | Apply selected palette or cycle | Rotate previews, push applies | Target; palette; uniform/spread; eligible members |
| Scene | Recall a named scene | Not required | Scene; edit/save controls in inspector |
| Color Fade | Start/stop selected fade | Not required | Target; palette; duration; spread |
| Light Sweep | Step forward/backward | Rotate steps; push toggles group power | Room; member order; direction |
| Status | Display room state; optionally toggle power | Not required | Target; press behavior; animation |

Default Status press behavior is display-only, avoiding accidental room changes. Provide explicit On/Off configurations for deterministic Stream Deck Multi Actions. Every essential dial operation also has a key equivalent.

Group toggle: if any reachable member is on, request all members off; otherwise request all on. Unknown/offline members stay visibly unknown. An empty group does nothing and shows “Choose lights.”

Brightness defaults to affecting lit members only; an optional “Turn lights on” setting makes preset behavior explicit. For heterogeneous levels, display “Mixed” and apply relative steps to each member’s current value; an absolute preset deliberately normalizes them. Translate zero to power-off and retain each device’s last nonzero level.

Color and palette changes default to lit members only. Scene recall applies its explicit saved power settings. A device missing a requested capability is skipped and counted in partial results; temperature controls are disabled when no target supports them.

Manual color/scene/power actions stop fades on affected devices. Overlapping rooms cannot run competing effect loops on the same device. Fade owns color only, so manual brightness can remain independent. All-off cancels affected effects and pending sweep operations. Startup and reconnection never automatically replay old light-changing commands.

## Setup in Stream Deck

1. Install the plugin and drag Power onto a key. The unconfigured key says “Choose lights.”
2. The inspector explains: enable LAN Control for each supported light in Govee Home, keep the computer and lights on a reachable local network, then choose **Find lights**.
3. Show discovered devices with model, local name, connection state, and last address. Discovery does not change lights. Offer an explicit **Identify** action with temporary change and best-effort restore only when prior state is known.
4. Select a device and give it a local name, such as “Desk Lamp.” The key becomes usable immediately after saving.
5. Use **Manage lights and rooms** within the inspector to create rooms and reorder members for palette spread and sweep.
6. Add a palette and scene key, or install a bundled starter profile. Profile placeholders use named roles; guide the user through binding these roles to their devices.

The inspector is a compact embedded settings surface, not a second application. Common controls: target, behavior, appearance, and a collapsible connection section. Device/room/scene editors are shared across actions. Configuration should remain usable at Stream Deck inspector widths with keyboard focus, labeled fields, and clear save feedback.

No results: offer rescan, manual private IP, network interface selection, and concise checks for LAN Control, guest Wi-Fi isolation, firewall permission, VPN routing, and a UDP port already in use. Manual IP cannot make a cloud-only device support LAN control.

Scene editing lets users specify values explicitly or capture available current state. Unknown fields are marked and omitted, never silently invented. Deleted room/scene references remain visible as missing assignments; they are never rebound to an unrelated item.

## Visual direction

Preserve the existing status key’s near-black violet ground, subtle grid, neon wave, magenta highlights, pale lavender type, palette swatches, and brightness rail. Source reference: `../docs/img/indicator-states.png` and `../streamdeck/plugin/internal/render/indicator.go`.

Modernize the hierarchy: readable name, one dominant value or state, then one secondary indicator. Keep pixel-display character where readable, but reduce dense labels. Futuristic should feel calm and precise. The new product should visibly belong to Lightwave while using room/device names and independent controls instead of desktop pad numbers.

| Token | Proposed value / use |
|---|---|
| Background | `#06030C` |
| Surface | `#120820` |
| Primary accent | `#B026FF` |
| Secondary accent | `#FF2EC8` |
| Primary text | `#E9D5FF` |
| Quiet decoration | `#6A4A8A`; not small essential text |
| Actual light color | Swatch and restrained glow, never the only state signal |

Key states: lit glow + ON; subdued + OFF; split indicator + MIXED; dotted marker + pending; broken-link symbol + OFFLINE; assignment symbol + SETUP. Do not depict offline as off. Respect user key titles, including intentionally blank titles. Truncate long local names predictably and expose the full name in settings.

Use movement sparingly on active fade/status keys, with a reduced-motion option. Design at actual 72-pixel key size and export high-resolution assets. Stream Deck + should show the target, current value, and useful progress feedback. Use monochrome action-list icons, keeping neon styling for the keys and product imagery.

Starter profile: a 15-key room layout with six light placeholders, three scenes, palette, fade, brightness down/up, status, and all-off. A separate Stream Deck + profile prioritizes scene/power keys and four dials: brightness, white temperature, palette, sweep. Include a compact six-key profile so launch does not depend on owning a large deck.

## Compatibility promise

Supports tested Govee models with functioning LAN Control, on supported desktop operating systems while Stream Deck is running. Govee Home may be needed to provision devices and enable LAN Control. No Govee account or API key is required by the plugin.

Lighting control is designed to operate without internet after installation. Marketplace purchase, installation, updates, and any entitlement checks are separate; verify protected-package offline behavior before claiming unrestricted offline operation. The host computer must remain awake; active fades stop when the plugin stops, and lights ordinarily retain their last commanded state.
