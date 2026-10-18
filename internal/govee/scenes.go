package govee

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Scene is one of the light effects Govee's app offers for a model (Cyber,
// Colorful, Aurora, ...). Code selects the effect on the lamp; Param, when
// present, is the effect's program, which the lamp must be sent first.
type Scene struct {
	Name  string `json:"name"`
	Code  int    `json:"code"`
	Param []byte `json:"param,omitempty"`
	// Icon is the address of Govee's picture for the scene, the variant
	// drawn for a dark background when there is one; "" when none is listed.
	Icon string `json:"icon,omitempty"`
}

// The scene library Govee's own app reads. It needs no account or API key,
// only an app version header.
const sceneLibraryURL = "https://app2.govee.com/appsku/v1/light-effect-libraries?sku="

type sceneLibrary struct {
	Message string `json:"message"`
	Data    struct {
		Categories []struct {
			Scenes []struct {
				SceneName    string   `json:"sceneName"`
				IconUrls     []string `json:"iconUrls"`
				LightEffects []struct {
					ScenceName  string `json:"scenceName"`
					ScenceParam string `json:"scenceParam"`
					SceneCode   int    `json:"sceneCode"`
					SpeedInfo   struct {
						SupSpeed bool   `json:"supSpeed"`
						Config   string `json:"config"`
					} `json:"speedInfo"`
				} `json:"lightEffects"`
			} `json:"scenes"`
		} `json:"categories"`
	} `json:"data"`
}

// FetchSceneLibrary downloads the raw scene library for a model. Callers
// cache the body and read it with ParseScenes.
func FetchSceneLibrary(model string) ([]byte, error) {
	model = strings.ToUpper(strings.TrimSpace(model))
	if model == "" {
		return nil, fmt.Errorf("govee scenes: no model")
	}
	req, err := http.NewRequest(http.MethodGet, sceneLibraryURL+url.QueryEscape(model), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("AppVersion", "5.6.01")
	req.Header.Set("Accept", "application/json")
	resp, err := cloudHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("govee scenes %s: %s", resp.Status, string(body))
	}
	if _, err := ParseScenes(body); err != nil {
		return nil, err
	}
	return body, nil
}

// ParseScenes reads a scene library body into scenes, in the order Govee's
// app lists them. A scene with variants (Aurora A and B) becomes one entry
// per variant.
func ParseScenes(body []byte) ([]Scene, error) {
	var lib sceneLibrary
	if err := json.Unmarshal(body, &lib); err != nil {
		return nil, fmt.Errorf("govee scenes: %w", err)
	}
	var out []Scene
	for _, cat := range lib.Data.Categories {
		for _, s := range cat.Scenes {
			for _, e := range s.LightEffects {
				name := strings.TrimSpace(s.SceneName)
				if v := strings.TrimSpace(e.ScenceName); v != "" && len(s.LightEffects) > 1 {
					name += " " + v
				}
				param, err := base64.StdEncoding.DecodeString(e.ScenceParam)
				if err != nil || name == "" || e.SceneCode < 0 || e.SceneCode > 0xFFFF {
					continue
				}
				if e.SpeedInfo.SupSpeed {
					param = slowestSpeed(param, e.SpeedInfo.Config)
				}
				out = append(out, Scene{Name: name, Code: e.SceneCode, Param: param, Icon: darkIcon(s.IconUrls)})
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("govee scenes: library is empty (%s)", lib.Message)
	}
	return out, nil
}

// darkIcon picks the icon drawn for a dark background, else the first listed.
func darkIcon(urls []string) string {
	for _, u := range urls {
		if strings.Contains(u, "_dark") {
			return u
		}
	}
	if len(urls) > 0 {
		return urls[0]
	}
	return ""
}

// maxIconBytes caps a scene icon download; Govee's are a few kilobytes.
const maxIconBytes = 1 << 20

// FetchSceneIcon downloads a scene's icon. Only a PNG is accepted, so an
// error page is never saved as a picture.
func FetchSceneIcon(u string) ([]byte, error) {
	if !strings.HasPrefix(u, "https://") {
		return nil, fmt.Errorf("govee scene icon: not an https address")
	}
	resp, err := cloudHTTP.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("govee scene icon: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxIconBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxIconBytes || !strings.HasPrefix(string(body), "\x89PNG\r\n\x1a\n") {
		return nil, fmt.Errorf("govee scene icon: not a PNG")
	}
	return body, nil
}

// speedPage is one page of a scene's speed choices: three values per field,
// slowest first, with DefaultIndex the one the program carries.
type speedPage struct {
	Page         int   `json:"page"`
	DefaultIndex int   `json:"defaultIndex"`
	MoveIn       []int `json:"moveIn"`
	Color        []int `json:"color"`
}

// speedColorOffset is where a page keeps its colour-change speed, counted from
// the page's length byte.
const speedColorOffset = 14

// slowestSpeed rewrites a scene program to Govee's slowest speed choice; the
// library's programs carry the middle one, which reads as hurried on a bar a
// foot from your face.
//
// A program is 0x0a, a page count, then each page as a length byte and that
// many bytes. Each page's movement speed is wherever its default value sits;
// its colour speed is at speedColorOffset. A program that does not parse that
// way is returned as it came, so the scene still plays at its own speed.
func slowestSpeed(param []byte, config string) []byte {
	var pages []speedPage
	if json.Unmarshal([]byte(config), &pages) != nil || len(param) < 2 || param[0] != 0x0a {
		return param
	}
	type span struct{ start, end int }
	var spans []span
	i := 2
	for n := 0; n < int(param[1]); n++ {
		if i >= len(param) {
			return param
		}
		end := i + 1 + int(param[i])
		if end > len(param) {
			return param
		}
		spans = append(spans, span{i, end})
		i = end
	}
	if i != len(param) {
		return param
	}
	out := append([]byte(nil), param...)
	for _, pg := range pages {
		if pg.Page < 0 || pg.Page >= len(spans) {
			continue
		}
		sp := spans[pg.Page]
		if len(pg.MoveIn) == 3 && pg.DefaultIndex >= 0 && pg.DefaultIndex < 3 {
			from, to := byte(pg.MoveIn[pg.DefaultIndex]), byte(pg.MoveIn[0])
			for j := sp.start + 1; j < sp.end; j++ {
				if param[j] == from {
					out[j] = to
				}
			}
		}
		if len(pg.Color) == 3 && pg.DefaultIndex >= 0 && pg.DefaultIndex < 3 {
			if j := sp.start + speedColorOffset; j < sp.end && param[j] == byte(pg.Color[pg.DefaultIndex]) {
				out[j] = byte(pg.Color[0])
			}
		}
	}
	return out
}

// scenePackets encodes a scene as the BLE frames that start it. A scene with
// a program sends it first as a multi-frame 0xA3 transfer: the first frame
// carries 01 and the frame count ahead of the program, the last is numbered
// 0xFF, and every frame holds 17 bytes. The 33 05 04 frame then selects the
// scene by its code, low byte first.
//
// Some models want a type byte (often 02) between the count and the program.
// The H2800 does not: it rejects a program carrying 02, 04 or 06 there and
// falls back to whatever it showed before, often a dark back light.
func scenePackets(sc Scene) [][]byte {
	var pkts [][]byte
	if len(sc.Param) > 0 {
		const chunk = 17
		data := append([]byte{0x01, 0x00}, sc.Param...)
		n := (len(data) + chunk - 1) / chunk
		data[1] = byte(n)
		for i := 0; i < n; i++ {
			idx := byte(i)
			if i == n-1 {
				idx = 0xFF
			}
			end := min((i+1)*chunk, len(data))
			pkts = append(pkts, blePacket(append([]byte{0xA3, idx}, data[i*chunk:end]...)))
		}
	}
	return append(pkts, blePacket([]byte{0x33, 0x05, 0x04, byte(sc.Code), byte(sc.Code >> 8)}))
}

// SendScene starts a scene on a lamp's colour LEDs.
func SendScene(ip string, sc Scene) error {
	if strings.TrimSpace(ip) == "" {
		return nil
	}
	pkts := scenePackets(sc)
	if IsBLE(ip) {
		var err error
		for _, p := range pkts {
			if e := bleSend(ip, p); err == nil {
				err = e
			}
		}
		return err
	}
	return sendPtReal(ip, pkts)
}
