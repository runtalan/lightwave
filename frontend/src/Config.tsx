import { useEffect, useMemo, useState } from 'react'
import {
  MoveSlot,
  RenameSlot,
  AssignSlot,
  CloseConfig,
  CommitMappings,
  Discover,
  FillRemaining,
  SaveSettings,
  ScanLAN,
  SetConfigAPIKey,
} from '../wailsjs/go/main/App'
import { NUMPAD_ORDER, type ConfigTab, type HUDState, type SettingsView } from './types'
import { slotByNumber } from './lib'
import { BrightnessSlider, TitleBar } from './chrome'

type Props = {
  state: HUDState
  onState: (s: HUDState) => void
}

const TABS: { id: ConfigTab; label: string }[] = [
  { id: 'lights', label: 'Lights' },
  { id: 'midi', label: 'MIDI' },
  { id: 'hud', label: 'HUD' },
  { id: 'account', label: 'Account' },
]

export function Config({ state, onState }: Props) {
  const [tab, setTab] = useState<ConfigTab>('lights')
  const [err, setErr] = useState('')
  const [note, setNote] = useState('')

  async function done() {
    setErr('')
    try {
      await CloseConfig()
    } catch (e) {
      setErr(String(e))
      setTab('lights')
    }
  }

  return (
    <div className="panel config">
      <TitleBar />
      <header className="mast compact no-drag" data-no-drag>
        <div>
          <p className="eyebrow">{state.firstRun ? 'first ignition' : 'control deck'}</p>
          <h1>Config</h1>
        </div>
        <button type="button" className="ghost" onClick={() => void done()}>
          Done
        </button>
      </header>

      <div className="config-shell">
        <nav className="rail" aria-label="Config sections">
          {TABS.map((t) => (
            <button
              key={t.id}
              type="button"
              className={`rail-btn ${tab === t.id ? 'active' : ''}`}
              onClick={() => {
                setErr('')
                setNote('')
                setTab(t.id)
              }}
            >
              {t.label}
            </button>
          ))}
        </nav>

        <div className="config-body">
          {tab === 'lights' && <LightsPane state={state} onState={onState} setErr={setErr} setNote={setNote} />}
          {tab === 'midi' && <MidiPane state={state} setErr={setErr} setNote={setNote} />}
          {tab === 'hud' && <HudPane state={state} />}
          {tab === 'account' && <AccountPane state={state} onState={onState} setErr={setErr} setNote={setNote} />}
        </div>
      </div>

      {(err || note) && (
        <p className={`status ${err ? 'bad' : ''}`}>{err || note}</p>
      )}
    </div>
  )
}

function LightsPane({
  state,
  onState,
  setErr,
  setNote,
}: {
  state: HUDState
  onState: (s: HUDState) => void
  setErr: (s: string) => void
  setNote: (s: string) => void
}) {
  const [focus, setFocus] = useState(7)
  const [busy, setBusy] = useState(false)
  const [dragFrom, setDragFrom] = useState<number | null>(null)
  const [dragOver, setDragOver] = useState<number | null>(null)
  const [renaming, setRenaming] = useState<number | null>(null)

  async function rename(slot: number, value: string) {
    setRenaming(null)
    setErr('')
    try {
      const next = await RenameSlot(slot, value)
      onState(next)
      setNote(value.trim() ? `Renamed pad ${slot}.` : `Pad ${slot} back to its discovered name.`)
    } catch (e) {
      setErr(String(e))
    }
  }

  async function move(from: number, to: number) {
    setErr('')
    try {
      const next = await MoveSlot(from, to)
      onState(next)
      setNote(to === from ? '' : `Moved to pad ${to}.`)
    } catch (e) {
      setErr(String(e))
    }
  }

  const boundIds = useMemo(() => {
    const m = new Map<string, number>()
    for (const s of state.slots) {
      if (s.deviceId) m.set(s.deviceId, s.number)
    }
    return m
  }, [state.slots])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      // Typing in the rename field must not be captured as pad navigation.
      const t = e.target as HTMLElement | null
      if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA')) return
      const map: Record<string, number> = {
        Digit1: 1, Digit2: 2, Digit3: 3, Digit4: 4, Digit5: 5, Digit6: 6, Digit7: 7, Digit8: 8, Digit9: 9,
        Numpad1: 1, Numpad2: 2, Numpad3: 3, Numpad4: 4, Numpad5: 5, Numpad6: 6, Numpad7: 7, Numpad8: 8, Numpad9: 9,
      }
      const n = map[e.code]
      if (n) {
        e.preventDefault()
        setFocus(n)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  async function bind(slot: number, deviceId: string) {
    setErr('')
    try {
      const next = await AssignSlot(slot, deviceId)
      onState(next)
    } catch (e) {
      setErr(String(e))
    }
  }

  const statusCopy = !state.hasApiKey
    ? 'No Govee key yet — Account tab or .env, then rescan.'
    : state.discoverError && state.discoverError !== 'missing_key'
      ? `Cloud handshake failed: ${state.discoverError}`
      : state.discovering
        ? 'Sweeping the account and LAN…'
        : (state.catalog ?? []).length === 0
          ? 'No lights found. Enable LAN control, then rescan.'
          : `${state.catalog.length} lights. Select a pad, then a light.`

  return (
    <div className="pane lights-pane">
      <p className="lede">Bind numpad 1–9. A light can live on one pad only. Drag a bound pad onto another to move it — dropping on an occupied pad swaps the two. Click ✎ to rename.</p>
      <div className="grid" role="grid" aria-label="Numpad slots">
        {NUMPAD_ORDER.map((n) => {
          const slot = slotByNumber(state, n)
          const mapped = Boolean(slot?.deviceId)
          return (
            <button
              key={n}
              type="button"
              className={`tile ${mapped ? 'ignited' : ''} ${focus === n ? 'focused' : ''} ${dragOver === n && dragFrom !== null && dragFrom !== n ? 'drop-target' : ''}`}
              onClick={() => setFocus(n)}
              draggable={mapped}
              onDragStart={(e) => {
                if (!mapped) return
                setDragFrom(n)
                e.dataTransfer.effectAllowed = 'move'
                e.dataTransfer.setData('text/plain', String(n))
              }}
              onDragEnd={() => {
                setDragFrom(null)
                setDragOver(null)
              }}
              onDragOver={(e) => {
                if (dragFrom === null || dragFrom === n) return
                e.preventDefault()
                e.dataTransfer.dropEffect = 'move'
                setDragOver(n)
              }}
              onDragLeave={() => setDragOver((cur) => (cur === n ? null : cur))}
              onDrop={(e) => {
                e.preventDefault()
                const from = Number(e.dataTransfer.getData('text/plain')) || dragFrom
                setDragFrom(null)
                setDragOver(null)
                if (from && from !== n) void move(from, n)
              }}
            >
              <span className="pad">{n}</span>
              {renaming === n ? (
                <input
                  className="rename-field"
                  autoFocus
                  defaultValue={slot?.name ?? ''}
                  maxLength={40}
                  onClick={(e) => e.stopPropagation()}
                  onBlur={(e) => void rename(n, e.target.value)}
                  onKeyDown={(e) => {
                    e.stopPropagation()
                    if (e.key === 'Enter') void rename(n, e.currentTarget.value)
                    if (e.key === 'Escape') setRenaming(null)
                  }}
                />
              ) : (
                <span className="name">{mapped ? slot?.name : 'empty'}</span>
              )}
              {mapped && !slot?.ip && <span className="warn">no link</span>}
              {mapped && slot?.ip?.startsWith('ble:') && <span className="linkway">BLE</span>}
              {mapped && renaming !== n && (
                <span
                  className="rename"
                  title="Rename"
                  onClick={(e) => {
                    e.stopPropagation()
                    setFocus(n)
                    setRenaming(n)
                  }}
                >
                  ✎
                </span>
              )}
              {mapped && renaming !== n && (
                <span
                  className="unbind"
                  onClick={(e) => {
                    e.stopPropagation()
                    void bind(n, '')
                  }}
                >
                  ×
                </span>
              )}
            </button>
          )
        })}
      </div>
      <p className={`status ${state.discoverError || !state.hasApiKey ? 'bad' : ''}`}>{statusCopy}</p>
      <div className="device-list">
        {(state.catalog ?? []).map((d) => {
          const taken = boundIds.get(d.id)
          const takenHere = taken === focus
          return (
            <button
              key={d.id}
              type="button"
              className={`device ${takenHere ? 'current' : ''} ${taken && !takenHere ? 'taken' : ''}`}
              disabled={Boolean(taken) && !takenHere}
              onClick={() => {
                if (taken && !takenHere) {
                  setErr(`${d.name} is already on pad ${taken}`)
                  return
                }
                void bind(focus, takenHere ? '' : d.id)
              }}
            >
              <span className="d-name">{d.name || d.model || d.id}</span>
              <span className="d-meta">
                {d.model}
                {d.ip ? (d.ip.startsWith('ble:') ? ' · BLE' : ` · ${d.ip}`) : ' · no link'}
                {taken ? ` · pad ${taken}` : ''}
              </span>
            </button>
          )
        })}
      </div>
      <footer className="actions">
        <button type="button" className="ghost" onClick={() => Discover().then(onState)}>
          Rescan
        </button>
        <button type="button" className="ghost" onClick={() => ScanLAN().then(onState)}>
          Scan LAN + BLE
        </button>
        <button type="button" className="ghost" onClick={() => FillRemaining().then(onState)}>
          Fill remaining
        </button>
        <button
          type="button"
          className="primary"
          disabled={busy}
          onClick={() => {
            setBusy(true)
            setErr('')
            CommitMappings()
              .then(() => setNote('Pad map locked.'))
              .catch((e) => setErr(String(e)))
              .finally(() => setBusy(false))
          }}
        >
          Save map
        </button>
      </footer>
    </div>
  )
}

function MidiPane({
  state,
  setErr,
  setNote,
}: {
  state: HUDState
  setErr: (s: string) => void
  setNote: (s: string) => void
}) {
  const [draft, setDraft] = useState<SettingsView>(state.settings)
  useEffect(() => setDraft(state.settings), [state.settings])

  return (
    <div className="pane form-pane">
      <p className="lede">
        {state.midiConnected ? `Listening · ${state.midiPort}` : 'No MIDI port — keyboard still drives the HUD.'}
      </p>
      <BrightnessSlider value={state.brightness} label="pool dim" />
      <p className="status">CC 0–127 maps to 0–100% on every light in the active pool.</p>
      <label className="field">
        <span>Brightness CC</span>
        <input
          type="number"
          min={0}
          max={127}
          value={draft.midiCC}
          onChange={(e) => setDraft({ ...draft, midiCC: Number(e.target.value) })}
        />
      </label>
      <label className="field">
        <span>Alternate CC</span>
        <input
          type="number"
          min={0}
          max={127}
          value={draft.midiCCAlt}
          onChange={(e) => setDraft({ ...draft, midiCCAlt: Number(e.target.value) })}
        />
      </label>
      <label className="field">
        <span>Color + note</span>
        <input
          type="number"
          min={0}
          max={127}
          value={draft.midiNotePlus}
          onChange={(e) => setDraft({ ...draft, midiNotePlus: Number(e.target.value) })}
        />
      </label>
      <label className="field">
        <span>Color − note</span>
        <input
          type="number"
          min={0}
          max={127}
          value={draft.midiNoteMinus}
          onChange={(e) => setDraft({ ...draft, midiNoteMinus: Number(e.target.value) })}
        />
      </label>
      <footer className="actions">
        <button
          type="button"
          className="primary"
          onClick={() => {
            setErr('')
            SaveSettings(draft)
              .then(() => setNote('MIDI knobs saved.'))
              .catch((e) => setErr(String(e)))
          }}
        >
          Save MIDI
        </button>
      </footer>
    </div>
  )
}

function HudPane({ state }: { state: HUDState }) {
  return (
    <div className="pane form-pane">
      <p className="lede">
        The HUD never hides on its own. Press <code>Enter</code> to dismiss it, or use the Stream Deck toggle.
        Hide does not quit; launch again or <code>--toggle</code> to show.
      </p>
      <BrightnessSlider value={state.brightness} label="brightness" />
    </div>
  )
}

function AccountPane({
  state,
  onState,
  setErr,
  setNote,
}: {
  state: HUDState
  onState: (s: HUDState) => void
  setErr: (s: string) => void
  setNote: (s: string) => void
}) {
  const [key, setKey] = useState('')
  const s = state.settings
  const badge = s.hasEnvKey ? 'key in .env' : s.hasConfigKey ? 'key in config.json' : 'no key'

  return (
    <div className="pane form-pane">
      <div className={`key-badge ${s.hasApiKey ? 'ok' : 'bad'}`}>{badge}</div>
      <p className="status">.env path: {s.envPath}</p>
      <p className="status">Prefs: {s.configPath}</p>
      <p className="status">Slots: {s.mappingPath}</p>
      <p className="lede">Optional local store if you do not want a project .env. Env still wins when both exist.</p>
      <label className="field">
        <span>Store key in config.json</span>
        <input
          type="password"
          autoComplete="off"
          placeholder={s.hasConfigKey ? '••••••••' : 'Govee developer key'}
          value={key}
          onChange={(e) => setKey(e.target.value)}
        />
      </label>
      <footer className="actions">
        <button type="button" className="ghost" onClick={() => Discover().then(onState)}>
          Rescan cloud
        </button>
        <button type="button" className="ghost" onClick={() => ScanLAN().then(onState)}>
          Scan LAN + BLE
        </button>
        <button
          type="button"
          className="ghost"
          onClick={() => {
            setErr('')
            SetConfigAPIKey('')
              .then(() => {
                setKey('')
                setNote('Cleared stored key. .env still applies.')
              })
              .catch((e) => setErr(String(e)))
          }}
        >
          Clear stored
        </button>
        <button
          type="button"
          className="primary"
          disabled={!key.trim()}
          onClick={() => {
            setErr('')
            SetConfigAPIKey(key.trim())
              .then(() => {
                setKey('')
                setNote('Key stored locally.')
              })
              .catch((e) => setErr(String(e)))
          }}
        >
          Save key
        </button>
      </footer>
    </div>
  )
}
