export type Device = {
  id: string
  name: string
  model: string
  ip: string
  online: boolean
}

export type SlotView = {
  number: number
  deviceId: string
  name: string
  model: string
  ip: string
  active: boolean
  online: boolean
}

export type SettingsView = {
  midiCC: number
  midiCCAlt: number
  midiNotePlus: number
  midiNoteMinus: number
  idleHideSeconds: number
  hasEnvKey: boolean
  hasConfigKey: boolean
  hasApiKey: boolean
  envPath: string
  configPath: string
  mappingPath: string
}

export type HUDState = {
  slots: SlotView[]
  activePool: number[]
  brightness: number
  paletteIndex: number
  paletteName: string
  midiConnected: boolean
  midiPort: string
  deviceCount: number
  needsSetup: boolean
  setupOpen: boolean
  configOpen: boolean
  dancing: boolean
  hasApiKey: boolean
  discoverError: string
  discovering: boolean
  catalog: Device[]
  hidden: boolean
  mappingPath: string
  firstRun: boolean
  settings: SettingsView
}

export type ConfigTab = 'lights' | 'midi' | 'hud' | 'account'

export const NUMPAD_ORDER = [7, 8, 9, 4, 5, 6, 1, 2, 3] as const

export function emptySettings(): SettingsView {
  return {
    midiCC: 7,
    midiCCAlt: 1,
    midiNotePlus: 60,
    midiNoteMinus: 61,
    idleHideSeconds: 3,
    hasEnvKey: false,
    hasConfigKey: false,
    hasApiKey: false,
    envPath: '',
    configPath: '',
    mappingPath: '',
  }
}

export function clampNum(n: unknown, fallback: number, min: number, max: number): number {
  const v = typeof n === 'number' ? n : Number(n)
  if (!Number.isFinite(v)) return fallback
  return Math.min(max, Math.max(min, Math.round(v)))
}

// Shared read-only defaults for normalizeState. State objects are treated as
// immutable throughout the app, so the fallback slots/settings can be shared
// across every incoming event instead of rebuilding 9 slot objects per push.
let baseState: HUDState | null = null

export function normalizeState(raw: Partial<HUDState> | null | undefined): HUDState {
  const base = (baseState ??= emptyState())
  if (!raw) return base
  return {
    ...base,
    ...raw,
    slots: Array.isArray(raw.slots) && raw.slots.length > 0 ? raw.slots : base.slots,
    catalog: Array.isArray(raw.catalog) ? raw.catalog : [],
    activePool: Array.isArray(raw.activePool) ? raw.activePool : [],
    brightness: clampNum(raw.brightness, base.brightness, 0, 100),
    settings: raw.settings ? { ...base.settings, ...raw.settings } : base.settings,
  }
}

export function emptyState(): HUDState {
  return {
    slots: Array.from({ length: 9 }, (_, i) => ({
      number: i + 1,
      deviceId: '',
      name: 'unmapped',
      model: '',
      ip: '',
      active: false,
      online: false,
    })),
    activePool: [],
    brightness: 80,
    paletteIndex: 0,
    paletteName: 'Warm Whites',
    midiConnected: false,
    midiPort: '',
    deviceCount: 0,
    needsSetup: true,
    setupOpen: true,
    configOpen: true,
    dancing: false,
    hasApiKey: false,
    discoverError: '',
    discovering: false,
    catalog: [],
    hidden: false,
    mappingPath: '',
    firstRun: true,
    settings: emptySettings(),
  }
}
