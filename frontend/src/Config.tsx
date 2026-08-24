import { useEffect, useMemo, useState } from 'react'
import {
  LiveCC,
  SaveCCCalibration,
  DiscardConfig,
  MoveSlot,
  RenameSlot,
  AssignSlot,
  CloseConfig,
  CommitMappings,
  Discover,
  FillRemaining,
  PersistNow,
  SaveSettings,
  ScanLAN,
  SetConfigAPIKey,
  SetLaunchAtLogin,
  SetWebEnabled,
  SetWebConfig,
} from '../wailsjs/go/main/App'
import { NUMPAD_ORDER, type ConfigTab, type HUDState, type SettingsView } from './types'
import { slotByNumber } from './lib'
import { BrightnessSlider, TitleBar } from './chrome'

type Props = {
  state: HUDState
  onState: (s: HUDState) => void
}

const PAD_KEYS: Record<string, number> = {
  Digit1: 1, Digit2: 2, Digit3: 3, Digit4: 4, Digit5: 5, Digit6: 6, Digit7: 7, Digit8: 8, Digit9: 9,
  Numpad1: 1, Numpad2: 2, Numpad3: 3, Numpad4: 4, Numpad5: 5, Numpad6: 6, Numpad7: 7, Numpad8: 8, Numpad9: 9,
}

const TABS: { id: ConfigTab; label: string }[] = [
  { id: 'lights', label: 'Lights' },
  { id: 'midi', label: 'MIDI' },
  { id: 'hud', label: 'HUD' },
  { id: 'remote', label: 'Remote' },
  { id: 'account', label: 'Account' },
]

export function Config({ state, onState }: Props) {
  const [tab, setTab] = useState<ConfigTab>('lights')
  const [err, setErr] = useState('')
  const [note, setNote] = useState('')
  const [midiDraft, setMidiDraft] = useState<SettingsView>(state.settings)
  useEffect(() => setMidiDraft(state.settings), [state.settings])
  const [confirmExit, setConfirmExit] = useState(false)

  // Unsaved work is either a settings draft the user edited but did not save,
  // or pad edits that live only in memory until CommitMappings runs.
  const draftDirty =
    midiDraft.midiCC !== state.settings.midiCC ||
    midiDraft.midiCCAlt !== state.settings.midiCCAlt ||
    midiDraft.midiNotePlus !== state.settings.midiNotePlus ||
    midiDraft.midiNoteMinus !== state.settings.midiNoteMinus ||
    midiDraft.midiNoteRecall !== state.settings.midiNoteRecall ||
    midiDraft.midiChanCC !== state.settings.midiChanCC ||
    midiDraft.midiChanPalette !== state.settings.midiChanPalette ||
    midiDraft.midiChanRecall !== state.settings.midiChanRecall ||
    midiDraft.idleHideSeconds !== state.settings.idleHideSeconds
  const dirty = draftDirty || Boolean(state.mapDirty)

  async function discard() {
    setErr('')
    try {
      await DiscardConfig()
    } catch (e) {
      setErr(String(e))
      setConfirmExit(false)
    }
  }

  // Escape leaves without saving. With unsaved work it asks first, so a
  // stray keypress cannot throw away a half-built pad map.
  function requestExit() {
    if (dirty) {
      setConfirmExit(true)
      return
    }
    void discard()
  }

  async function persist(close: boolean) {
    setErr('')
    try {
      await SaveSettings(midiDraft)
      if (close && tab === 'lights') {
        await CommitMappings()
      } else {
        await PersistNow()
      }
      if (close) {
        await CloseConfig()
        return
      }
      setNote('Saved.')
    } catch (e) {
      setErr(String(e))
      if (close) setTab('lights')
    }
  }

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && (e.key === 's' || e.key === 'S')) {
        e.preventDefault()
        e.stopImmediatePropagation()
        // Save and go back to the HUD: Cmd-S is "commit and done", not
        // "commit and stay". persist(true) also commits the pad map.
        void persist(true)
        return
      }
      if (e.key === 'Escape') {
        e.preventDefault()
        e.stopImmediatePropagation()
        // While the prompt is up, Escape dismisses it rather than exiting:
        // the answer to "discard?" should never be given by the same key
        // that asked the question.
        if (confirmExit) {
          setConfirmExit(false)
          return
        }
        requestExit()
      }
    }
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
    // persist()/requestExit() close over midiDraft, tab and the dirty state;
    // re-bind when any of them move so Cmd-S never flushes a stale draft and
    // Escape always sees the current dirtiness. Capture phase +
    // stopImmediatePropagation keep App's HUD-side Cmd-S from also firing.
  }, [midiDraft, tab, confirmExit, dirty])

  return (
    <div className="panel config">
      <TitleBar />
      <header className="mast compact no-drag" data-no-drag>
        <div>
          <p className="eyebrow">{state.firstRun ? 'first ignition' : 'control deck'}</p>
          <h1>Config</h1>
        </div>
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
          {tab === 'lights' && (
            <LightsPane state={state} onState={onState} setErr={setErr} setNote={setNote} onFinish={() => persist(true)} />
          )}
          {tab === 'midi' && (
            <MidiPane
              state={state}
              draft={midiDraft}
              setDraft={setMidiDraft}
              setErr={setErr}
              onFinish={() => persist(true)}
            />
          )}
          {tab === 'hud' && (
            <HudPane state={state} setErr={setErr} setNote={setNote} onFinish={() => persist(true)} />
          )}
          {tab === 'remote' && <RemotePane state={state} setErr={setErr} setNote={setNote} onFinish={() => persist(true)} />}
          {tab === 'account' && <AccountPane state={state} onState={onState} setErr={setErr} setNote={setNote} onFinish={() => persist(true)} />}
        </div>
      </div>

      {(err || note) && (
        <p className={`status ${err ? 'bad' : ''}`}>{err || note}</p>
      )}

      {confirmExit && (
        <div className="confirm-veil" role="dialog" aria-modal="true" aria-labelledby="confirm-exit-q">
          <div className="confirm-box">
            <p id="confirm-exit-q">Would you like to exit without saving your changes?</p>
            <div className="actions">
              <button
                type="button"
                className="primary"
                autoFocus
                onClick={() => {
                  setConfirmExit(false)
                  void persist(true)
                }}
              >
                Save and exit
              </button>
              <button type="button" className="ghost" onClick={() => void discard()}>
                Discard
              </button>
              <button type="button" className="ghost" onClick={() => setConfirmExit(false)}>
                Keep editing
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

function LightsPane({
  state,
  onState,
  setErr,
  setNote,
  onFinish,
}: {
  state: HUDState
  onState: (s: HUDState) => void
  setErr: (s: string) => void
  setNote: (s: string) => void
  onFinish: () => Promise<void>
}) {
  const [focus, setFocus] = useState(7)
  const [busy, setBusy] = useState(false)
  const [dragFrom, setDragFrom] = useState<number | null>(null)
  // Set while a device is being dragged out of the list, so pads can light up
  // as drop targets for it as well as for a pad-to-pad move.
  const [dragDevice, setDragDevice] = useState<string | null>(null)
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
      const n = PAD_KEYS[e.code]
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

  const statusCopy = state.bluetoothDenied
    ? 'Bluetooth needed to find H6001 — enable Lightwave in System Settings → Privacy & Security → Bluetooth, then rescan.'
    : state.bluetoothOff
      ? 'Bluetooth is off. Turn it on to find H6001.'
      : !state.hasApiKey
        ? 'No Govee key yet — Account tab or .env, then rescan. BLE bulbs like H6001 still appear from a Bluetooth scan.'
        : state.discoverError && state.discoverError !== 'missing_key'
          ? `Cloud handshake failed: ${state.discoverError}`
          : state.bleScanning
            ? `${(state.catalog ?? []).length} lights. Scanning Bluetooth for H6001…`
            : state.discovering
              ? 'Sweeping the account, LAN, and Bluetooth…'
              : (state.catalog ?? []).length === 0
                ? 'No lights found. Enable LAN control or Bluetooth, then rescan.'
                : `${state.catalog.length} lights. Select a pad, then a light.`

  const statusBad = state.bluetoothDenied || state.bluetoothOff || Boolean(state.discoverError) || !state.hasApiKey

  return (
    <div className="pane lights-pane">
      <p className="lede">Bind numpad 1–9. A light can live on one pad only. Drag a bound pad onto another to move it — dropping on an occupied pad swaps the two. Click ✎ to rename.</p>

      <section className="pad-col">
        <header className="device-head">
          <h3>Pads</h3>
          <p>Your numpad, laid out the way the keys are.</p>
        </header>
        <div className="grid" role="grid" aria-label="Numpad slots">
        {NUMPAD_ORDER.map((n) => {
          const slot = slotByNumber(state, n)
          const mapped = Boolean(slot?.deviceId)
          return (
            <button
              key={n}
              type="button"
              className={`tile ${mapped ? 'ignited' : ''} ${focus === n ? 'focused' : ''} ${
                dragOver === n && (dragDevice !== null || (dragFrom !== null && dragFrom !== n)) ? 'drop-target' : ''
              }`}
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
                // A pad accepts two kinds of drag: another pad (move/swap) and
                // a device from the list (bind).
                if (dragDevice !== null) {
                  e.preventDefault()
                  e.dataTransfer.dropEffect = 'copy'
                  setDragOver(n)
                  return
                }
                if (dragFrom === null || dragFrom === n) return
                e.preventDefault()
                e.dataTransfer.dropEffect = 'move'
                setDragOver(n)
              }}
              onDragLeave={() => setDragOver((cur) => (cur === n ? null : cur))}
              onDrop={(e) => {
                e.preventDefault()
                const deviceId = e.dataTransfer.getData('application/x-lightwave-device') || dragDevice
                setDragOver(null)
                if (deviceId) {
                  setDragDevice(null)
                  setFocus(n)
                  void bind(n, deviceId)
                  return
                }
                const from = Number(e.dataTransfer.getData('text/plain')) || dragFrom
                setDragFrom(null)
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
      </section>

      <section className="device-col">
        <header className="device-head">
          <h3>Devices</h3>
          <p>These are the devices that you can map to the pads. Drag one onto a pad, or click a pad and then a device.</p>
        </header>
        <p className={`status ${statusBad ? 'bad' : ''}`}>{statusCopy}</p>
      <div className="device-list">
        {state.bluetoothDenied && (
          <p className="device-empty bad">
            Bluetooth needed to find H6001. Enable Lightwave in System Settings → Privacy & Security → Bluetooth, then tap Rescan.
          </p>
        )}
        {state.bleScanning && !state.bluetoothDenied && !state.bluetoothOff && (
          <div className="device scanning" aria-live="polite">
            <span className="d-name">Scanning BLE…</span>
            <span className="d-meta">Waiting for H6001 (ClaudiaBulb)</span>
          </div>
        )}
        {(state.catalog ?? []).map((d) => {
          const taken = boundIds.get(d.id)
          const takenHere = taken === focus
          return (
            <button
              key={d.id}
              type="button"
              className={`device ${takenHere ? 'current' : ''} ${taken && !takenHere ? 'taken' : ''}`}
              disabled={Boolean(taken) && !takenHere}
              draggable={!taken || takenHere}
              onDragStart={(e) => {
                setDragDevice(d.id)
                e.dataTransfer.effectAllowed = 'copy'
                // A distinct MIME type so a pad drop can tell a device drag
                // from a pad-to-pad move and never confuse the two.
                e.dataTransfer.setData('application/x-lightwave-device', d.id)
                e.dataTransfer.setData('text/plain', d.id)
              }}
              onDragEnd={() => setDragDevice(null)}
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
      </section>

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
              .then(() => onFinish())
              .catch((e) => setErr(String(e)))
              .finally(() => setBusy(false))
          }}
        >
          Save
        </button>
      </footer>
    </div>
  )
}

function MidiPane({
  state,
  draft,
  setDraft,
  setErr,
  onFinish,
}: {
  state: HUDState
  draft: SettingsView
  setDraft: (s: SettingsView) => void
  setErr: (s: string) => void
  onFinish: () => Promise<void>
}) {
  // Channel is per control because one controller can spread its keys, encoder
  // and fader across different channels. "Any" is the default and the safer
  // setting: a firmware remap that moves a control to another channel would
  // silently break a pinned binding.
  const chan = (key: 'midiChanCC' | 'midiChanPalette' | 'midiChanRecall', hint: string) => (
    <label className="field narrow">
      <span>Channel</span>
      <select
        value={draft[key]}
        onChange={(e) => setDraft({ ...draft, [key]: Number(e.target.value) })}
      >
        <option value={0}>Any{hint ? ` (yours: ${hint})` : ''}</option>
        {Array.from({ length: 16 }, (_, i) => i + 1).map((n) => (
          <option key={n} value={n}>{n}</option>
        ))}
      </select>
    </label>
  )

  return (
    <div className="pane form-pane">
      <p className="lede">
        {state.midiConnected ? `Listening · ${state.midiPort}` : 'No MIDI port — keyboard still drives the HUD.'}
      </p>

      <section className="group">
        <h3 className="group-title">Brightness</h3>
        <p className="group-hint">
          CC 0–127 maps to 1–100% on ignited lights. The bottom of the fader is dimmest, not off.
        </p>
        <BrightnessSlider value={state.brightness} label="pool dim" />
        <div className="field-row">
          <label className="field">
            <span>CC</span>
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
          {chan('midiChanCC', '3')}
        </div>
        <FaderCalibration state={state} setErr={setErr} />
      </section>

      <section className="group">
        <h3 className="group-title">Palette</h3>
        <p className="group-hint">
          Notes 60 (−) and 61 (+) always cycle palettes; these add your own keys on top.
        </p>
        <div className="field-row">
          <label className="field">
            <span>Colour + note</span>
            <input
              type="number"
              min={0}
              max={127}
              value={draft.midiNotePlus}
              onChange={(e) => setDraft({ ...draft, midiNotePlus: Number(e.target.value) })}
            />
          </label>
          <label className="field">
            <span>Colour − note</span>
            <input
              type="number"
              min={0}
              max={127}
              value={draft.midiNoteMinus}
              onChange={(e) => setDraft({ ...draft, midiNoteMinus: Number(e.target.value) })}
            />
          </label>
          {chan('midiChanPalette', '1')}
        </div>
      </section>

      <section className="group">
        <h3 className="group-title">Recall</h3>
        <p className="group-hint">
          Turns everything off, or brings back exactly the lights that were on last — the same
          press as the Stream Deck status key. 0 leaves it unassigned.
        </p>
        <div className="field-row">
          <label className="field">
            <span>Recall note</span>
            <input
              type="number"
              min={0}
              max={127}
              value={draft.midiNoteRecall}
              onChange={(e) => setDraft({ ...draft, midiNoteRecall: Number(e.target.value) })}
            />
          </label>
          {chan('midiChanRecall', '2')}
        </div>
      </section>

      <footer className="actions">
        <button
          type="button"
          className="primary"
          onClick={() => {
            setErr('')
            SaveSettings(draft)
              .then(() => onFinish())
              .catch((e) => setErr(String(e)))
          }}
        >
          Save
        </button>
      </footer>
    </div>
  )
}

function HudPane({
  state,
  setErr,
  setNote,
  onFinish,
}: {
  state: HUDState
  setErr: (s: string) => void
  setNote: (s: string) => void
  onFinish: () => Promise<void>
}) {
  const s = state.settings
  const [busy, setBusy] = useState(false)
  const on = s.launchAtLogin

  function toggleLogin(next: boolean) {
    setBusy(true)
    setErr('')
    setNote('')
    SetLaunchAtLogin(next)
      .then(() =>
        setNote(next ? 'Lightwave will start hidden at login.' : 'Lightwave will not start at login.'),
      )
      .catch((e) => setErr(String(e)))
      .finally(() => setBusy(false))
  }

  return (
    <div className="pane form-pane">
      {/* Startup leads the page: it is the only setting here that changes what
          the machine does on its own, and it was previously buried under a
          stray brightness slider. */}
      <section className={`group feature ${on ? 'on' : ''}`}>
        <div className="feature-head">
          <div>
            <h3 className="group-title">Start at login</h3>
            <p className="group-hint">
              Lightwave launches when you log in and stays hidden — lights, fader, and Stream Deck
              keys live straight away, with no window taking focus.
            </p>
          </div>
          <span className={`state-pill ${on ? 'ok' : ''}`}>{on ? 'On' : 'Off'}</span>
        </div>
        <footer className="actions">
          <button
            type="button"
            className={on ? 'ghost' : 'primary'}
            disabled={busy}
            onClick={() => toggleLogin(!on)}
          >
            {on ? 'Turn off' : 'Turn on'}
          </button>
        </footer>
      </section>

      <section className="group">
        <h3 className="group-title">Showing and hiding</h3>
        <p className="group-hint">
          The HUD never hides on its own — if it disappears, something dismissed it.
        </p>
        <dl className="shortcuts">
          <div>
            <dt><kbd>Enter</kbd></dt>
            <dd>Dismiss the HUD</dd>
          </div>
          <div>
            <dt><kbd>0</kbd></dt>
            <dd>All lights off, or back on</dd>
          </div>
          <div>
            <dt><kbd>1</kbd>–<kbd>9</kbd></dt>
            <dd>Toggle that pad</dd>
          </div>
          <div>
            <dt><kbd>+</kbd> <kbd>−</kbd></dt>
            <dd>Cycle the palette</dd>
          </div>
          <div>
            <dt><kbd>/</kbd></dt>
            <dd>Gradient or single colour</dd>
          </div>
          <div>
            <dt><kbd>*</kbd></dt>
            <dd>Start or stop the colour fade</dd>
          </div>
          <div>
            <dt><kbd>,</kbd></dt>
            <dd>Open this Config window</dd>
          </div>
          <div>
            <dt><kbd>.</kbd></dt>
            <dd>Quit Lightwave outright</dd>
          </div>
        </dl>
        <p className="group-hint">
          Hiding does not quit Lightwave: the lights, the fader, and the Stream Deck keys keep
          working. Launch it again to bring the window back.
        </p>
      </section>
    </div>
  )
}

// FaderCalibration measures the fader's real travel. Many controllers do not
// span the full 0-127 range -- one topping out at 117 mapped to 92%, so the
// light could never be driven to full. Recording the observed endpoints makes
// a full throw mean 100% on whatever hardware is plugged in.

function FaderCalibration({
  state,
  setErr,
}: {
  state: HUDState
  setErr: (s: string) => void
}) {
  const saved = state.settings
  const [learning, setLearning] = useState(false)
  const [lo, setLo] = useState<number | null>(null)
  const [hi, setHi] = useState<number | null>(null)
  const [live, setLive] = useState<number | null>(null)
  const [note, setNote] = useState('')

  // Poll the raw CC while learning. 60ms is fast enough to catch the ends of a
  // sweep without flooding the bridge.
  useEffect(() => {
    if (!learning) return
    let alive = true
    const id = setInterval(() => {
      LiveCC()
        .then((v) => {
          if (!alive || typeof v !== 'number' || v < 0) return
          setLive(v)
          setLo((p) => (p === null || v < p ? v : p))
          setHi((p) => (p === null || v > p ? v : p))
        })
        .catch(() => undefined)
    }, 60)
    return () => {
      alive = false
      clearInterval(id)
    }
  }, [learning])

  const ready = lo !== null && hi !== null && hi - lo >= 8

  return (
    <div className="calibration">
      <p className="lede">
        Fader range: <strong>{saved.midiCCMin}</strong>–<strong>{saved.midiCCMax}</strong>
        {saved.midiCCMax < 127 || saved.midiCCMin > 0 ? ' (calibrated)' : ' (full range)'}
      </p>
      {!state.midiConnected && <p className="lede dim">Connect a MIDI controller to calibrate.</p>}
      {learning ? (
        <>
          <p className="lede">
            Sweep the fader all the way down, then all the way up.
            {' '}Live: <strong>{live ?? '—'}</strong> · low: <strong>{lo ?? '—'}</strong> · high: <strong>{hi ?? '—'}</strong>
          </p>
          <div className="actions">
            <button
              type="button"
              className="primary"
              disabled={!ready}
              onClick={() => {
                setErr('')
                SaveCCCalibration(lo as number, hi as number)
                  .then(() => {
                    setLearning(false)
                    setNote(`Saved ${lo}–${hi}.`)
                  })
                  .catch((e) => setErr(String(e)))
              }}
            >
              {ready ? `Save ${lo}\u2013${hi}` : 'Sweep the fader\u2026'}
            </button>
            <button type="button" className="ghost" onClick={() => setLearning(false)}>
              Cancel
            </button>
          </div>
        </>
      ) : (
        <div className="actions">
          <button
            type="button"
            className="ghost"
            disabled={!state.midiConnected}
            onClick={() => {
              setErr('')
              setNote('')
              setLo(null)
              setHi(null)
              setLive(null)
              setLearning(true)
            }}
          >
            Calibrate fader
          </button>
          {(saved.midiCCMin > 0 || saved.midiCCMax < 127) && (
            <button
              type="button"
              className="ghost"
              onClick={() => {
                setErr('')
                SaveCCCalibration(0, 127)
                  .then(() => setNote('Reset to full range.'))
                  .catch((e) => setErr(String(e)))
              }}
            >
              Reset
            </button>
          )}
        </div>
      )}
      {note && <p className="lede dim">{note}</p>}
    </div>
  )
}

// RemotePane controls the phone server. It is off until switched on here, and
// the pane leads with the reachable URLs because that is the one thing the
// user needs in order to use the feature at all.
function RemotePane({
  state,
  setErr,
  setNote,
  onFinish,
}: {
  state: HUDState
  setErr: (s: string) => void
  setNote: (s: string) => void
  onFinish: () => Promise<void>
}) {
  const s = state.settings
  const [addr, setAddr] = useState(s.webAddr)
  const [token, setToken] = useState('')
  const [busy, setBusy] = useState(false)
  useEffect(() => setAddr(s.webAddr), [s.webAddr])

  function toggle(on: boolean) {
    setBusy(true)
    setErr('')
    setNote('')
    SetWebEnabled(on)
      .then(() => setNote(on ? 'Phone control on.' : 'Phone control off.'))
      .catch((e) => setErr(String(e)))
      .finally(() => setBusy(false))
  }

  return (
    <div className="pane form-pane">
      <div className={`key-badge ${s.webRunning ? 'ok' : 'bad'}`}>
        {s.webRunning ? 'serving' : 'stopped'}
      </div>
      <p className="lede">
        Serves this same HUD to a phone or tablet, for control only — no config, no pad edits,
        no API key. Only devices on a private or VPN address can connect; public addresses are
        always refused.
      </p>

      <footer className="actions">
        <button type="button" className={s.webRunning ? 'ghost' : 'primary'} disabled={busy}
          onClick={() => toggle(!s.webRunning)}>
          {s.webRunning ? 'Stop server' : 'Start server'}
        </button>
      </footer>

      {s.webRunning && (
        <>
          <p className="status">Open on your phone:</p>
          {(s.webUrls ?? []).length === 0 ? (
            <p className="status bad">No private address found — connect to your VPN or LAN.</p>
          ) : (
            (s.webUrls ?? []).map((u) => (
              <p key={u} className="status web-url">
                {u}
                {s.webHasToken ? '?token=…' : ''}
              </p>
            ))
          )}
        </>
      )}

      <label className="field">
        <span>Listen address</span>
        <input
          type="text"
          value={addr}
          placeholder=":8787"
          onChange={(e) => setAddr(e.target.value)}
        />
      </label>
      <p className="status">
        <code>:8787</code> listens on every interface. Set a VPN address like{' '}
        <code>100.92.4.7:8787</code> to bind only that one.
      </p>

      <label className="field">
        <span>{s.webHasToken ? 'Replace token' : 'Token (optional)'}</span>
        <input
          type="password"
          autoComplete="off"
          placeholder={s.webHasToken ? '••••••••' : 'blank = rely on the network'}
          value={token}
          onChange={(e) => setToken(e.target.value)}
        />
      </label>
      <p className="status">
        A token adds a second lock, for a VPN shared with people who should not reach the lights.
        Open the URL with <code>?token=…</code> once and the phone remembers it.
      </p>

      <footer className="actions">
        <button type="button" className="ghost" disabled={busy}
          onClick={() => {
            setBusy(true)
            setErr('')
            SetWebConfig(addr, '')
              .then(() => { setToken(''); setNote('Cleared token.') })
              .catch((e) => setErr(String(e)))
              .finally(() => setBusy(false))
          }}>
          Clear token
        </button>
        <button type="button" className="primary" disabled={busy}
          onClick={() => {
            setBusy(true)
            setErr('')
            SetWebConfig(addr, token.trim())
              .then(() => { setToken(''); return onFinish() })
              .catch((e) => setErr(String(e)))
              .finally(() => setBusy(false))
          }}>
          Save
        </button>
      </footer>
    </div>
  )
}

function AccountPane({
  state,
  onState,
  setErr,
  setNote,
  onFinish,
}: {
  state: HUDState
  onState: (s: HUDState) => void
  setErr: (s: string) => void
  setNote: (s: string) => void
  onFinish: () => Promise<void>
}) {
  const [key, setKey] = useState('')
  const [busy, setBusy] = useState(false)
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
          disabled={busy || !key.trim()}
          onClick={() => {
            setBusy(true)
            setErr('')
            // Save then close, with nothing between: onState() here would
            // re-render the parent mid-chain and strand onFinish(). The
            // backend's emitState already pushes the new key to App's
            // 'state' listener, so the snapshot needs no manual plumbing.
            // Clearing the field waits until after the close for the same
            // reason — disabled={!key.trim()} would flip under the promise.
            SetConfigAPIKey(key.trim())
              .then(() => onFinish())
              .then(() => setKey(''))
              .catch((e) => setErr(String(e)))
              .finally(() => setBusy(false))
          }}
        >
          Save
        </button>
      </footer>
    </div>
  )
}
