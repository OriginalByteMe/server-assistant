package unraid

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"server-assistant/internal/core"
)

// smartctlArgs builds the exact argument list for one smartctl invocation.
// It is its own function, deliberately: this is the surface a regression
// test asserts against directly, so dropping "-n standby" fails a test
// instead of silently shipping a build that can wake a sleeping disk.
//
// "-n standby" is the load-bearing flag: unraid-api's own resolver uses the
// identical pattern (`execa('smartctl', ['-n','standby','-A','-j', device])`,
// confirmed by decompiling its shipped bundle — see
// docs/research/unraid-state-sources.md) for exactly this reason. "-i" is
// added beyond that vendor call because core.SmartAttrs additionally needs
// device identity (ModelName, SerialHash) that "-A" alone never returns —
// confirmed live: a real "-n standby -A -j" capture against the reference
// host's disk1 has no model_name/serial_number key at all, only
// ata_smart_attributes and temperature. Adding "-i" to the same call (rather
// than a second call) keeps the standby-safety guarantee to one exec: "-n
// standby" gates the whole invocation regardless of which report sections
// are requested alongside it.
func smartctlArgs(device string) []string {
	return []string{"-n", "standby", "-i", "-A", "-j", device}
}

// standbyExitBit is smartctl's documented exit-status bit for "device open
// failed, or device is in a low-power mode" when "-n" is in effect
// (manpages.debian.org smartctl(8), EXIT STATUS; quoted in
// docs/research/unraid-state-sources.md). Passing "-n standby" specifically
// means this bit reports "asleep, skipped" rather than a generic open
// failure — smartctl has no way to distinguish the two by exit code alone,
// and the vendor's own resolver relies on exactly this bit for the same
// interpretation.
const standbyExitBit = 2

type smartctlOutput struct {
	Smartctl struct {
		ExitStatus int `json:"exit_status"`
	} `json:"smartctl"`
	ModelName          string `json:"model_name"`
	SerialNumber       string `json:"serial_number"`
	AtaSMARTAttributes struct {
		Table []smartctlAttr `json:"table"`
	} `json:"ata_smart_attributes"`
}

type smartctlAttr struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Value  int    `json:"value"`
	Worst  int    `json:"worst"`
	Thresh int    `json:"thresh"`
	Raw    struct {
		Value int64 `json:"value"`
	} `json:"raw"`
}

// runSmartctl executes smartctl for device and parses its "-n standby -i -A
// -j" output. It returns core.ErrDiskStandby (never woken, never a
// fabricated reading) when the standby exit bit is set.
func runSmartctl(ctx context.Context, smartctlPath, device string) (core.SmartAttrs, error) {
	cmd := exec.CommandContext(ctx, smartctlPath, smartctlArgs(device)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if exitErr.ExitCode()&standbyExitBit != 0 {
				return core.SmartAttrs{}, fmt.Errorf("unraid smart %s: %w", device, core.ErrDiskStandby)
			}
			return core.SmartAttrs{}, fmt.Errorf("unraid smart %s: smartctl exit %d: %s: %w", device, exitErr.ExitCode(), stderr.String(), err)
		}
		return core.SmartAttrs{}, fmt.Errorf("unraid smart %s: run smartctl: %w", device, err)
	}

	var out smartctlOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return core.SmartAttrs{}, fmt.Errorf("unraid smart %s: decode smartctl json: %w", device, err)
	}

	attrs := make([]core.SmartAttr, 0, len(out.AtaSMARTAttributes.Table))
	for _, a := range out.AtaSMARTAttributes.Table {
		attrs = append(attrs, core.SmartAttr{
			ID:        a.ID,
			Name:      a.Name,
			Value:     a.Value,
			Worst:     a.Worst,
			Threshold: a.Thresh,
			RawValue:  a.Raw.Value,
		})
	}

	return core.SmartAttrs{
		Device:      device,
		ModelName:   out.ModelName,
		SerialHash:  hashSerial(out.SerialNumber),
		Attributes:  attrs,
		CollectedAt: time.Now(),
	}, nil
}

// hashSerial returns the first 16 hex characters of the serial's SHA-256
// digest. The raw serial is discarded the moment this returns — it is never
// assigned to a struct field, logged, or otherwise retained (core.go: "never
// the raw serial: it is identifying").
func hashSerial(serial string) string {
	sum := sha256.Sum256([]byte(serial))
	return hex.EncodeToString(sum[:])[:16]
}
