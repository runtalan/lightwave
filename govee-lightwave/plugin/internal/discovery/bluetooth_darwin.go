//go:build darwin

package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

func ScanBluetooth(ctx context.Context, found func(BluetoothDevice), status func(string)) {
	executable, err := os.Executable()
	if err != nil {
		status(err.Error())
		return
	}
	helper := filepath.Join(filepath.Dir(executable), "Govee Lightwave Discovery.app")
	if _, err := os.Stat(helper); err != nil {
		status("Bluetooth scanner is missing. Rebuild/reinstall the complete plugin bundle.")
		return
	}
	dir, err := os.MkdirTemp("", "govee-lightwave-discovery-")
	if err != nil {
		status(err.Error())
		return
	}
	defer os.RemoveAll(dir)
	report := filepath.Join(dir, "results.json")
	// LaunchServices gives this bundled helper its own permission identity.
	// Stream Deck itself has no NSBluetoothAlwaysUsageDescription.
	cmd := exec.CommandContext(ctx, "/usr/bin/open", "-g", "-n", "-W", helper, "--args", report, fmt.Sprint(os.Getpid()))
	if err := cmd.Start(); err != nil {
		status("Could not open Bluetooth scanner: " + err.Error())
		return
	}
	finished := make(chan error, 1)
	go func() { finished <- cmd.Wait() }()
	seen := map[string]BluetoothDevice{}
	lastStatus := ""
	read := func() bool {
		b, err := os.ReadFile(report)
		if err != nil || len(b) > 256*1024 {
			return false
		}
		var result struct {
			Status  string            `json:"status"`
			Devices []BluetoothDevice `json:"devices"`
			Done    bool              `json:"done"`
		}
		if json.Unmarshal(b, &result) != nil {
			return false
		}
		if result.Status != lastStatus {
			status(result.Status)
			lastStatus = result.Status
		}
		for _, d := range result.Devices {
			if d.ID == "" {
				continue
			}
			d.Transport, d.Controllable = "bluetooth", false
			if previous, ok := seen[d.ID]; !ok || previous.Name != d.Name || previous.Model != d.Model {
				seen[d.ID] = d
				found(d)
			}
		}
		return result.Done
	}
	status("Checking Bluetooth permission for Govee Lightwave Discovery…")
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			status("Bluetooth discovery timed out or stopped. Check Bluetooth permission and retry.")
			return
		case err := <-finished:
			complete := read()
			if err != nil {
				status("Bluetooth scanner could not finish: " + err.Error())
			} else if !complete {
				status("Bluetooth scanner stopped before reporting results. Check its macOS Bluetooth permission.")
			}
			return
		case <-ticker.C:
			read()
		}
	}
}
