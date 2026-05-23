package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/InazumaV/V2bX/common/exec"
)

const dnsFlushResultPath = "/run/V2bX.dns-flush"

type dnsFlushResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
	At      int64  `json:"at"`
}

func writeDNSFlushResult(path string, err error) {
	result := dnsFlushResult{
		OK: err == nil,
		At: time.Now().Unix(),
	}
	if err != nil {
		result.Message = err.Error()
	}
	raw, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		return
	}
	_ = os.WriteFile(path, raw, 0644)
}

func waitDNSFlushResult(path string, since time.Time, timeout time.Duration) (*dnsFlushResult, bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		info, err := os.Stat(path)
		if err == nil && !info.ModTime().Before(since) {
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				return nil, false
			}
			result := dnsFlushResult{}
			if unmarshalErr := json.Unmarshal(raw, &result); unmarshalErr != nil {
				return nil, false
			}
			return &result, true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil, false
}

func getV2bXMainPID() (int, error) {
	out, err := exec.RunCommandByShell("systemctl show V2bX -p MainPID --value 2>/dev/null")
	if err == nil {
		pidText := strings.TrimSpace(out)
		if pid, parseErr := strconv.Atoi(pidText); parseErr == nil && pid > 0 {
			return pid, nil
		}
	}
	out, err = exec.RunCommandByShell("pgrep -f '[V]2bX server' | head -1")
	if err != nil {
		return 0, fmt.Errorf("V2bX process not found")
	}
	pidText := strings.TrimSpace(out)
	pid, err := strconv.Atoi(pidText)
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("invalid V2bX pid: %q", pidText)
	}
	return pid, nil
}

func sendDNSFlushSignal() error {
	pid, err := getV2bXMainPID()
	if err != nil {
		return err
	}
	return syscall.Kill(pid, syscall.SIGUSR1)
}
