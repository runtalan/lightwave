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
}

// The scene library Govee's own app reads. It needs no account or API key,
// only an app version header.
const sceneLibraryURL = "https://app2.govee.com/appsku/v1/light-effect-libraries?sku="

type sceneLibrary struct {
	Message string `json:"message"`
	Data    struct {
		Categories []struct {
			Scenes []struct {
				SceneName    string `json:"sceneName"`
				LightEffects []struct {
					ScenceName  string `json:"scenceName"`
					ScenceParam string `json:"scenceParam"`
					SceneCode   int    `json:"sceneCode"`
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
				out = append(out, Scene{Name: name, Code: e.SceneCode, Param: param})
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("govee scenes: library is empty (%s)", lib.Message)
	}
	return out, nil
}

// scenePackets encodes a scene as the BLE frames that start it. A scene with
// a program sends it first as a multi-frame 0xA3 transfer: the first frame
// carries 01, the frame count and 02 ahead of the program, the last is
// numbered 0xFF, and every frame holds 17 bytes. The 33 05 04 frame then
// selects the scene by its code, low byte first.
func scenePackets(sc Scene) [][]byte {
	var pkts [][]byte
	if len(sc.Param) > 0 {
		const chunk = 17
		data := append([]byte{0x01, 0x00, 0x02}, sc.Param...)
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
