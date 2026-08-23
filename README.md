# Lightwave

A lightning-fast local control center for a Govee lighting ecosystem. Native Apple Silicon binary via Wails v2 (Go + React). Triggered from an Elgato Stream Deck, driven in real time by a Glorious numpad (QMK/VIA/MIDI).

Cloud REST is used **only** for first-run device discovery. Live control is Govee LAN UDP, with a Bluetooth LE fallback for lamps that have no LAN Control (CoreBluetooth, macOS).

## Setup

1. Copy `.env.example` to `.env` and set `GOVEE_API_KEY` from [Govee Developer](https://developer.govee.com).
2. On each Govee device, enable **LAN Control** (Settings → LAN Control).
3. Install toolchain: Go 1.22+, [Wails v2](https://wails.io), Node. Wails lives in `$HOME/go/bin` (Go’s `GOBIN`). That directory is on PATH via a small block in `~/.zshrc` and `~/.zprofile`; open a new terminal (or `source ~/.zshrc`) after first setup.

```bash
cd /Users/runtalan/LightWave
./scripts/dev.sh      # live HUD (prepends $HOME/go/bin even if this shell has not reloaded rc)
./scripts/build.sh    # macOS app at build/bin/lightwave.app (ad-hoc signed)
# or, in a new terminal: wails dev / wails build
```

**Launch the built app** (do not double-click a half-built bundle — `Contents/MacOS` must contain `lightwave`):

```bash
open /Users/runtalan/LightWave/build/bin/lightwave.app
```

Hot reload during development is `./scripts/dev.sh` (`wails dev`), not the `.app`. Stream Deck `--toggle` should point at the inner binary:

```bash
/Users/runtalan/LightWave/build/bin/lightwave.app/Contents/MacOS/lightwave --toggle
```

`./scripts/build.sh` ad-hoc signs the bundle (`codesign --force --deep --sign -`) and clears quarantine xattrs so Finder does not report the app as damaged. If you ever see that dialog after a failed or interrupted build, run `./scripts/build.sh` again — do not keep opening the incomplete `.app`.

## Config (in-app)

The HUD **Config** button (also `,` / `G`, or `lightwave --config`) opens a settings deck. First launch lands on **Lights** until you save a pad map.

| Section | What it does |
| --- | --- |
| **Lights** | Bind numpad 7–8–9 / 4–5–6 / 1–2–3 to Govee devices. Duplicate guard, unbind, link badges (LAN IP / BLE / no link), rescan, fill remaining, **Save map**. |
| **MIDI** | Brightness CC (default 7), alt CC, color +/− note numbers. |
| **HUD** | Brightness preview. The HUD never hides on its own; press `Enter` to dismiss it. |
| **Account** | API key present/missing, `.env` path, optional key stored in prefs, cloud + LAN scan. |

Persisted under `~/Library/Application Support/Lightwave/`:

- `slots.json` — pad mappings
- `config.json` — MIDI, optional API key

`.env` `GOVEE_API_KEY` still wins if both exist. **Done** returns to the HUD (first run requires a saved map).

## Stream Deck

Bind a key to the binary with `--toggle`. Single-instance IPC (`/tmp/lightwave.sock`) show/hides the HUD without spawning extra processes. If Lightwave is not running, `--toggle` starts it and shows the HUD.

The HUD never hides on its own. Press `Enter` (or numpad Enter) to dismiss it, or use `--toggle`. Hide uses WindowHide — the process keeps running. Launch again or `--toggle` to show it. Press `.` to quit the process outright.

```bash
lightwave --toggle
```

## MIDI (Glorious numpad)

| Input | Default | Env |
| --- | --- | --- |
| Brightness slider | CC 7 (also CC 1) | `MIDI_CC`, `MIDI_CC_ALT` |
| Color engine + | Note 60 or keyboard `+` / NumpadAdd | `MIDI_NOTE_PLUS` |
| Color engine − | Note 61 or keyboard `-` / NumpadSubtract | `MIDI_NOTE_MINUS` |

CC 0–127 maps to 0–100% brightness for **all lights in the active pool**. Missing MIDI hardware is fine — the app still launches.

## Control HUD

- Keys **1–9** / numpad **1–9** toggle that pad into the active pool (ignite)
- **\*** / numpad **\*** starts/stops a slow colour fade across every pooled light
- **0** / numpad **0** toggles every bound light: all off, or all back on at the current slider level and palette
- **Enter** / numpad **Enter** dismisses the HUD (the process keeps running; `--toggle` brings it back)
- **.** / numpad **.** quits Lightwave entirely (relaunch with `open` or the Stream Deck key)
- `+` / `-` cycle the Color Engine palettes: Warm Whites, Soft Ambers, Deep Oranges, Reds, Purples, Ocean, Fall Leaves, Sunset
- Multiple pooled lights get adjacent/complementary colors from the palette, not clones
- Tiles reached over Bluetooth show **BLE**; tiles with no route at all show **no link**

## LAN protocol

- Scan multicast `239.255.255.250:4001`, listen `:4002`
- Control JSON to device IP `:4003` (`turn`, `brightness`, `color`, `colorwc`)

## BLE protocol (fallback)

Lamps without LAN Control (e.g. H6168) are discovered by advertised name
(`Govee_…`, `ihoment_…`, `GBK_…`) and controlled over GATT:

- Service `00010203-0405-0607-0809-0a0b0c0d1910`, characteristic `…2b11`,
  Write Without Response
- 20-byte frames, XOR checksum in byte 19; keep-alive `0xAA01` every 2s
- Addressed by CoreBluetooth UUID (`ble:` prefix in slot storage); a LAN IP
  always wins over BLE when a lamp has both
- All CoreBluetooth work runs on a private dispatch queue
  (`internal/govee/ble_darwin.m`) — never the AppKit main thread

## Stream Deck plugin

`streamdeck/` holds a native Stream Deck plugin. It is a protocol adapter, not
a second copy of the app: Stream Deck events come in over its WebSocket, and
commands go out to the running Lightwave app over `/tmp/lightwave.sock`. All
device logic stays in Lightwave — which is also what makes Bluetooth work, since
macOS gates CoreBluetooth on the responsible process and the Stream Deck app
declares no Bluetooth usage string.

```bash
./streamdeck/build.sh --install   # build universal binary, install, restart Stream Deck
```

Actions: **Light** (toggle one pad, key shows the light's name and lights up when
on), **All Lights** (everything off, or back on), **Palette**, **Color Fade**, **Brightness** (key nudge, or the
dial on Stream Deck +). Keys track state pushed from Lightwave, so they stay
correct when lights are changed from the HUD, the numpad, or the Govee app.

Lightwave must be running; a key press when it is not shows an alert, and the
plugin reconnects on its own once the app is back. Plugin log:
`~/Library/Logs/Lightwave/streamdeck-plugin.log`.

### Profile

`streamdeck/Lightwave.streamDeckProfile` is a ready-made two-page layout — open
it to import. Page 1 holds four lights plus All Lights / Dimmer / Brighter / Color
Fade; page 2 holds the rest plus the palette controls.

Regenerate it after rebinding pads (it reads the live pad map, so keys carry
your real light names):

```bash
python3 streamdeck/makeprofile.py
```

Icons are generated too — `python3 streamdeck/genicons.py` from the `imgs`
directory redraws the set.

### IPC command surface

The socket accepts one line per command and replies with `STATE <json>`:
`PING`, `STATE`, `TOGGLE_SLOT <1-9>`, `SLOT_ON`/`SLOT_OFF <1-9>`,
`BRIGHTNESS <0-100|+n|-n>`, `ALL_OFF`, `ALL_ON`, `ALL_TOGGLE`, `DANCE`,
`PALETTE <+1|-1>`. `SUBSCRIBE`
holds the connection open and streams state on every change.

## Project layout

```
main.go                 CLI, single-instance, Wails window
app.go                  Bindings, inactivity hide, setup/HUD
internal/govee/         Cloud REST + LAN UDP + BLE (CoreBluetooth)
internal/midi/          CoreMIDI listener
internal/color/         Palette engine
internal/config/        .env + slots.json
internal/ipc/           /tmp/lightwave.sock
remote.go               IPC command surface for external controllers
streamdeck/             Stream Deck plugin (Go, native binary)
frontend/src            HUD + Config
```

Do not commit `.env`.
