package govee

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseScenesKeepsOrderAndVariants(t *testing.T) {
	body := []byte(`{"message":"success","data":{"categories":[
		{"scenes":[{"sceneName":"Cyber","lightEffects":[{"scenceName":"","scenceParam":"CgIo","sceneCode":33285}]}]},
		{"scenes":[
			{"sceneName":"Sunrise","lightEffects":[{"scenceName":"","scenceParam":"","sceneCode":0}]},
			{"sceneName":"Aurora","lightEffects":[
				{"scenceName":"A","scenceParam":"","sceneCode":33315},
				{"scenceName":"B","scenceParam":"","sceneCode":33373}]}]}]}}`)
	got, err := ParseScenes(body)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range got {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "Cyber,Sunrise,Aurora A,Aurora B" {
		t.Fatalf("names = %v", names)
	}
	if !bytes.Equal(got[0].Param, []byte{0x0a, 0x02, 0x28}) || got[0].Code != 33285 {
		t.Fatalf("Cyber = %+v", got[0])
	}
	if _, err := ParseScenes([]byte(`{"data":{"categories":[]}}`)); err == nil {
		t.Fatal("an empty library must be an error, or a failed fetch would cache it")
	}
}

func TestScenePacketsFrameTheProgram(t *testing.T) {
	param := make([]byte, 20) // 2 header bytes + 20 = 22: two frames
	for i := range param {
		param[i] = byte(i + 1)
	}
	pkts := scenePackets(Scene{Code: 0x8205, Param: param})
	if len(pkts) != 3 {
		t.Fatalf("got %d frames, want 2 program frames and the select", len(pkts))
	}
	for _, p := range pkts {
		if len(p) != 20 {
			t.Fatalf("frame %x is not 20 bytes", p)
		}
		var sum byte
		for _, b := range p[:19] {
			sum ^= b
		}
		if sum != p[19] {
			t.Fatalf("frame %x has a bad checksum", p)
		}
	}
	if !bytes.Equal(pkts[0][:7], []byte{0xA3, 0x00, 0x01, 0x02, 0x01, 0x02, 0x03}) {
		t.Fatalf("first frame = %x", pkts[0])
	}
	if pkts[1][0] != 0xA3 || pkts[1][1] != 0xFF || pkts[1][2] != 16 || pkts[1][6] != 20 || pkts[1][7] != 0 {
		t.Fatalf("last frame = %x", pkts[1])
	}
	if !bytes.Equal(pkts[2][:5], []byte{0x33, 0x05, 0x04, 0x05, 0x82}) {
		t.Fatalf("select frame = %x", pkts[2])
	}
	if one := scenePackets(Scene{Code: 1}); len(one) != 1 {
		t.Fatalf("a scene with no program is one frame, got %d", len(one))
	}
}

// Scenes play at Govee's slowest speed: each page's default movement and
// colour-change values become the first, slowest, choice.
func TestSlowestSpeedRewritesEachPage(t *testing.T) {
	page := func(move byte) []byte {
		p := make([]byte, 20)
		p[0] = 19                  // length byte: 19 bytes follow
		p[speedColorOffset] = 0xF9 // colour speed, default choice
		p[10] = move               // movement speed, default choice
		return p
	}
	param := append([]byte{0x0a, 0x02}, append(page(0xE6), page(0xE6)...)...)
	cfg := `[{"page":0,"defaultIndex":1,"moveIn":[220,230,240],"color":[244,249,253]},
		{"page":1,"defaultIndex":1,"moveIn":[220,230,240]}]`
	got := slowestSpeed(param, cfg)
	if got[2+10] != 220 || got[22+10] != 220 {
		t.Fatalf("movement speed = %d, %d; want 220 on both pages", got[2+10], got[22+10])
	}
	if got[2+speedColorOffset] != 244 {
		t.Fatalf("page 0 colour speed = %d, want 244", got[2+speedColorOffset])
	}
	if got[22+speedColorOffset] != 0xF9 {
		t.Fatal("page 1 has no colour choices; its byte must be left alone")
	}
	if param[2+10] != 0xE6 {
		t.Fatal("the library's own program must not be modified")
	}
	if bad := []byte{0x0a, 0x02, 50}; !bytes.Equal(slowestSpeed(bad, cfg), bad) {
		t.Fatal("a program that does not parse must be sent as it came")
	}
}
