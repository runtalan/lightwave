import type { HUDState } from './types'

export function slotByNumber(state: HUDState, n: number) {
  // Snapshots always write pads 1–9 in order; fall back to a scan if a
  // partial payload ever arrives out of sequence.
  const direct = state.slots[n - 1]
  if (direct?.number === n) return direct
  return state.slots.find((s) => s.number === n)
}
