import { useEffect, useState } from 'react'
import { EventsOff, EventsOn } from '../wailsjs/runtime/runtime'
import { GetState, MarkUIReady, PersistNow } from '../wailsjs/go/main/App'
import { bindWindowActivity } from './chrome'
import { emptyState, normalizeState, type HUDState } from './types'
import { HUD } from './HUD'
import { Config } from './Config'

export default function App() {
  const [state, setState] = useState<HUDState>(() => emptyState())
  const [fading, setFading] = useState(false)

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
        if (!config) void PersistNow()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [config])

  return (
    <div className={`hud-shell ${fading && !config ? 'is-fading' : ''}`}>
      <div className="scanlines" aria-hidden />
      <div className="vignette" aria-hidden />
      {config ? <Config state={state} onState={setState} /> : <HUD state={state} onState={setState} />}
    </div>
  )
}
