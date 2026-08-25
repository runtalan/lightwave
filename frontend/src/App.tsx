import { lazy, Suspense, useEffect, useState } from 'react'
import { EventsOff, EventsOn } from '../wailsjs/runtime/runtime'
import { GetState, MarkUIReady, PersistNow } from '../wailsjs/go/main/App'
import { bindWindowActivity, dragSurfaceProps } from './chrome'
import { emptyState, normalizeState, type HUDState } from './types'
import { HUD } from './HUD'

// Config is a large form surface the HUD never needs. Phone clients never
// open it (webState forces the flags off). Loading it on demand keeps the
// first paint to the control deck.
const Config = lazy(() => import('./Config'))

export default function App() {
  const [state, setState] = useState<HUDState>(() => emptyState())
  const [fading, setFading] = useState(false)
  const [saveToast, setSaveToast] = useState('')

  useEffect(() => {
    let alive = true
    let unbind = () => {}
    try {
      void MarkUIReady()
    } catch {
      /* Wails runtime not injected yet */
    }
    try {
      GetState()
        .then((s) => {
          if (alive) setState(normalizeState(s))
        })
        .catch(() => undefined)
    } catch {
      /* same: first paint must not throw */
    }
    try {
      EventsOn('state', (s: HUDState) => {
        if (alive) setState(normalizeState(s))
      })
      EventsOn('hud:fade-out', () => {
        if (alive) setFading(true)
      })
      EventsOn('hud:shown', () => {
        if (alive) setFading(false)
      })
      unbind = bindWindowActivity()
    } catch {
      /* listeners are best-effort; the first GetState is enough to render */
    }
    return () => {
      alive = false
      unbind()
      try {
        EventsOff('state')
        EventsOff('hud:fade-out')
        EventsOff('hud:shown')
      } catch {
        /* runtime already gone */
      }
    }
  }, [])

  const config = Boolean(state.configOpen || state.setupOpen || state.needsSetup)

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && (e.key === 's' || e.key === 'S')) {
        // Stop WebKit/Wails from offering "Save Page" as HTML.
        e.preventDefault()
        // Config owns its own Cmd-S: it has a settings draft to flush first.
        if (config) return
        PersistNow().then(
          () => setSaveToast('Saved.'),
          (err) => setSaveToast(`Save failed: ${String(err)}`),
        )
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [config])

  // A save with no visible result is indistinguishable from a dead shortcut.
  useEffect(() => {
    if (!saveToast) return
    const t = setTimeout(() => setSaveToast(''), 1400)
    return () => clearTimeout(t)
  }, [saveToast])

  return (
    <div className={`hud-shell ${fading && !config ? 'is-fading' : ''}`} {...dragSurfaceProps()}>
      {config ? (
        <Suspense fallback={<div className="panel config" />}>
          <Config state={state} onState={setState} />
        </Suspense>
      ) : (
        <HUD state={state} onState={setState} />
      )}
      {!config && saveToast && (
        <p className={`save-toast ${saveToast.startsWith('Save failed') ? 'bad' : ''}`} role="status">
          {saveToast}
        </p>
      )}
    </div>
  )
}
