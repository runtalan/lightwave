package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/joho/godotenv"
)

const AppName = "Lightwave"

type MIDI struct {
	CC        uint8
	CCAlt     uint8
	NotePlus  uint8
	NoteMinus uint8
}

type SlotBinding struct {
	Slot     int    `json:"slot"`
	DeviceID string `json:"deviceId"`
	Name     string `json:"name"`
	Model    string `json:"model"`
	IP       string `json:"ip"`
	// Custom is a name the user typed. Discovery refreshes Name from the
	// cloud/BLE catalog on every scan, so a rename has to be recorded
	// separately or it would be overwritten minutes later.
	Custom string `json:"custom,omitempty"`
}

// Label is the name to display: the user's rename when set, else whatever
// discovery last supplied.
func (s SlotBinding) Label() string {
	if strings.TrimSpace(s.Custom) != "" {
		return s.Custom
	}
	return s.Name
}

type SlotFile struct {
	Configured bool          `json:"configured"`
	Slots      []SlotBinding `json:"slots"`
}

type legacyMap struct {
	Slots map[string]string `json:"slots"`
}

func LoadEnv() {
	for _, p := range envSearchPaths() {
		_ = godotenv.Load(p)
	}
	persistLoadedKey()
}

func envSearchPaths() []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p == "" {
			return
		}
		p = filepath.Clean(p)
		if seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}

	add(".env")
	if cwd, err := os.Getwd(); err == nil {
		add(filepath.Join(cwd, ".env"))
	}
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		dir := filepath.Dir(exe)
		add(filepath.Join(dir, ".env"))
		add(filepath.Join(dir, "..", "Resources", ".env"))
		p := dir
		for i := 0; i < 8; i++ {
			add(filepath.Join(p, ".env"))
			parent := filepath.Dir(p)
			if parent == p {
				break
			}
			p = parent
		}
	}
	if dir, err := ConfigDir(); err == nil {
		add(filepath.Join(dir, ".env"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		add(filepath.Join(home, ".lightwave.env"))
		add(filepath.Join(home, "LightWave", ".env"))
		add(filepath.Join(home, "Lightwave", ".env"))
	}
	return out
}

func persistLoadedKey() {
	key := strings.TrimSpace(os.Getenv("GOVEE_API_KEY"))
	if key == "" {
		return
	}
	if dir, err := ConfigDir(); err == nil {
		dest := filepath.Join(dir, ".env")
		_ = os.WriteFile(dest, []byte("GOVEE_API_KEY="+key+"\n"), 0o600)
	}
	s := LoadSettings()
	if strings.TrimSpace(s.GoveeAPIKey) == "" {
		s.GoveeAPIKey = key
		_ = SaveSettings(s)
	}
}

type Settings struct {
	MidiCC          int    `json:"midiCC"`
	MidiCCAlt       int    `json:"midiCCAlt"`
	MidiNotePlus    int    `json:"midiNotePlus"`
	MidiNoteMinus   int    `json:"midiNoteMinus"`
	IdleHideSeconds int    `json:"idleHideSeconds"`
	GoveeAPIKey     string `json:"goveeApiKey,omitempty"`
	// WebEnabled starts the phone control server at launch. Off by default:
	// nothing binds a port until the user asks for it.
	WebEnabled bool `json:"webEnabled"`
	// WebAddr is the listen address, e.g. ":8787" for every interface or
	// "100.92.4.7:8787" to bind only a VPN address.
	WebAddr string `json:"webAddr"`
	// WebToken, when set, is required by every request on top of the
	// private-network restriction.
	WebToken string `json:"webToken,omitempty"`
	// Gradient is the HUD scene style: false paints every pooled lamp the
	// same colour; true spreads complementary/adjacent swatches across the pool.
	Gradient bool `json:"gradient"`
}

type settingsFile struct {
	MidiCC          *int    `json:"midiCC"`
	MidiCCAlt       *int    `json:"midiCCAlt"`
	MidiNotePlus    *int    `json:"midiNotePlus"`
	MidiNoteMinus   *int    `json:"midiNoteMinus"`
	IdleHideSeconds *int    `json:"idleHideSeconds"`
	GoveeAPIKey     *string `json:"goveeApiKey"`
	WebEnabled      *bool   `json:"webEnabled"`
	WebAddr         *string `json:"webAddr"`
	WebToken        *string `json:"webToken"`
	Gradient        *bool   `json:"gradient"`
}

func EnvAPIKey() string {
	return os.Getenv("GOVEE_API_KEY")
}

func GoveeAPIKey() string {
	if k := EnvAPIKey(); k != "" {
		return k
	}
	return LoadSettings().GoveeAPIKey
}

func MIDISettings() MIDI {
	s := LoadSettings()
	return s.MIDI()
}

func (s Settings) MIDI() MIDI {
	return MIDI{
		CC:        clampU8(s.MidiCC, 7),
		CCAlt:     clampU8(s.MidiCCAlt, 1),
		NotePlus:  clampU8(s.MidiNotePlus, 60),
		NoteMinus: clampU8(s.MidiNoteMinus, 61),
	}
}

func DefaultSettings() Settings {
	return Settings{
		MidiCC:          int(uint8Env("MIDI_CC", 7)),
		MidiCCAlt:       int(uint8Env("MIDI_CC_ALT", 1)),
		MidiNotePlus:    int(uint8Env("MIDI_NOTE_PLUS", 60)),
		MidiNoteMinus:   int(uint8Env("MIDI_NOTE_MINUS", 61)),
		IdleHideSeconds: 3,
		WebAddr:         DefaultWebAddr,
	}
}

// DefaultWebAddr binds every interface. That is safe here only because the web
// server refuses any client that is not on a private or VPN address; set a
// specific VPN address to narrow it further.
const DefaultWebAddr = ":8787"

func SettingsPath() string {
	if dir, err := ConfigDir(); err == nil {
		return filepath.Join(dir, "config.json")
	}
	return "config.json"
}

func LoadSettings() Settings {
	s := DefaultSettings()
	b, err := os.ReadFile(SettingsPath())
	if err != nil {
		return s
	}
	var raw settingsFile
	if err := json.Unmarshal(b, &raw); err != nil {
		return s
	}
	if raw.MidiCC != nil {
		s.MidiCC = *raw.MidiCC
	}
	if raw.MidiCCAlt != nil {
		s.MidiCCAlt = *raw.MidiCCAlt
	}
	if raw.MidiNotePlus != nil {
		s.MidiNotePlus = *raw.MidiNotePlus
	}
	if raw.MidiNoteMinus != nil {
		s.MidiNoteMinus = *raw.MidiNoteMinus
	}
	if raw.IdleHideSeconds != nil {
		s.IdleHideSeconds = *raw.IdleHideSeconds
	}
	if raw.GoveeAPIKey != nil {
		s.GoveeAPIKey = *raw.GoveeAPIKey
	}
	if raw.WebEnabled != nil {
		s.WebEnabled = *raw.WebEnabled
	}
	if raw.WebAddr != nil {
		s.WebAddr = *raw.WebAddr
	}
	if raw.WebToken != nil {
		s.WebToken = *raw.WebToken
	}
	if raw.Gradient != nil {
		s.Gradient = *raw.Gradient
	}
	return s
}

func SaveSettings(s Settings) error {
	s.MidiCC = int(clampU8(s.MidiCC, 7))
	s.MidiCCAlt = int(clampU8(s.MidiCCAlt, 1))
	s.MidiNotePlus = int(clampU8(s.MidiNotePlus, 60))
	s.MidiNoteMinus = int(clampU8(s.MidiNoteMinus, 61))
	if s.IdleHideSeconds < 0 {
		s.IdleHideSeconds = 0
	}
	if s.IdleHideSeconds > 120 {
		s.IdleHideSeconds = 120
	}
	s.WebAddr = strings.TrimSpace(s.WebAddr)
	if s.WebAddr == "" {
		s.WebAddr = DefaultWebAddr
	}
	s.WebToken = strings.TrimSpace(s.WebToken)
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	path := SettingsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return os.WriteFile("config.json", b, 0o600)
	}
	return os.WriteFile(path, b, 0o600)
}

// EnvFileHint names the .env file the app loaded, for display in Config.
// Computed once: env files are only read at startup (LoadEnv), so the hint
// must describe that moment anyway — and this is called from every state
// snapshot, which must not stat a dozen paths each time.
var (
	envHintOnce sync.Once
	envHint     string
)

func EnvFileHint() string {
	envHintOnce.Do(func() {
		envHint = findEnvFile()
	})
	return envHint
}

func findEnvFile() string {
	for _, p := range envSearchPaths() {
		if _, err := os.Stat(p); err == nil {
			if abs, err := filepath.Abs(p); err == nil {
				return abs
			}
			return p
		}
	}
	if dir, err := ConfigDir(); err == nil {
		return filepath.Join(dir, ".env")
	}
	return "project .env, app Resources/.env, or Application Support"
}

func clampU8(n int, fallback uint8) uint8 {
	if n < 0 || n > 127 {
		return fallback
	}
	return uint8(n)
}

func uint8Env(key string, fallback uint8) uint8 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > 127 {
		return fallback
	}
	return uint8(n)
}

// ConfigDir resolves and creates the per-user config directory once. It is on
// the path of every settings/mapping lookup — including each state snapshot —
// so it must not re-run MkdirAll per call.
var (
	configDirOnce sync.Once
	configDirPath string
	configDirErr  error
)

func ConfigDir() (string, error) {
	configDirOnce.Do(func() {
		base, err := os.UserConfigDir()
		if err != nil {
			configDirErr = err
			return
		}
		dir := filepath.Join(base, AppName)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			configDirErr = err
			return
		}
		configDirPath = dir
	})
	return configDirPath, configDirErr
}

func MappingPath() string {
	if dir, err := ConfigDir(); err == nil {
		return filepath.Join(dir, "slots.json")
	}
	return "slots.json"
}

func LoadSlotFile() SlotFile {
	paths := []string{MappingPath(), "slots.json", "devices.json"}
	if dir, err := ConfigDir(); err == nil {
		paths = append([]string{filepath.Join(dir, "devices.json")}, paths...)
	}
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var f SlotFile
		if err := json.Unmarshal(b, &f); err == nil && (f.Configured || len(f.Slots) > 0) {
			f.Slots = normalizeSlots(f.Slots)
			return f
		}
		var legacy legacyMap
		if err := json.Unmarshal(b, &legacy); err == nil && legacy.Slots != nil {
			f := SlotFile{Configured: false, Slots: make([]SlotBinding, 0, 9)}
			anyBound := false
			for n := 1; n <= 9; n++ {
				id := legacy.Slots[strconv.Itoa(n)]
				b := SlotBinding{Slot: n, DeviceID: id}
				if id != "" {
					anyBound = true
				}
				f.Slots = append(f.Slots, b)
			}
			f.Configured = anyBound
			return f
		}
	}
	return emptySlotFile()
}

func emptySlotFile() SlotFile {
	f := SlotFile{Configured: false, Slots: make([]SlotBinding, 9)}
	for i := 0; i < 9; i++ {
		f.Slots[i] = SlotBinding{Slot: i + 1}
	}
	return f
}

func normalizeSlots(in []SlotBinding) []SlotBinding {
	by := map[int]SlotBinding{}
	for _, s := range in {
		if s.Slot < 1 || s.Slot > 9 {
			continue
		}
		by[s.Slot] = s
	}
	out := make([]SlotBinding, 9)
	for i := 1; i <= 9; i++ {
		s := by[i]
		s.Slot = i
		out[i-1] = s
	}
	return out
}

func NeedsSetup(f SlotFile) bool {
	if f.Configured {
		return false
	}
	for _, s := range f.Slots {
		if s.DeviceID != "" {
			return false
		}
	}
	return true
}

func SaveSlotFile(f SlotFile) error {
	f.Slots = normalizeSlots(f.Slots)
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	path := MappingPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return os.WriteFile("slots.json", b, 0o600)
	}
	return os.WriteFile(path, b, 0o600)
}
