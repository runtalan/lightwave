# Configuration audit and revised flow

Scope: planning only for the visual redesign and room workflow. Discovery transport, catalog delivery, status text, and replacement of stale options are implemented separately. No redesigned layout is included in this change.

## Decision

The first experience is **choose a light, press the key**. Rooms are optional organization, reached through an advanced control. A room must never be a prerequisite for controlling one lamp.

Keep Stream Deck as the everyday interface. The embedded inspector shows the configuration for the particular action the person dragged onto the deck. Avoid asking them to choose the action a second time from a generic Operation menu.

## Audit of the current build

Locations refer to the configuration code inspected before the discovery repair; line numbers may move as the implementation evolves.

### action.html

- `com.dinksf.govee-lightwave.sdPlugin/ui/action.html:3` — empty target defaults to all lights; choosing nothing should not imply a whole-room command.
- `com.dinksf.govee-lightwave.sdPlugin/ui/action.html:4` — every action exposes every operation, duplicating the action list and allowing nonsensical combinations.
- `com.dinksf.govee-lightwave.sdPlugin/ui/action.html:5` — “Value” has no unit; the 1–100 range is also used for white temperature.
- `com.dinksf.govee-lightwave.sdPlugin/ui/action.html:8` — room creation appears in every inspector before the user has successfully controlled a light; the persistent checklist crowds the main task.
- `com.dinksf.govee-lightwave.sdPlugin/ui/action.html:9` — no progress, retry, or permission-specific recovery; restarting is presented as normal setup.
- `com.dinksf.govee-lightwave.sdPlugin/ui/action.html:14` — UI defaults and runtime defaults disagree; partial saves can change behavior unexpectedly.
- `com.dinksf.govee-lightwave.sdPlugin/ui/action.html:15` — catalog updates append duplicate options and checkboxes; discovery repair now replaces results and preserves unsaved member selection.
- `com.dinksf.govee-lightwave.sdPlugin/ui/action.html:17` — room validation uses generic help text, no field focus, no save acknowledgment, and no announced async result.
- `com.dinksf.govee-lightwave.sdPlugin/ui/action.html:4` — Scene is exposed without a scene picker/editor; Fade and Sweep do not have configuration matching their labels.

### pi.js and pi.css

- `com.dinksf.govee-lightwave.sdPlugin/ui/pi.js:11` — no visible connection lifecycle or reconnect recovery; scan status needs an explicit state model.
- `com.dinksf.govee-lightwave.sdPlugin/ui/pi.css:1` — dense inline form styling, no designed hover/focus states, no long-name handling; verify contrast at actual inspector width.
- `com.dinksf.govee-lightwave.sdPlugin/ui/pi.css:1` — a scrollable room checklist adds nested scrolling inside a small inspector.

### Runtime issues affecting the configuration experience

- `plugin/main.go` / `members` — missing IDs fell through to all lights; the discovery repair now returns an empty target for unknown/Bluetooth-only IDs. An empty initial assignment still needs its own explicit unassigned state in the redesign.
- `plugin/main.go` / `handle` — saved room-name/member fields recreate rooms on ordinary settings events and override a later individual target selection. Room mutations must be explicit requests, independent of action settings.
- `plugin/main.go` / `handle` — name-derived room IDs collide and make renaming ambiguous. Use persistent UUIDs.
- `plugin/main.go` / `sendInspector` — catalog messages lacked action routing and live discovery pushes; corrected in the discovery repair.

The accessibility and form audit uses [Web Interface Guidelines](https://raw.githubusercontent.com/vercel-labs/web-interface-guidelines/main/command.md). Findings are based on code and user-reported friction. The current inspector was also opened in the collaborative browser to verify discovery updates; the proposed redesign has not been implemented or visually validated.

## Proposed inspector hierarchy

| Surface | Show | Keep deeper |
|---|---|---|
| Unconfigured Power key | “Choose a light”, live discoveries, one Find lights button | Connection help, manual address, room management |
| Configured individual light | Light name; action-specific setting; concise saved/connection state | Rename, replacement, connection details |
| Advanced targeting | “Control several lights”; saved rooms | Create/edit a room |
| Room editor | Room name, selected lights, Save room | Member ordering, delete, advanced effects |

Default target is unassigned. A short discovered list contains the device name, model when needed to distinguish duplicates, and LAN/Bluetooth connection label. Present advanced addresses and protocol detail only under Connection details.

Selecting a supported individual light persists that target immediately and updates the key. Do not silently pick the first discovered device, auto-toggle it to prove discovery, or place every newly discovered lamp into an implicit all-lights target.

Configuration has no separate “Add device” step for a discovered controllable light. Selection is enrollment for that key. Renaming is optional and can be done after the first successful command.

## Flow A: first light

1. Drag Power to a key. Its inspector says “Choose a light” and begins LAN discovery.
2. Devices arrive as a stable, deduplicated list; show “Searching…” without hiding prior results.
3. Select the desired light. Default behavior is Toggle, with explicit On and Off alternatives.
4. The key displays its name and observed/unknown state. Confirmation reads “Ready — press your key.”
5. “More options” contains grouping and connection details; no room form is visible in the first-light flow.

Bluetooth discovery needs an explanatory permission step: “Find nearby Bluetooth lights.” If permission is denied, the next action is “Open Bluetooth settings,” not another empty rescan. The current implementation detects Bluetooth candidates only; keep them labeled **discovery only**, disabled as control targets, until the GATT control milestone is complete.

## Flow B: repeat use and utilities

Each action shares the same target selector but has only relevant fields:

| Action | Primary controls |
|---|---|
| Power | Light; Toggle / On / Off |
| Brightness | Light; Set / Increase / Decrease; percent or step |
| Color | Light; color picker and preview |
| White temperature | Compatible light; Kelvin value and model-supported bounds |
| Palette | Light; palette preview; direct apply / cycle |
| Scene | Saved scene picker; explicit Create/edit scene link |
| Fade | Light; palette; duration; start/stop behavior |
| Sweep | Ordered group; direction; short explanation of order |
| Status | Light; display options; display-only default |

Hide unsupported capabilities with an explanation in Connection details. Keep deterministic On/Off and scene actions for Stream Deck Multi Actions. Utility settings such as diagnostics export and rescanning belong in a common Connection help section, not among light commands.

Sweep inherently needs multiple lights. Only that action may lead directly into selecting several lights; offer a group of selected devices without requiring a name first. Do not expose an action as complete until its behavior actually matches its label.

## Flow C: optional rooms

1. Open More options → Control several lights.
2. Select an existing room or choose “Create room.” The currently selected individual light is preselected.
3. Select additional lights and optionally accept a suggested name. Save is the only mutation; Cancel returns to the individual target unchanged.
4. After a successful save, bind the action to the new room ID and show the room name with member count. Other keys can choose it without recreating it.
5. Room editing is a separate explicit action. Changing brightness or switching a key back to an individual device must never rewrite room membership.

Use UUIDs for room identity, allow rename without changing keys, preserve ordering, and provide undo or confirmation on deletion. Missing members remain listed as unavailable. Do not silently delete or merge room members on rescan. Do not merge Bluetooth/LAN candidates by display name or a four-character address suffix; require verified identity or an explicit later link operation.

## States and recovery

| State | Message and next action |
|---|---|
| Searching | “Looking for lights…”; keep prior results; coalesce repeated Find requests |
| Nothing found | LAN: enable LAN Control/check network; Bluetooth: radio/permission help |
| Port conflict | “Another lighting controller is using LAN discovery”; retry once that app is closed |
| Radio off | “Bluetooth is off”; open OS settings |
| Permission denied | Name the permission owner; show settings path and Retry |
| Saved light absent | Keep assignment, label unavailable; offer Replace; never fall back to all lights |
| Bluetooth candidate | “Found over Bluetooth — control not available in this build” |
| Partial group availability | Show available/total; describe which members could not respond |
| Saving room | Disable duplicate save; retain draft; success or field-specific error |

Discovery completion means the scan window ended, not that every nearby device is supported. Differentiated discovery errors replace instructions to restart the app. On permission/network changes, a retry should repair the relevant transport while keeping discovered results from the other transport.

## Accessibility and visual acceptance

Use labeled controls, semantic buttons, visible keyboard focus, announced async status (`role=status`/`aria-live=polite`), and inline errors that focus the relevant field. Use “Brightness (%)” and “White temperature (K)” instead of “Value.” Long names wrap or truncate without pushing controls offscreen. Preserve checkbox drafts during catalog updates. Keep neon accents and dark surfaces, with legible contrast and a restrained hierarchy.

Test at typical Stream Deck inspector widths with long names, 0/1/20 devices, both transports, no mouse, and permission denial. First-light success takes one device selection after discovery. No room name or checklist appears in that path. Room creation takes one optional route and never changes unrelated keys.

## Later implementation sequence

1. Fix target/settings semantics: explicit unassigned/device/group/all types, canonical action defaults, UUID room CRUD messages, error acknowledgments.
2. Build the first-light inspector and action-specific fields.
3. Add advanced room editor with draft preservation and optional ordering.
4. Add connection recovery, accessible status feedback, and actual-width visual QA.
5. Connect Bluetooth control only after model-specific GATT validation and honest state reporting; Bluetooth discovery is not that validation.
