import { useState } from 'react'
import { EndWindowDrag, HideWindow, PingActivity, PingMotion, Quit, SetBrightness, StartWindowDrag } from '../wailsjs/go/main/App'

declare global {
  interface Window {
    WailsInvoke?: (msg: string) => void
  }
}

export function clampBrightness(n: unknown): number {
  const v = typeof n === 'number' ? n : Number(n)
  if (!Number.isFinite(v)) return 80
  return Math.min(100, Math.max(0, Math.round(v)))
}

function isInteractive(target: EventTarget | null): boolean {
  if (!(target instanceof Element)) return false
  return Boolean(target.closest('button, input, select, textarea, a, [data-no-drag]'))
}

// beginDrag is the shared pointerdown behaviour for any draggable surface:
// left button only, and never when the press landed on something clickable.
export function beginDrag(e: React.PointerEvent): void {
  if (e.button !== 0 || isInteractive(e.target)) return
  void PingActivity()
  void StartWindowDrag()
  try {
    window.WailsInvoke?.('drag')
  } catch {
    /* native Go drag is the fallback */
  }
}

// dragSurfaceProps makes a whole region draggable by empty space. Interactive
// descendants opt out via isInteractive, so buttons/sliders keep their clicks.
export function dragSurfaceProps() {
  return {
    style: {
      ['--wails-draggable']: 'drag',
      WebkitAppRegion: 'drag',
    } as React.CSSProperties,
    onPointerDown: beginDrag,
    onPointerUp: () => void EndWindowDrag(),
  }
}

export function TitleBar() {
  return (
    <div
      className="titlebar drag"
      title="Drag to move"
      style={
        {
          ['--wails-draggable']: 'drag',
          WebkitAppRegion: 'drag',
        } as React.CSSProperties
      }
      onPointerDown={beginDrag}
      onPointerUp={() => void EndWindowDrag()}
    >
      <span className="grip" aria-hidden />
      <span className="titlebar-mark">LIGHTWAVE</span>
      <span className="grip" aria-hidden />
      <div className="titlebar-controls no-drag" data-no-drag>
        <button
          type="button"
          className="titlebar-btn no-drag"
          data-no-drag
          title="Minimize"
          aria-label="Minimize"
          onPointerDown={(e) => e.stopPropagation()}
          // HideWindow, not HideHUD: the latter ignores a hide while Config is
          // open, which left this button dead on every Config tab.
          onClick={() => void HideWindow()}
        >
          &#8211;
        </button>
        <button
          type="button"
          className="titlebar-btn titlebar-close no-drag"
          data-no-drag
          title="Quit Lightwave"
          aria-label="Quit Lightwave"
          onPointerDown={(e) => e.stopPropagation()}
          onClick={() => void Quit()}
        >
          &times;
        </button>
      </div>
    </div>
  )
}

export function BrightnessSlider({
  value,
  label = 'level',
}: {
  value: number
  label?: string
}) {
  const incoming = clampBrightness(value)
  const [live, setLive] = useState<number | null>(null)
  const shown = live ?? incoming

  function push(n: number) {
    if (!Number.isFinite(n)) return
    const next = clampBrightness(n)
    setLive(next)
    void SetBrightness(next)
  }

  return (
    <div className="meter no-drag" data-no-drag style={{ WebkitAppRegion: 'no-drag' } as React.CSSProperties}>
      <span className="meter-label">{label}</span>
      <div className="meter-track">
        <input
          className="brightness-slider"
          type="range"
          min={0}
          max={100}
          step={1}
          value={shown}
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={shown}
          aria-label="Brightness"
          // Do NOT call setPointerCapture here: a range input's thumb tracking
          // is native, and capturing the pointer interferes with it so the
          // drag stops climbing partway. stopPropagation is enough to keep the
          // window-drag surface from stealing the gesture.
          onPointerDown={(e) => e.stopPropagation()}
          onPointerUp={() => setLive(null)}
          onPointerCancel={() => setLive(null)}
          onChange={(e) => push(Number(e.target.value))}
        />
      </div>
      <span className="meter-val">{shown}%</span>
    </div>
  )
}

export function bindWindowActivity() {
  const ping = () => void PingActivity()
  // Pointer motion only feeds coarse idle bookkeeping in Go (its thresholds
  // are measured in seconds). Relaying every raw mousemove crossed the
  // JS→Go bridge ~120 times a second — a throttled ping carries the same
  // information at a fraction of the traffic and garbage.
  let lastMotion = 0
  const motion = () => {
    const now = Date.now()
    if (now - lastMotion < 250) return
    lastMotion = now
    void PingMotion()
  }
  const endDrag = () => void EndWindowDrag()
  window.addEventListener('mousemove', motion)
  window.addEventListener('pointerdown', ping)
  window.addEventListener('keydown', ping)
  window.addEventListener('pointerup', endDrag)
  window.addEventListener('pointercancel', endDrag)
  return () => {
    window.removeEventListener('mousemove', motion)
    window.removeEventListener('pointerdown', ping)
    window.removeEventListener('keydown', ping)
    window.removeEventListener('pointerup', endDrag)
    window.removeEventListener('pointercancel', endDrag)
  }
}
