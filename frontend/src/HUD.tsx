import { useEffect } from 'react'
import {
  ToggleAll,
  CycleColor,
  Quit,
  ToggleDance,
  ToggleGradient,
  HideHUD,
  OpenConfig,
  ToggleSlot,
} from '../wailsjs/go/main/App'
import { BrightnessSlider, TitleBar } from './chrome'
import { NUMPAD_ORDER, type HUDState } from './types'
import { slotByNumber } from './lib'

type Props = {
  state: HUDState
  onState: (s: HUDState) => void
}

const KEY_TO_SLOT: Record<string, number> = {
  Digit1: 1,
  Digit2: 2,
  Digit3: 3,
  Digit4: 4,
  Digit5: 5,
  Digit6: 6,
  Digit7: 7,
  Digit8: 8,
  Digit9: 9,
  Numpad1: 1,
  Numpad2: 2,
  Numpad3: 3,
  Numpad4: 4,
  Numpad5: 5,
  Numpad6: 6,
  Numpad7: 7,
  Numpad8: 8,
  Numpad9: 9,
}

function keyToSlot(e: KeyboardEvent): number | null {
  return KEY_TO_SLOT[e.code] ?? null
}

export function HUD({ state }: Props) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Enter' || e.code === 'NumpadEnter') {
        e.preventDefault()
        void HideHUD()
        return
      }
      // Activity pings are handled once, app-wide, by bindWindowActivity;
      // a second ping per keystroke here just doubled the bridge traffic.
      // Period / numpad decimal: quit outright, unlike Enter which only hides.
      if (e.code === 'Period' || e.code === 'NumpadDecimal' || e.key === '.') {
        e.preventDefault()
        void Quit()
        return
      }
      // Star / asterisk: numpad *, or Shift+8 on the number row.
      if (e.code === 'NumpadMultiply' || e.key === '*') {
        e.preventDefault()
        void ToggleDance()
        return
      }
      if (e.code === 'Digit0' || e.code === 'Numpad0') {
        e.preventDefault()
        void ToggleAll()
        return
      }
      const slot = keyToSlot(e)
      if (slot) {
        e.preventDefault()
        void ToggleSlot(slot)
        return
      }
      if (e.code === 'NumpadAdd' || e.key === '+') {
        e.preventDefault()
        void CycleColor(1)
        return
      }
      // Minus switches the scene style rather than cycling the palette
      // backwards; plus still walks the palette and wraps, so every palette
      // stays reachable.
      if (e.code === 'NumpadSubtract' || e.key === '-') {
        e.preventDefault()
        void ToggleGradient()
        return
      }
      if (e.key === ',' || e.key === 'g' || e.key === 'G') {
        e.preventDefault()
        void OpenConfig()
      }
    }
    // Mouse motion is already relayed (throttled) by bindWindowActivity in
    // App; a second mousemove listener here doubled the per-event IPC.
    window.addEventListener('keydown', onKey)
    return () => {
      window.removeEventListener('keydown', onKey)
    }
  }, [])

  return (
    <div className="panel hud">
      <TitleBar />
      <header className="mast compact no-drag" data-no-drag>
        <div>
          <p className="eyebrow">lightwave</p>
          <h1>{state.paletteName}</h1>
        </div>
        <button type="button" className="config-launch" onClick={() => void OpenConfig()}>
          Config
        </button>
      </header>

      {/* The legend doubles as controls. On the desktop this just means a key
          can also be clicked; on a phone, where there is no keyboard, it is
          the only way to reach fades, palettes and all-off. Entries that only
          make sense on the desktop are marked so the web build can drop
          them. */}
      <ul className="keymap" aria-label="Shortcuts">
        <li>
          <button type="button" className="keycap" onClick={() => void ToggleDance()}>
            <kbd className={state.dancing ? 'live' : ''}>*</kbd>
            <span>color fades</span>
          </button>
        </li>
        <li>
          <button type="button" className="keycap" onClick={() => void CycleColor(1)}>
            <kbd>+</kbd>
            <span>palette</span>
          </button>
        </li>
        <li>
          <button type="button" className="keycap" onClick={() => void ToggleGradient()}>
            <kbd className={state.gradient ? 'live' : ''}>-</kbd>
            <span>{state.gradient ? 'gradient' : 'single'}</span>
          </button>
        </li>
        <li>
          <button type="button" className="keycap" onClick={() => void ToggleAll()}>
            <kbd>0</kbd>
            <span>all on/off</span>
          </button>
        </li>
        <li data-desktop-only>
          <button type="button" className="keycap" onClick={() => void HideHUD()}>
            <kbd>Enter</kbd>
            <span>hide</span>
          </button>
        </li>
        <li data-desktop-only>
          <button type="button" className="keycap" onClick={() => void Quit()}>
            <kbd>.</kbd>
            <span>quit</span>
          </button>
        </li>
      </ul>

      <div className="grid" role="grid" aria-label="Active pool">
        {NUMPAD_ORDER.map((n) => {
          const slot = slotByNumber(state, n)
          const mapped = Boolean(slot?.deviceId)
          return (
            <button
              key={n}
              type="button"
              className={`tile ${slot?.active ? 'ignited' : ''} ${mapped ? '' : 'ghosted'}`}
              onClick={() => mapped && void ToggleSlot(n)}
              disabled={!mapped}
            >
              <span className="pad">{n}</span>
              <span className="name">{mapped ? slot?.name : '—'}</span>
              {mapped && !slot?.ip && <span className="warn">no link</span>}
              {mapped && slot?.ip?.startsWith('ble:') && <span className="linkway">BLE</span>}
            </button>
          )
        })}
      </div>

      <BrightnessSlider value={state.brightness} />

      <footer className="hud-foot">
        <span className={state.midiConnected ? 'ok' : 'dim'}>
          {state.midiConnected ? `midi · ${state.midiPort}` : 'midi silent'}
        </span>
        <span className="dim">+ color · − {state.gradient ? 'gradient' : 'single'} · 1–9 pool</span>
      </footer>
    </div>
  )
}
