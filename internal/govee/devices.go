package govee

import "strings"

type Device struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Model  string `json:"model"`
	IP     string `json:"ip"`
	Online bool   `json:"online"`
	// AdvName is the raw BLE advertised name (e.g. "ihoment_H6168_3A4B").
	// Its hex tail is the only handle macOS gives us for matching a BLE
	// peripheral to its cloud identity. Never persisted or sent to the UI.
	AdvName string `json:"-"`
}

func NormalizeID(id string) string {
	id = strings.TrimSpace(strings.ToUpper(id))
	id = strings.ReplaceAll(id, ":", "")
	id = strings.ReplaceAll(id, "-", "")
	return id
}

func Merge(cloud, lan []Device) []Device {
	byID := map[string]Device{}
	order := make([]string, 0, len(cloud)+len(lan))

	add := func(d Device) {
		id := NormalizeID(d.ID)
		if id == "" {
			return
		}
		d.ID = id
		existing, ok := byID[id]
		if !ok {
			byID[id] = d
			order = append(order, id)
			return
		}
		if d.Name != "" {
			existing.Name = d.Name
		}
		if d.Model != "" {
			existing.Model = d.Model
		}
		if d.IP != "" {
			existing.IP = d.IP
			existing.Online = true
		}
		if d.Online {
			existing.Online = true
		}
		byID[id] = existing
	}

	for _, d := range cloud {
		add(d)
	}
	for _, d := range lan {
		add(d)
	}

	out := make([]Device, 0, len(order))
	for _, id := range order {
		d := byID[id]
		if d.Name == "" {
			if d.Model != "" {
				d.Name = d.Model
			} else {
				d.Name = d.ID
			}
		}
		out = append(out, d)
	}
	return out
}
