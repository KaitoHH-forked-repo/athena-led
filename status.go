package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type NetworkStatus struct {
	ifname   string // interface name
	txRate   float64
	rxRate   float64
	upProb   float64
	dlProb   float64
	lastRx   uint64
	lastTx   uint64
	lastTime time.Time
}

// Update 更新上传和下载速率 (Bytes per second)
func (nr *NetworkStatus) Update() error {
	// 读取当前累计值
	rxNow, err := readSysFile(nr.ifname, "rx_bytes")
	if err != nil {
		return err
	}
	txNow, err := readSysFile(nr.ifname, "tx_bytes")
	if err != nil {
		return err
	}

	now := time.Now()
	duration := now.Sub(nr.lastTime).Seconds()

	// 第一次调用或时间间隔太短，返回0并更新基准
	if duration <= 0 {
		nr.lastRx = rxNow
		nr.lastTx = txNow
		nr.lastTime = now
		return nil
	}

	// 计算差值 (处理可能的计数器溢出/重启归零情况)
	var rxDiff, txDiff uint64
	if rxNow >= nr.lastRx {
		rxDiff = rxNow - nr.lastRx
	} else {
		rxDiff = rxNow // 计数器重置了，简单处理为当前值
	}

	if txNow >= nr.lastTx {
		txDiff = txNow - nr.lastTx
	} else {
		txDiff = txNow
	}

	// 计算速率 (Bytes/s)
	nr.rxRate = float64(rxDiff) / duration
	nr.txRate = float64(txDiff) / duration

	// 更新状态
	nr.lastRx = rxNow
	nr.lastTx = txNow
	nr.lastTime = now

	// 3. 转换为概率 (使用之前的对数映射函数)
	nr.upProb = calculateProb(nr.txRate)
	nr.dlProb = calculateProb(nr.rxRate)

	return nil
}

type StatusManager struct {
	mu          sync.RWMutex
	netStatuses map[string]*NetworkStatus
	sysMonitor  *SysMonitor
	testUrl     string
	netOk       atomic.Bool
	option      *Option // current displayed option
}

func NewStatusManager(ifname string, testUrl string, profiles [][]*Option) *StatusManager {
	netStatuses := map[string]*NetworkStatus{}
	for _, options := range profiles {
		for _, opt := range options {
			if opt.Type == OPTION_UPLOAD || opt.Type == OPTION_DOWNLOAD {
				netStatuses[opt.Value] = &NetworkStatus{
					ifname:   opt.Value,
					lastTime: time.Now(),
				}
			}
		}
	}
	netStatuses[ifname] = &NetworkStatus{
		ifname:   ifname,
		lastTime: time.Now(),
	}
	return &StatusManager{
		netStatuses: netStatuses,
		sysMonitor:  &SysMonitor{},
		testUrl:     testUrl,
	}
}

func (sm *StatusManager) SetOption(option *Option) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.option = option
}

// return wan,lan4,lan3,lan2,lan1 speeds string "XXXXX", where X is:
// 'O' : -1 (disconnected); 'B': 10 Mbps (basic); 'H' : 100 Mbps (hundred); 'G' : 1000Mbps; 'S': 2500 Mbps (super).
// The order is in accordance with physical interface ports order.
func (sm *StatusManager) GetLinkSpeedsStr() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	result := ""
	for i := 4; i >= 0; i-- {
		speed := sm.sysMonitor.linkSpeeds[i]
		switch speed {
		case -1:
			result += "O"
		case 10:
			result += "B"
		case 100:
			result += "H"
		case 1000:
			result += "G"
		case 2500:
			result += "S"
		default:
			result += "O"
		}
	}
	return result
}

func (sm *StatusManager) Get(ifname string) (netOk bool, cpuUsage, memUsage, txRate, rxRate, upProb, dlProb float64) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if ns, ok := sm.netStatuses[ifname]; ok {
		return sm.netOk.Load(), sm.sysMonitor.cpuUsage, sm.sysMonitor.memUsage, ns.txRate, ns.rxRate, ns.upProb, ns.dlProb
	}
	return sm.netOk.Load(), sm.sysMonitor.cpuUsage, sm.sysMonitor.memUsage, 0, 0, 0, 0
}

// 默认每 60 秒测试一次外网连通性。如果成功则逐渐延长间隔
const CHECK_NET_MIN_INTERVAL = 60
const CHECK_NET_MAX_INTERVAL = 480

// Run goroutine
func (sm *StatusManager) Run(ctx context.Context) {
	counter := 0
	checkNetInterval := CHECK_NET_MIN_INTERVAL
	ticker := time.NewTicker(time.Second) // 1s
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if sm.testUrl != "" && counter%checkNetInterval == 0 {
				go func() {
					res, err := http.Get(sm.testUrl)
					if err != nil || res.StatusCode != 204 {
						sm.netOk.Store(false)
						checkNetInterval = CHECK_NET_MIN_INTERVAL
						return
					}
					sm.netOk.Store(true)
					checkNetInterval = min(checkNetInterval*2, CHECK_NET_MAX_INTERVAL)
				}()
			}
			sm.mu.Lock()
			if counter%5 == 0 {
				sm.sysMonitor.updateCpu()
				if sm.option != nil {
					if sm.option.Type == OPTION_MEM {
						sm.sysMonitor.updateMemory()
					}
				}
			}
			if sm.option != nil {
				if sm.option.Type == OPTION_NIC {
					sm.sysMonitor.updateLinkSpeeds()
				}
			}
			for _, ns := range sm.netStatuses {
				ns.Update()
			}
			sm.mu.Unlock()
			counter++
		}
	}
}

func readSysFile(iface, statFile string) (uint64, error) {
	path := fmt.Sprintf("/sys/class/net/%s/statistics/%s", iface, statFile)
	content, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(content)), 10, 64)
}

// calculateProb 将速率转换为亮灯概率 (0.0 - 1.0)
// 使用对数映射，以便在低流量时也能有明显的视觉反馈
// 代码逻辑解析：阈值选择 (1KB - 12MB)：下限 1KB/s：路由器即使空闲也会有一些 ARP、DHCP 或 KeepAlive 包。
// 设置 1KB 的门槛可以防止灯光在完全待机时也偶尔“抽搐”。上限 12MB/s：这是百兆宽带的极限。
// 即使你是千兆宽带，当速度达到 12MB/s 时，数据传输已经非常密集了，视觉上让灯常亮（或接近常亮）是合理的。
// 视觉效果对比：假设当前下载速度是 100 KB/s (一般的网页浏览速度)：线性映射： $100KB / 12MB \approx 0.008$。
// 效果：灯亮起的概率只有 0.8%，肉眼看起来基本是灭的，感觉像断网了。
// 对数映射：$log_{10}(1024) \approx 3.0$$log_{10}(12 \times 1024^2) \approx 7.1$$log_{10}(100 \times 1024) \approx 5.0$
// 计算：$(5.0 - 3.0) / (7.1 - 3.0) \approx 0.48$效果：灯亮起的概率约为 48%。
// 肉眼看起来是明显的快速闪烁，非常符合“正在传输数据”的直觉。
func calculateProb(rateBytesPerSec float64) float64 {
	// 定义阈值
	// 1 KB/s: 起始阈值。低于此值视为静默（为了忽略心跳包等微小流量）
	const minThreshold = 1024.0

	// 12 MB/s (~100 Mbps): 饱和阈值。高于此值视为满载（常亮）
	// 你可以根据你的宽带上限调整，例如千兆网可设为 100 * 1024 * 1024
	const maxThreshold = 12.0 * 1024.0 * 1024.0

	// 1. 低于下限，关灯
	if rateBytesPerSec <= minThreshold {
		return 0.0
	}

	// 2. 高于上限，常亮 (概率 100%)
	if rateBytesPerSec >= maxThreshold {
		return 1.0
	}

	// 3. 对数映射计算
	// 公式: P = (log(当前) - log(下限)) / (log(上限) - log(下限))
	// 使用 Log10 比较直观
	minLog := math.Log10(minThreshold)
	maxLog := math.Log10(maxThreshold)
	currLog := math.Log10(rateBytesPerSec)

	probability := (currLog - minLog) / (maxLog - minLog)

	// 4. 边界保护 (防止浮点精度误差)
	if probability < 0.0 {
		return 0.0
	}
	if probability > 1.0 {
		return 1.0
	}

	return probability
}

// GetWanInterface 获取当前负责外网流量的网络接口名。通过 "ip route show default"
func GetWanInterface() (string, error) {
	// Output 示例: "default via 192.168.1.1 dev eth0 proto static"
	out, err := exec.Command("ip", "route", "show", "default").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("execute ip route failed: %v", err)
	}

	iface := parseDevName(string(out))
	if iface == "" {
		return "", errors.New("no default interface found")
	}

	return iface, nil
}

// parseDevName 从 ip route 的输出字符串中提取 dev 后面的接口名
func parseDevName(output string) string {
	// 可能有多行，只处理第一行
	lines := strings.Split(output, "\n")
	if len(lines) == 0 {
		return ""
	}
	firstLine := lines[0]

	// 按空格分割
	fields := strings.Fields(firstLine)
	for i, field := range fields {
		// 寻找 "dev" 关键字，且确保后面还有单词
		if field == "dev" && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return ""
}

// ByteCountIEC converts bytes to IEC units (KB, MB, GB, etc.) string.
// The returned string consists of at most 3 digits, possibly 1 dot, 1 space, and 1 unit char.
// E.g. 1024 => "1.0 K"; 1024 * 75 => "75.0 K"; 1024 * 150 => "150 K".
// The max width in display is 21.
func ByteCountIEC(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	// 为了节省宽度，"1000K" 以上即换算为 M
	for n := b / unit; n >= 1000; n /= unit {
		div *= unit
		exp++
	}
	value := float64(b) / float64(div)
	if value >= 100 {
		return fmt.Sprintf("%f %c", value, "KMGTPE"[exp])
	}
	return fmt.Sprintf("%.1f %c", value, "KMGTPE"[exp])
}

// Return string max width: "1.6|2.6M" => 28
func UpDlByteCountIEC(up int64, dl int64) string {
	updl := max(up, dl)
	const unit = 1024
	div, exp := int64(unit), 0
	for n := updl / unit; n >= 100; n /= unit {
		div *= unit
		exp++
	}
	upValue := float64(up) / float64(div)
	dlValue := float64(dl) / float64(div)
	upStr := ""
	dlStr := ""
	if upValue >= 10 {
		upStr = fmt.Sprintf("%f", upValue)
	} else {
		upStr = fmt.Sprintf("%.1f", upValue)
	}
	if dlValue >= 10 {
		dlStr = fmt.Sprintf("%f", dlValue)
	} else {
		dlStr = fmt.Sprintf("%.1f", dlValue)
	}
	return fmt.Sprintf("%s  %s%c", upStr, dlStr, "KMGTPE"[exp])
}

type SysMonitor struct {
	lastTotal uint64
	lastIdle  uint64
	// 当前 CPU 整体使用率 (范围 0.0 - 1.0)
	cpuUsage float64
	memUsage float64
	// physical Network Interface Card link speeds: lan1,lan2,lan3,lan4,wan .
	// Possible values: -1 (not plugged), 10, 100, 1000, 2500.
	linkSpeeds [5]int
}

func (sm *SysMonitor) updateCpu() error {
	file, err := os.Open("/proc/stat")
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		return fmt.Errorf("failed to scan /proc/stat")
	}
	line := scanner.Text() // 读取第一行，即汇总行 "cpu ..."

	fields := strings.Fields(line)
	if len(fields) < 5 {
		return fmt.Errorf("unexpected format in /proc/stat")
	}

	// 字段索引:
	// 0: "cpu"
	// 1: user
	// 2: nice
	// 3: system
	// 4: idle  <-- 关键
	// 5: iowait
	// 6: irq
	// 7: softirq
	// 8: steal
	// ...

	var total uint64
	var idle uint64

	// 遍历所有数值字段计算 Total
	for i := 1; i < len(fields); i++ {
		val, err := strconv.ParseUint(fields[i], 10, 64)
		if err != nil {
			continue
		}
		total += val
		// 第4列 (fields[4]) 是 idle
		if i == 4 {
			idle = val
		}
	}

	// 计算差值
	totalDelta := total - sm.lastTotal
	idleDelta := idle - sm.lastIdle

	// 更新状态
	sm.lastTotal = total
	sm.lastIdle = idle

	if totalDelta == 0 {
		return nil
	}

	// 使用率 = (总时间增量 - 空闲时间增量) / 总时间增量
	sm.cpuUsage = float64(totalDelta-idleDelta) / float64(totalDelta)
	return nil
}

// physical network interfaces
var Interfaces = []string{"lan1", "lan2", "lan3", "lan4", "wan"}

func (sm *SysMonitor) updateLinkSpeeds() {
	for i, iface := range Interfaces {
		speedPath := fmt.Sprintf("/sys/class/net/%s/speed", iface)
		content, err := os.ReadFile(speedPath)
		if err != nil {
			sm.linkSpeeds[i] = -1
			continue
		}
		speed, err := strconv.Atoi(strings.TrimSpace(string(content)))
		if err != nil {
			sm.linkSpeeds[i] = -1
			continue
		}
		sm.linkSpeeds[i] = speed
	}
}

func (sm *SysMonitor) updateMemory() error {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return err
	}
	defer file.Close()

	var total, available, free, buffers, cached float64
	// We track if we found MemAvailable because older kernels don't have it
	foundAvailable := false

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)

		if len(fields) < 2 {
			continue
		}

		key := fields[0]
		// Parse the value (kB)
		val, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			continue
		}

		switch key {
		case "MemTotal:":
			total = val
		case "MemAvailable:":
			available = val
			foundAvailable = true
		case "MemFree:":
			free = val
		case "Buffers:":
			buffers = val
		case "Cached:":
			cached = val
		}
	}

	if total == 0 {
		return fmt.Errorf("could not determine total memory")
	}

	// Calculate Used Memory
	var used float64
	if foundAvailable {
		// Modern Linux (Best Accuracy)
		// Used = Total - Available
		used = total - available
	} else {
		// Fallback for older kernels (Pre 3.14)
		// Used = Total - (Free + Buffers + Cached)
		used = total - (free + buffers + cached)
	}

	sm.memUsage = used / total
	return nil
}
