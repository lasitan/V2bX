package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/InazumaV/V2bX/common/exec"
	"github.com/spf13/cobra"
)

var (
	startCommand = cobra.Command{
		Use:   "start",
		Short: "Start V2bX service",
		Run:   startHandle,
	}
	stopCommand = cobra.Command{
		Use:   "stop",
		Short: "Stop V2bX service",
		Run:   stopHandle,
	}
	restartCommand = cobra.Command{
		Use:   "restart",
		Short: "Restart V2bX service",
		Run:   restartHandle,
	}
	logCommand = cobra.Command{
		Use:   "log",
		Short: "Output V2bX log",
		Run: func(_ *cobra.Command, _ []string) {
			exec.RunCommandStd("journalctl", "-u", "V2bX.service", "-e", "--no-pager", "-f")
		},
	}
	checkCommand = cobra.Command{
		Use:   "check",
		Short: "Check runtime upstream/downstream traffic",
		Run:   checkHandle,
	}
	dnsFlushCommand = cobra.Command{
		Use:     "dns-flush",
		Aliases: []string{"dnsflush"},
		Short:   "Flush sing-box DNS cache without restart",
		Run:     dnsFlushHandle,
	}
)

func init() {
	command.AddCommand(&startCommand)
	command.AddCommand(&stopCommand)
	command.AddCommand(&restartCommand)
	command.AddCommand(&logCommand)
	command.AddCommand(&checkCommand)
	command.AddCommand(&dnsFlushCommand)
}

func startHandle(_ *cobra.Command, _ []string) {
	r, err := checkRunning()
	if err != nil {
		fmt.Println(Err("check status error: ", err))
		fmt.Println(Err("V2bX启动失败"))
		return
	}
	if r {
		fmt.Println(Ok("V2bX已运行，无需再次启动，如需重启请选择重启"))
	}
	_, err = exec.RunCommandByShell("systemctl start V2bX.service")
	if err != nil {
		fmt.Println(Err("exec start cmd error: ", err))
		fmt.Println(Err("V2bX启动失败"))
		return
	}
	time.Sleep(time.Second * 3)
	r, err = checkRunning()
	if err != nil {
		fmt.Println(Err("check status error: ", err))
		fmt.Println(Err("V2bX启动失败"))
	}
	if !r {
		fmt.Println(Err("V2bX可能启动失败，请稍后使用 V2bX log 查看日志信息"))
		return
	}
	fmt.Println(Ok("V2bX 启动成功，请使用 V2bX log 查看运行日志"))
}

func stopHandle(_ *cobra.Command, _ []string) {
	_, err := exec.RunCommandByShell("systemctl stop V2bX.service")
	if err != nil {
		fmt.Println(Err("exec stop cmd error: ", err))
		fmt.Println(Err("V2bX停止失败"))
		return
	}
	time.Sleep(2 * time.Second)
	r, err := checkRunning()
	if err != nil {
		fmt.Println(Err("check status error:", err))
		fmt.Println(Err("V2bX停止失败"))
		return
	}
	if r {
		fmt.Println(Err("V2bX停止失败，可能是因为停止时间超过了两秒，请稍后查看日志信息"))
		return
	}
	fmt.Println(Ok("V2bX 停止成功"))
}

func restartHandle(_ *cobra.Command, _ []string) {
	_, err := exec.RunCommandByShell("systemctl restart V2bX.service")
	if err != nil {
		fmt.Println(Err("exec restart cmd error: ", err))
		fmt.Println(Err("V2bX重启失败"))
		return
	}
	r, err := checkRunning()
	if err != nil {
		fmt.Println(Err("check status error: ", err))
		fmt.Println(Err("V2bX重启失败"))
		return
	}
	if !r {
		fmt.Println(Err("V2bX可能启动失败，请稍后使用 V2bX log 查看日志信息"))
		return
	}
	fmt.Println(Ok("V2bX重启成功"))
}

type runtimeTrafficView struct {
	StartedAt int64 `json:"started_at"`
	UpdatedAt int64 `json:"updated_at"`
	Upload    int64 `json:"upload"`
	Download  int64 `json:"download"`
	ReportedUpload   int64 `json:"reported_upload"`
	ReportedDownload int64 `json:"reported_download"`
}

type pendingTrafficPayload struct {
	Items []struct {
		Upload   int64 `json:"upload"`
		Download int64 `json:"download"`
	} `json:"items"`
}

func dnsFlushHandle(_ *cobra.Command, _ []string) {
	r, err := checkRunning()
	if err != nil {
		fmt.Println(Err("check status error: ", err))
		return
	}
	if !r {
		fmt.Println(Err("V2bX 未运行，无法刷新 DNS 缓存"))
		return
	}

	since := time.Now()
	if err = sendDNSFlushSignal(); err != nil {
		fmt.Println(Err("发送刷新信号失败: ", err))
		return
	}

	result, ok := waitDNSFlushResult(dnsFlushResultPath, since, 3*time.Second)
	if !ok {
		fmt.Println(Warn("已发送刷新信号，但未收到结果，请使用 V2bX log 查看是否成功"))
		return
	}
	if result.OK {
		fmt.Println(Ok("DNS 解析缓存已强制清空（30 秒内新连接强制重新解析，服务未重启）"))
		fmt.Println(Warn("提示：新连接将重新解析；已建立的长连接仍使用旧 IP，需客户端断开后重连"))
		return
	}
	if result.Message != "" {
		fmt.Println(Err("DNS 缓存刷新失败: ", result.Message))
		return
	}
	fmt.Println(Err("DNS 缓存刷新失败"))
}

func checkHandle(_ *cobra.Command, _ []string) {
	pattern := filepath.Join("/etc/V2bX", "cache", "runtime_traffic_*.json")
	files, err := filepath.Glob(pattern)
	if err != nil || len(files) == 0 {
		fmt.Println(Err("未找到运行期流量统计文件"))
		return
	}

	var totalUp int64
	var totalDown int64
	var totalReportedUp int64
	var totalReportedDown int64
	var totalPendingUp int64
	var totalPendingDown int64
	for _, path := range files {
		raw, readErr := os.ReadFile(path)
		if readErr != nil || len(raw) == 0 {
			continue
		}
		view := runtimeTrafficView{}
		if unmarshalErr := json.Unmarshal(raw, &view); unmarshalErr != nil {
			continue
		}
		pendingUp, pendingDown := readPendingTraffic(path)
		totalUp += view.Upload
		totalDown += view.Download
		totalReportedUp += view.ReportedUpload
		totalReportedDown += view.ReportedDownload
		totalPendingUp += pendingUp
		totalPendingDown += pendingDown
		fmt.Printf("%s\n", strings.TrimSuffix(filepath.Base(path), ".json"))
		fmt.Printf("  started_at: %s\n", time.Unix(view.StartedAt, 0).Format("2006-01-02 15:04:05"))
		fmt.Printf("  updated_at: %s\n", time.Unix(view.UpdatedAt, 0).Format("2006-01-02 15:04:05"))
		fmt.Printf("  本次累计: up=%d bytes, down=%d bytes\n", view.Upload, view.Download)
		fmt.Printf("  已上报:   up=%d bytes, down=%d bytes\n", view.ReportedUpload, view.ReportedDownload)
		fmt.Printf("  待上报缓存: up=%d bytes, down=%d bytes\n", pendingUp, pendingDown)
	}

	fmt.Println("--------------------------------------------------")
	fmt.Printf("本次累计总计: up=%d bytes, down=%d bytes\n", totalUp, totalDown)
	fmt.Printf("已上报总计:   up=%d bytes, down=%d bytes\n", totalReportedUp, totalReportedDown)
	fmt.Printf("待上报缓存总计: up=%d bytes, down=%d bytes\n", totalPendingUp, totalPendingDown)
}

func readPendingTraffic(runtimePath string) (int64, int64) {
	base := filepath.Base(runtimePath)
	base = strings.TrimPrefix(base, "runtime_traffic_")
	base = strings.TrimSuffix(base, ".json")
	trafficPath := filepath.Join("/etc/V2bX", "cache", "traffic_"+base+".db")
	raw, err := os.ReadFile(trafficPath)
	if err != nil || len(raw) == 0 {
		return 0, 0
	}
	payload := pendingTrafficPayload{}
	if err = json.Unmarshal(raw, &payload); err != nil {
		return 0, 0
	}
	var up int64
	var down int64
	for _, it := range payload.Items {
		up += it.Upload
		down += it.Download
	}
	return up, down
}
