# Lightwave — developer notes

Build, architecture, and protocol details. For the user guide see the [README](../README.md).


A lightning-fast local control center for a Govee lighting ecosystem. Native binary via Wails v2 (Go + React) on macOS and Windows 11. Triggered from an Elgato Stream Deck, driven in real time by a Glorious numpad (QMK/VIA/MIDI).

Cloud REST is used **only** for first-run device discovery. Live control is Govee LAN UDP, with a Bluetooth LE fallback for lamps that have no LAN Control (CoreBluetooth on macOS, WinRT on Windows).

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

On Windows 11 (CGO gcc required — the MinGW Wails documents):

```powershell
.\scripts\dev.ps1
.\scripts\build.ps1   # build\bin\lightwave.exe
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

Global numpad controls use existing macOS Accessibility permission. Startup checks silently and skips the global keyboard hook if access is unavailable; it never prompts or opens System Settings. To enable those controls, grant the built Lightwave app access under **System Settings → Privacy & Security → Accessibility**, then restart Lightwave.

## Config (in-app)

The HUD **Config** button (also `,` / `G`, or `lightwave --config`) opens a settings deck. First launch lands on **Lights** until you save a pad map.

| Section | What it does |
| --- | --- |
| **Lights** | Bind numpad 7–8–9 / 4–5–6 / 1–2–3 to Govee devices. Duplicate guard, unbind, link badges (LAN IP / BLE / no link), rescan, fill remaining, **Save map**. |
| **MIDI** | Brightness CC (default 7), alt CC, color +/− note numbers. |
| **HUD** | Brightness preview. The HUD never hides on its own; press `Enter` to dismiss it. |
| **Account** | API key present/missing, `.env` path, optional key stored in prefs, cloud + LAN scan. |

Persisted under `~/Library/Application Support/Lightwave/` on macOS, or `%APPDATA%\Lightwave\` on Windows:

- `slots.json` — pad mappings
- `config.json` — MIDI, optional API key

`.env` `GOVEE_API_KEY` still wins if both exist. **Done** returns to the HUD (first run requires a saved map).

## Stream Deck

Bind a key to the binary with `--toggle`. Single-instance IPC (`/tmp/lightwave.sock`) show/hides the HUD without spawning extra processes. If Lightwave is not running, `--toggle` starts it and shows the HUD.

The HUD never hides on its own. Press `Enter` (or numpad Enter) to dismiss it, or use `--toggle`. Hide uses WindowHide — the process keeps running. Launch again or `--toggle` to show it. Press `.` to quit the process outright. On Windows, a second launch of `lightwave.exe` while it is already running is how you restore a hidden HUD (same single-instance socket as Stream Deck).

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
- Addressed by a platform identifier (`ble:` prefix in slot storage): CoreBluetooth UUID on macOS, Bluetooth address on Windows. A LAN IP always wins over BLE when a lamp has both
- All CoreBluetooth work runs on a private dispatch queue (`internal/govee/ble_darwin.m`) — never the AppKit main thread. Windows uses WinRT on a worker goroutine for the same reason (WebView2 owns the UI thread)

## Stream Deck plugin

`streamdeck/` holds a native Stream Deck plugin. It is a protocol adapter, not
a second copy of the app: Stream Deck events come in over its WebSocket, and
commands go out to the running Lightwave app over the single-instance socket
(`/tmp/lightwave.sock` on macOS, `%TEMP%\lightwave.sock` on Windows). All
device logic stays in Lightwave — which is also what makes Bluetooth work, since
the OS gates BLE on the responsible process and the Stream Deck app does not
hold that permission.

```bash
./streamdeck/build.sh --install   # macOS: universal binary, install, restart Stream Deck
# Windows: .\streamdeck\build.ps1 -Install
```

Actions: **Status**, **Light** (toggle one pad, key shows the light's name and
lights up when on), **All Lights** (everything off, or back on), **Palette**
(next/previous or jump to one; warmer/cooler in Warmness mode; on a Stream Deck +
dial the touch strip shows the current palette or temperature and a push switches
mode), **Color Mode** (Warmness → Palette → Solid Color, over the `MODE` socket
verb), **Color Fade**, **Pattern**, **Brightness** (read-only key, or the dial on
Stream Deck +), **Light Sweep** (a Stream Deck + dial that walks the lights on in
pad order and back off in reverse, one every 300ms), **Light Brightness**
and **Light Color** (a pair of Stream Deck + dials sharing one selected light,
which a push on either moves to the next lit light: turning sets that light's
level with `LEVEL`, which stores it as the pad's trim against the slider so it
can sit above it, or its own palette or white with `LIGHT_COLOR`; a tap on the
colour dial's strip walks room → palette → warm). Keys track state pushed
from Lightwave, so they stay correct when lights are changed from the HUD, the
numpad, or the Govee app.

**Tapo Plug** is the one action that does not go through Lightwave. The Go
plugin runs `bin/tapo_helper.py` as a child process (started on first use, so a
deck with no Tapo key never launches Python) and exchanges one JSON line per
request and reply with it; `plugin/internal/tapo` is the client. The helper is
a thin wrapper over python-kasa, which owns the Tapo protocols (KLAP and the
older AES handshake), and keeps one connection per plug, dropping and reopening
it after any error. The interpreter is the venv `setup-tapo.sh` creates at
`~/Library/Application Support/Lightwave/tapo-venv` (`%APPDATA%\Lightwave\tapo-venv`
on Windows); `LIGHTWAVE_TAPO_PYTHON` overrides it. The TP-Link login is a Stream
Deck *global* setting (not stored in profiles), sent to the helper as `auth`.
`plugin/plug.go` polls every plug that has a key on screen every 3s and
repaints with `render.PlugKey`. The bridge's tests are
`streamdeck/test_tapo_helper.py` (run with the venv's Python:
`python -m unittest streamdeck/test_tapo_helper.py`).

**Vacuum** works the same way for a Dreame robot vacuum: `bin/dreame_helper.py`
under the Python that `setup-dreame.sh` builds at
`~/Library/Application Support/Lightwave/dreame/venv` (`LIGHTWAVE_DREAME_PYTHON`
overrides it), client in `plugin/internal/dreame`. Both helpers share
`plugin/internal/helper`, which runs the child and does the JSON-line
request/reply matching. The cloud client is three files from
Tasshack/dreame-vacuum (protocol, exceptions, types), downloaded at the commit
pinned in `setup-dreame.sh` into `.../dreame/vendor` with a stub `__init__.py`,
because the upstream package imports its map and device layers (numpy, a JS
engine) for nothing the plugin uses. Move the pinned commit deliberately: the
protocol is reverse-engineered. The helper uses MIoT ids from that project:
state `2.1`, battery `3.1`, actions start `2.1`, pause `2.2`, charge `3.1`. Room cleaning is the custom-start action `4.1` with status `18` (segment cleaning) and a `{"selects":[[room,repeat,suction,water,1]]}` string in `4.10`; one room per command, because upstream notes the order field breaks some models. Room numbers come from the map, which the plugin does not decode. Suction (`4.4`) and water (`4.5`) are read and echoed back; on a self-washing base upstream uses the mop-wetness setting for water, which is not a plain property, so that value may differ from the app's.
The Dreame login (email, password, region) and the room names (`dreameRooms`, one `N = name` per line, parsed by `parseRooms` in `plugin/vacuum.go`) are Stream Deck global settings. Inspectors also send them to the plugin as `globals` in a `sendToPlugin` after saving, because Stream Deck only hands the plugin its global settings at startup.
Tests: `streamdeck/test_dreame_helper.py`, with a fake cloud.

**Vacuum Status** is a Stream Deck Neo info-bar action: `"Controllers": ["Neo"]`,
which the manifest schema only accepts with `Software.MinimumVersion` 7.6, so the
whole plugin requires 7.6. The layout (`layouts/infobar.json`) is one 232x50 pixmap
named `strip`, filled with a 464x100 image from `render.InfobarImage` through
`setFeedback`. Which manifest key gives a Neo action its layout is not stated in
Elgato's reference; this assumes `Encoder.layout`, as for the dials. The bar finds
its vacuum itself (the account's first online vacuum, `findBarVacuum`) and is
polled with the rest in `vacuumLoop`. `streamdeck/dreame_telemetry.py` is a
read-only probe for the map data (position, heading, room) that the bar will
show once its decoding is settled.

The palette and mode dials draw their touch strip with the custom layout in
`com.dinksf.lightwave.sdPlugin/layouts/dial.json`; the plugin fills its `strip`
pixmap with PNGs from `plugin/internal/render/dial.go`.

Lightwave must be running; a key press when it is not shows an alert, and the
plugin reconnects on its own once the app is back. Plugin log: `~/Library/Logs/Lightwave/streamdeck-plugin.log` on macOS,
`%APPDATA%\Lightwave\Logs\streamdeck-plugin.log` on Windows.

### Profile

`streamdeck/Lightwave.streamDeckProfile` is a ready-made two-page layout — open
it to import. Page 1 holds four lights plus Status / All Lights / Pattern / Color
Fade; page 2 holds the rest plus Palette −/+, Color Mode and All Lights; the
pages after that jump straight to each palette.

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
internal/govee/         Cloud REST + LAN UDP + BLE (CoreBluetooth / WinRT)
internal/midi/          RtMidi listener
internal/color/         Palette engine
internal/config/        .env + slots.json
internal/ipc/           single-instance socket
remote.go               IPC command surface for external controllers
streamdeck/             Stream Deck plugin (Go, native binary)
frontend/src            HUD + Config
```

Windows BLE lives in `internal/govee/ble_windows.go` (WinRT via `tinygo.org/x/bluetooth`) and shares the queue/keep-alive manager in `ble_host.go` with the CoreBluetooth backend. The Windows BLE module is imported only from a `windows` build tag; after changing it, tidy with `GOOS=windows go mod tidy` so macOS `go mod tidy` does not drop it.

Lightwave cannot be cross-compiled from macOS to Windows: MIDI (RtMidi) and Wails need a Windows CGO toolchain. Build on the Windows 11 machine.

Do not commit `.env`.
