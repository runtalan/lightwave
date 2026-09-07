# Govee Lightwave

Standalone implementation workspace for a separate, paid Elgato Stream Deck plugin. It contains a native Go runtime and a Stream Deck plugin bundle; it has no runtime dependency on the parent Lightwave desktop application.

**Product promise:** Your room, at your fingertips. A complete LAN lighting controller inside Stream Deck, with Lightwave’s neon visual identity, live controls, curated palettes, rooms, and saved scenes.

Stream Deck is the everyday interface. Its property inspector handles setup. A bundled plugin process talks directly to supported Govee lights; customers do not install or run the Lightwave desktop app, a server, or a separate dashboard.

## Current build

`plugin/main.go` implements LAN discovery, state observation, local device persistence, power, brightness, RGB color, white-temperature commands, room-target-ready action settings, palettes, status feedback, and a deliberately slow whole-device color fade. The included Stream Deck actions are ready for individual devices and all discovered devices. A future iteration needs the shared room/scene editor and exact-model compatibility matrix before this can be marketed as a complete paid release.

Build the macOS executable with:

```sh
./scripts/build.sh
```

The result is `com.dinksf.govee-lightwave.sdPlugin/bin/govee-lightwave`. Run `streamdeck pack com.dinksf.govee-lightwave.sdPlugin` from this folder once the Stream Deck CLI is installed. The resulting `.streamDeckPlugin` must be validated with physical hardware and Marketplace processing before it is distributed.

| Document | Purpose |
|---|---|
| [Product and experience](PRODUCT.md) | Launch scope, actions, setup, appearance, and product boundaries |
| [Technical design](ARCHITECTURE.md) | Independent runtime, reuse, LAN behavior, persistence, and packaging |
| [Delivery and launch](DELIVERY.md) | Implementation sequence, acceptance gates, commercial plan, and research |
| [Discovery repair](DISCOVERY.md) | LAN/Bluetooth scanner behavior, validation, and current limits |
| [UI/UX plan](UI-UX-PLAN.md) | Individual lights first, optional rooms deeper in configuration |

Planning baseline: September 6, 2026. Proposed defaults are recorded so implementation can proceed without another discovery round. External platform requirements and hardware capabilities must be rechecked during implementation.

The existing `streamdeck/` plugin and desktop application remain separate products. The working name is **Govee Lightwave**; publishing identity and name availability are release decisions.
