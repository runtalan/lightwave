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
	param := make([]byte, 20) // 3 header bytes + 20 = 23: two frames
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
	if !bytes.Equal(pkts[0][:7], []byte{0xA3, 0x00, 0x01, 0x02, 0x02, 0x01, 0x02}) {
		t.Fatalf("first frame = %x", pkts[0])
	}
	if pkts[1][0] != 0xA3 || pkts[1][1] != 0xFF || pkts[1][2] != 15 || pkts[1][7] != 20 || pkts[1][8] != 0 {
		t.Fatalf("last frame = %x", pkts[1])
	}
	if !bytes.Equal(pkts[2][:5], []byte{0x33, 0x05, 0x04, 0x05, 0x82}) {
		t.Fatalf("select frame = %x", pkts[2])
	}
	if one := scenePackets(Scene{Code: 1}); len(one) != 1 {
		t.Fatalf("a scene with no program is one frame, got %d", len(one))
	}
}
