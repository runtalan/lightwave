package govee

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// The modern openapi router returns every device on the account. The legacy
// developer-api /v1/devices endpoint silently omits newer models (e.g. H61E5),
// which left those devices with no friendly name at all.
const cloudDevicesURL = "https://openapi.api.govee.com/router/api/v1/user/devices"

type cloudResponse struct {
	Data []struct {
		Device     string `json:"device"`
		SKU        string `json:"sku"`
		DeviceName string `json:"deviceName"`
	} `json:"data"`
	Message string `json:"message"`
}

func DiscoverCloud(apiKey string) ([]Device, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("GOVEE_API_KEY is empty")
	}
	req, err := http.NewRequest(http.MethodGet, cloudDevicesURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Govee-API-Key", apiKey)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 12 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("govee cloud %s: %s", resp.Status, string(body))
	}
	var parsed cloudResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	out := make([]Device, 0, len(parsed.Data))
	for _, d := range parsed.Data {
		id := NormalizeID(d.Device)
		if id == "" {
			continue
		}
		out = append(out, Device{
			ID:    id,
			Name:  d.DeviceName,
			Model: d.SKU,
		})
	}
	return out, nil
}
