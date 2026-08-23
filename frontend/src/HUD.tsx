import { useEffect } from 'react'
import {
  AllOff,
  CycleColor,
  Quit,
  ToggleDance,
  HideHUD,
  OpenConfig,
  PingActivity,
  PingMotion,
  ToggleSlot,
} from '../wailsjs/go/main/App'
import { BrightnessSlider, TitleBar } from './chrome'
import { NUMPAD_ORDER, type HUDState } from './types'
import { slotByNumber } from './lib'

type Props = {
  state: HUDState
  onState: (s: HUDState) => void
}

function keyToSlot(e: KeyboardEvent): number | null {
  const map: Record<string, number> = {
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
  return map[e.code] ?? null
}

export function HUD({ state }: Props) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Enter' || e.code === 'NumpadEnter') {
        e.preventDefault()
        void HideHUD()
        return
      }
      void PingActivity()
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
        void AllOff()
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
      if (e.code === 'NumpadSubtract' || e.key === '-') {
        e.preventDefault()
        void CycleColor(-1)
        return
      }
      if (e.key === ',' || e.key === 'g' || e.key === 'G') {
        e.preventDefault()
        void OpenConfig()
      }
    }
    const onMove = () => void PingMotion()
    window.addEventListener('keydown', onKey)
    window.addEventListener('mousemove', onMove)
    return () => {
      window.removeEventListener('keydown', onKey)
      window.removeEventListener('mousemove', onMove)
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

      <ul className="keymap" aria-label="Keyboard shortcuts">
        <li>
          <kbd className={state.dancing ? 'live' : ''}>*</kbd>
          <span>color fades</span>
        </li>
        <li>
          <kbd>-</kbd>
          <kbd>+</kbd>
          <span>palette</span>
        </li>
        <li>
          <kbd>0</kbd>
          <span>all off</span>
        </li>
        <li>
          <kbd>Enter</kbd>
          <span>hide</span>
        </li>
        <li>
          <kbd>.</kbd>
          <span>quit</span>
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
        <span className="dim">+/− color · 1–9 pool</span>
      </footer>
    </div>
  )
}
