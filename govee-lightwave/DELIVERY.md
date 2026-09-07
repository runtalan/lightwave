# Delivery and launch

## Implementation sequence

Estimates are planning ranges for one developer with access to physical test hardware, not delivery commitments. Allow approximately 7–10 engineering weeks plus external Marketplace review and time needed to obtain hardware.

| Milestone | Estimate | Reviewable result and exit gate |
|---|---|---|
| 0. Prove standalone packaging | 3–5 days | New UUID/module, launches through Stream Deck on both platforms, discovers and toggles one supported light with desktop app absent; verify protected-package route early |
| 1. LAN foundation | 1–2 weeks | Identity registry, queues, observed/pending state, persistence, capability records, wake/reconnect, diagnostics; passes fake-device and real-device failure tests |
| 2. Complete daily controls | 1–2 weeks | Inspector onboarding, aliases, rooms, power/brightness/color/temperature; all usable from keys and without an API key |
| 3. Lightwave experience | 1–2 weeks | Palettes, scenes, fade, sweep, key rendering, dial layouts, starter profiles; explicit overlap and partial-failure behavior |
| 4. Paid-product hardening | 1–2 weeks | Clean installs/updates, OS and hardware matrix, bounded resource usage, protected-package offline tests, notices, support documents |
| 5. Private beta and listing | 1 week plus review | External users complete setup, compatibility data published, listing media finalized, package submitted and accepted before release |

Keep implementation isolated in `govee-lightwave/`. Do not rename or migrate the existing desktop app or its plugin. Any future migration is explicit export/import with device rebinding, never a hidden first-launch dependency.

## Verification and release gates

Tests during implementation should verify observable behavior and protocol failure handling, not repeat constants or rendering implementation details.

| Area | Required evidence |
|---|---|
| Independence | Export only this folder, build/package it, install on a machine without Lightwave, and complete setup/control |
| Protocol | Fake UDP devices exercise malformed packets, loss, duplicates, late replies, IP changes, missing capabilities, retries, and command supersession |
| Actual hardware | Record exact SKU, firmware, OS, interface, discovery, each command, readback, and reconnect results; include bulb, strip, and floor-lamp classes where available |
| Network | Wi-Fi/Ethernet, multiple interfaces, VPN, guest isolation, blocked multicast, manual IP, firewall denial, port conflict, router restart |
| Lifecycle | Host restart, plugin update, sleep/wake, disconnect/reconnect, profile switch, multiple inspectors, duplicated keys, overlapping rooms |
| Control semantics | Group mixed/offline state, zero brightness, restore nonzero level, unsupported temperature, scene partial failure, fade cancellation, all-off priority |
| Persistence | Corrupt file recovery, upgrade migration, missing references, backup restore, import validation, machine-to-machine rebinding |
| Rendering | Real 72-pixel readability, custom/blank titles, long names, reduced motion, offline/pending distinction, Stream Deck + touch layout |
| Commercial package | CLI validation, clean install/update/uninstall, protected build, offline cold start after installation, supported architecture execution |

Provisional performance targets: key input feedback within 100 ms; 95th-percentile observed power/brightness confirmation within 1 second on a healthy test LAN; status converges within 5 seconds for visible targets changed externally. Measure across 1, 5, and 20 devices. If hardware cannot meet a target, adjust the behavior and published claims before launch. Do not claim these figures as measurements today.

Run an eight-hour mixed-use soak with effects, repeated dial input, and disconnects. Target under 100 MB plugin memory and under 2% average idle CPU on a named reference machine; investigate sustained growth. Render on state changes and limit decorative animation to 2–5 updates per second on visible keys, with a global render budget. Rendering frequency never dictates lighting-command frequency.

Launch requires all core workflows passing on both target platforms, zero critical data-loss/control defects, and a published list of actually tested devices. Unknown models may be discoverable but remain “unverified”; do not promote an entire model family based on a shared prefix.

## Commercial plan

Propose one paid edition with every launch feature, sold as a one-time purchase through Elgato Marketplace. Test a **$19.99 launch price and $24.99 regular price** as positioning hypotheses during beta. These are proposed prices, not competitor-derived market values. Include maintenance for the purchased major version; avoid promising lifetime feature expansion.

Elgato’s published revenue share is 70% to makers and 30% to Elgato. At $24.99, simple 70% arithmetic is approximately $17.49 per sale before other applicable adjustments. Model support cost and refunds before locking the price. [Revenue share](https://docs.elgato.com/monetization/revenue-share/)

Prefer Marketplace payment and DRM to a custom checkout, licensing server, or account system. Verify seller eligibility, payout setup, pricing options, customer regions, and protected-package behavior in Maker Console. Elgato describes paid-product onboarding and payment availability in its [Maker guide](https://docs.elgato.com/marketplace/become-a-maker/). Do not promise a trial, license transfer policy, or particular refund terms until platform support is confirmed.

The repository contains an MIT license with a Renato Untalan copyright notice. Track copied code/assets and retain applicable notices. Record ownership and third-party licenses before setting commercial package terms; do not assume a new folder changes rights in existing source. This is a release inventory task, not a change to the existing repository’s license.

Keep **Govee Lightwave** as the requested working name. Check Marketplace availability and third-party brand presentation before final publication. Prepare “Lightwave for Govee” as a possible listing alternative only if necessary. Clearly describe an independent compatible controller; do not imply official Govee or Elgato authorship. Confirm the publisher identity because the existing manifest author and repository copyright name differ.

## Listing and support materials

Create a product icon, dark neon hero image, readable screenshots of actual keys/inspector/dials, a short room-control demo, and three starter profiles. Show real LAN controls and measured behavior. Explain required LAN Control support above the purchase decision, with a compatibility page and setup guide linked from the listing.

Core differentiators to demonstrate: install once inside Stream Deck; locally named devices and rooms; direct LAN commands; physical dimming; cohesive palettes; one-touch scenes; honest live connection feedback. Do not use blanket “all Govee lights,” “zero latency,” “unlimited commands,” or universal strip-gradient claims.

Support pack: first-light guide, LAN troubleshooting, tested-device table, scene/profile import guide, local diagnostics export, changelog, privacy statement, and a support contact. The FAQ must explain why a device can work in Govee Home but lack LAN control, why computer sleep stops fades, and what happens when another controller owns port 4002.

Beta goals: at least five testers across Windows and macOS, successful first-light setup without developer intervention for most testers, and explicit reporting of incompatible-device confusion. Track setup time, failures, and perceived value through voluntary feedback; no telemetry service is required.

## Open decisions and defaults

| Decision | Default | Resolve by |
|---|---|---|
| Product name | Govee Lightwave | Before listing artwork |
| Publisher / UUID | Existing namespace plus distinct product ID | Before beta profiles are distributed |
| Runtime | Native Go | Milestone 0 |
| Launch scope | LAN basics, rooms, palettes, scenes, fades, sweep | Set by this plan |
| Advanced gradients | Post-launch, exact model allowlist | Separate compatibility work |
| Offline marketing | Local control offline; entitlement behavior unverified | Protected-package testing |
| OS minimums | Windows 11; macOS 13+ | Clean-machine packaging tests |
| Price | $19.99 introductory / $24.99 regular | Beta feedback and seller setup |

## Research notes

Reviewed September 6, 2026. These references support platform planning; most architecture and UX choices above are proposed design decisions.

- [Elgato distribution](https://docs.elgato.com/streamdeck/sdk/introduction/distribution/): package through CLI; test the Maker Console processed build; SDK 3/6.9 baseline for DRM; immutable distributed files and no runtime manifest reads. Go support is explicitly documented.
- [Elgato plugin guidelines](https://docs.elgato.com/guidelines/stream-deck/plugins/): separate publisher/product identifiers, descriptive actions, monochrome action-list icons, readable key assets, and restrained programmatic rendering. Use the current guidelines when producing final assets.
- [Elgato manifest reference](https://docs.elgato.com/streamdeck/sdk/references/manifest/): validate platform, action, version, and controller declarations during implementation.
- [Elgato Maker guide](https://docs.elgato.com/marketplace/become-a-maker/) and [revenue share](https://docs.elgato.com/monetization/revenue-share/): commercial distribution baseline; account-specific eligibility is not verified here.
- [Govee LAN guide](https://app-h5.govee.com/user-manual/wlan-guide): authoritative protocol reference to inspect during implementation. The page is dynamic and did not expose its specification through the research tool, so protocol details in this plan are grounded in repository source and still require current-document and hardware verification.

Planning validation: inspected current adapter, manifest, LAN transport/model code, palette engine references, license, inspector stylesheet, and rendered status-key reference image. No application tests or hardware tests were run because this change adds planning documents only.
