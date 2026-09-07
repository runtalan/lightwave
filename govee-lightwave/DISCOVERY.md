# Discovery repair

This update implements discovery only. The UI/room redesign is a separate [plan](UI-UX-PLAN.md). Bluetooth light control (connecting, finding writable characteristics, encoding model-specific commands, and reporting state) is not implemented or implied by finding an advertisement.

## Findings and changes

The installed process had no UDP 4002 listener during diagnosis. The original code attempted the bind once, silently continued when it failed, and never retried. It also omitted multicast membership, discarded discovery send errors, did not push newly discovered devices to an open inspector, and omitted the action identifier when routing inspector messages. The inspector appended every catalog response, producing duplicates and stale selections.

The repair retries the listener on each scan, reports port/network failures, joins the Govee multicast group on eligible interfaces, and sends multicast plus directed-broadcast discovery from local IPv4 interfaces. Discovery replies use the actual sender address and validated device identity. A port conflict is reported rather than taking over another controller's socket. Subsequent scans recheck interfaces; restarting a network interface can still require reopening the plugin if the OS retains a stale membership for the same interface index.

Catalog changes now reach open inspectors. Option replacement preserves selected lights and room checkbox drafts. Reopening an inspector initiates discovery; background LAN scans retry every 15 seconds. Missing or Bluetooth-only targets cannot fall through to controlling all LAN lights. No power/color commands are used to verify discovery.

## Bluetooth

- macOS: a bundled, short-lived `Govee Lightwave Discovery.app` helper uses CoreBluetooth and owns its Bluetooth usage description. It is launched through LaunchServices, has no control window, and needs no desktop Lightwave install. Scan reports travel through a private temporary directory removed after the scan. It stops on timeout or when the plugin exits.
- Windows: a native WinRT advertisement watcher runs a bounded scan, recognizes Govee manufacturer data or advertised names, and reports adapter errors. The Windows implementation is cross-compiled but not physically tested here.
- macOS additionally recognizes the Govee service UUID in nameless advertisements. Both implementations preserve names when later advertisements omit them. A manufacturer match can identify a Govee device without proving it is a controllable light.
- LAN IDs and Bluetooth IDs remain distinct. There is no unsafe automatic merge based on model, name, or a short address suffix.
- Bluetooth candidates appear as disabled **discovery only** entries. They are not added to the controllable LAN registry or implicitly enrolled in all-lights commands.

Bluetooth permission belongs to **Govee Lightwave Discovery** on macOS. If the scan times out or access is denied, enable it in System Settings → Privacy & Security → Bluetooth, then reopen an action inspector to retry. Discovery currently starts once per plugin launch and on an inspector scan request; the redesigned flow will make the permission request an explicit Find Bluetooth lights action.

## Verification

On the local Mac, the read-only diagnostic found four distinct LAN devices: two H6072 units, one H6168, and one H61E5. After installing the repaired bundle, the actual Stream Deck-launched plugin connected successfully, owned UDP 4002, and persisted those four devices to its own configuration store. Bluetooth reached the permission step but timed out without permission being granted in the diagnostic; nearby BLE results and BLE control are not verified.

Automated tests cover invalid packets, source-address validation, malformed state replies, recovery after a port conflict, Bluetooth advertisement filtering, inspector routing, and missing-target isolation. Run `go test -race ./...` and `go vet ./...` from `plugin/`.

The inspector's live catalog handler was exercised in the collaborative browser: sending the same catalog twice left one device entry and one member checkbox, preserved the selection and checked draft, and kept Bluetooth candidates disabled. This uses simulated catalogs; host discovery is separately verified from real LAN packets.

Build with `./scripts/build.sh`. It creates a universal macOS plugin executable, a Windows x64 executable, and a universal macOS Bluetooth helper. The helper is ad-hoc signed for local testing; release signing/notarization and Intel/Windows runtime testing remain release gates. The current local Swift toolchain emits an Intel compatibility-library warning; the Intel artifact is not runtime-validated.

For a standalone read-only diagnostic (close this plugin first to free UDP 4002):

```sh
./com.dinksf.govee-lightwave.sdPlugin/bin/govee-lightwave --discover
```

## References

[Apple CoreBluetooth](https://developer.apple.com/documentation/corebluetooth/cbcentralmanager) describes scanner state and advertising discovery. [Apple Bluetooth usage description](https://developer.apple.com/documentation/bundleresources/information-property-list/nsbluetoothalwaysusagedescription) documents the app privacy string. [Microsoft advertisement scanning](https://learn.microsoft.com/en-us/windows/uwp/devices-sensors/ble-beacon) documents active scan responses and watcher behavior. Device filtering also follows the existing Lightwave repository's Govee advertisement handling.
