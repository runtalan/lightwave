# Lightwave for iOS

A native SwiftUI client for the desktop app's phone server. Pad names, pad
positions (the same 7-8-9 / 4-5-6 / 1-2-3 keypad layout), palettes and live
state all come from the desktop app; nothing is set up on the phone.

## Use

1. On the Mac, switch on **Config → Remote** in Lightwave.
2. Open the app. The Mac is found over Bonjour (`_lightwave._tcp`) and picked
   automatically if it is the only one. Over a VPN such as Tailscale, where
   Bonjour doesn't reach, type the address shown in the Remote tab instead.
3. If you set a token in the Remote tab, enter it once on the connect screen.

## Build

The Xcode project is generated from `project.yml`:

```sh
brew install xcodegen
cd ios && xcodegen generate && open Lightwave.xcodeproj
```

Pick your team under Signing & Capabilities and run on your iPhone.
