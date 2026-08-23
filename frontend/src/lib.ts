import type { HUDState } from './types'

export function slotByNumber(state: HUDState, n: number) {
  return state.slots.find((s) => s.number === n)
}
