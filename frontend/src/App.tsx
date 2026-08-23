import { useEffect, useState } from 'react'
import { EventsOff, EventsOn } from '../wailsjs/runtime/runtime'
import { GetState, MarkUIReady } from '../wailsjs/go/main/App'
import { bindWindowActivity } from './chrome'
import { emptyState, normalizeState, type HUDState } from './types'
import { HUD } from './HUD'
import { Config } from './Config'

export default function App() {
  const [state, setState] = useState<HUDState>(() => emptyState())
  const [fading, setFading] = useState(false)

  useEffect(() => {
    let alive = true
    void MarkUIReady()
    GetState()
      .then((s) => {
        if (alive) setState(normalizeState(s))
      })
      .catch(() => undefined)
    EventsOn('state', (s: HUDState) => {
      if (alive) setState(normalizeState(s))
    })
    EventsOn('hud:fade-out', () => {
      if (alive) setFading(true)
    })
    EventsOn('hud:shown', () => {
      if (alive) setFading(false)
    })
    const unbind = bindWindowActivity()
    return () => {
      alive = false
      unbind()
      EventsOff('state')
      EventsOff('hud:fade-out')
      EventsOff('hud:shown')
    }
  }, [])

  const config = state.configOpen || state.setupOpen || state.needsSetup

  return (
    <div className={`hud-shell ${fading && !config ? 'is-fading' : ''}`}>
      <div className="scanlines" aria-hidden />
      <div className="vignette" aria-hidden />
      {config ? <Config state={state} onState={setState} /> : <HUD state={state} onState={setState} />}
    </div>
  )
}
