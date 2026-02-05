package athenaLed

import (
	"bufio"
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	WIDTH       = 27
	LedTime     = 0
	LedMedal    = 1
	LedUpload   = 2
	LedDownload = 3
)

var fileDict = map[int]*os.File{}

// LedScreen 完整屏幕
type LedScreen struct {
	leftScreen   ledScreenUnit
	rightScreen  ledScreenUnit
	mu           sync.Mutex // 互斥锁，保护 GPIO 操作
	currentData  []byte     // 缓存当前屏幕显示的像素数据 (27 bytes)
	currentProbs [4]float64 // 缓存当前 4 个灯的概率状态
}

func Init() (screen LedScreen, err error) {
	stbLeft, stbRight, clk, dio, err := getGpioPin()
	if err != nil {
		fmt.Printf("getGpioPin error: %v\n", err)
		return
	}
	leftScreen := ledScreenUnit{
		stb: stbLeft,
		clk: clk,
		dio: dio,
	}
	err = leftScreen.initGpio()
	if err != nil {
		fmt.Printf("getGpioPin error: %v\n", err)
		return
	}
	rightScreen := ledScreenUnit{
		stb: stbRight,
		clk: clk,
		dio: dio,
	}
	err = rightScreen.initGpio()
	if err != nil {
		fmt.Printf("getGpioPin error: %v\n", err)
		return
	}
	screen.leftScreen = leftScreen
	screen.rightScreen = rightScreen
	err = screen.SetShowModel()
	if err != nil {
		fmt.Printf("SetShowModel error: %v\n", err)
		return
	}

	err = screen.SetDataModel()
	if err != nil {
		fmt.Printf("SetDataModel error: %v\n", err)
	}
	return
}

func getGpioPin() (stbLeft, stbRight, clk, dio int, err error) {
	file, err := os.Open("/etc/openwrt_release")
	if err != nil {
		fmt.Println("Error opening file:", err)
		return 581, 582, 585, 586, nil
	}
	defer func(file *os.File) {
		_ = file.Close()
	}(file)

	scanner := bufio.NewScanner(file)
	distribId := "LiBwrt"
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "DISTRIB_ID=") {
			if strings.Contains(line, "'") {
				distribId = strings.TrimSpace(strings.Split(line, "'")[1])
			} else {
				distribId = strings.TrimSpace(strings.Split(line, "=")[1])
			}
		}
	}
	if err = scanner.Err(); err != nil {
		fmt.Println("Error reading file:", err)
	}
	switch distribId {
	case "QWRT":
		return 501, 502, 505, 506, nil
	default:
		return 581, 582, 585, 586, nil
	}
}

func (screen *LedScreen) Destroy() error {
	screen.mu.Lock()
	defer screen.mu.Unlock()
	_ = screen.Power(false, 0)
	_ = screen.doWriteData([]byte{}, 0b00000000)
	for index := range fileDict {
		_ = fileDict[index].Close()
	}
	err := screen.leftScreen.destroyGpio()
	if err != nil {
		fmt.Println("Error leftScreen destroyGpio:", err)
	}
	err = screen.rightScreen.destroyGpio()
	if err != nil {
		fmt.Println("Error rightScreen destroyGpio:", err)
	}
	return err
}

func (screen *LedScreen) SetShowModel() error {
	err := screen.leftScreen.setShowModel()
	if err != nil {
		return err
	}
	return screen.rightScreen.setShowModel()
}

// SetDataModel 数据模式
func (screen *LedScreen) SetDataModel() error {
	err := screen.leftScreen.setDataModel()
	if err != nil {
		return err
	}
	return screen.rightScreen.setDataModel()
}

// Power 显示控制、亮度开关等
func (screen *LedScreen) Power(run bool, lightLevel byte) error {
	err := screen.leftScreen.power(run, lightLevel)
	if err != nil {
		return err
	}
	return screen.rightScreen.power(run, lightLevel)
}

func (screen *LedScreen) WriteData(str string, statusProbs [4]float64) {
	data := make([]byte, 0)
	for _, item := range []rune(str) { // 修正为 rune 遍历以支持 unicode
		if val, ok := charDict[item]; ok {
			data = append(data, val...)
		}
	}
	length := len(data)
	if length > 27 {
		// 滚动模式比较特殊，滚动时通常不建议高频刷新灯光，或者需要在滚动内部处理
		// 这里暂且简化，滚动时不缓存 data
		screen.flow(data, statusProbs)
	} else {
		// 静态显示：填充数据
		paddedData := make([]byte, 27)
		offset := (27 - length) / 2
		copy(paddedData[offset:], data)

		// 调用底层写入
		screen.WriteRawData(paddedData, statusProbs)
	}
}

// WriteRawData 修改：签名变更 + 缓存状态
func (screen *LedScreen) WriteRawData(data []byte, statusProbs [4]float64) {
	screen.mu.Lock()
	defer screen.mu.Unlock()

	// 1. 深拷贝缓存当前数据（供 Refresh 使用）
	if len(screen.currentData) != 27 {
		screen.currentData = make([]byte, 27)
	}
	// 确保输入数据长度正确
	inputLen := len(data)
	if inputLen > 27 {
		inputLen = 27
	}
	copy(screen.currentData, data[:inputLen])

	// 2. 缓存灯光概率
	screen.currentProbs = statusProbs

	// 3. 执行真正的硬件写入
	screen.flush()
}

// Refresh 仅用于刷新灯光状态（使用缓存的文字数据）
// 供 main.go 中的定时器调用
func (screen *LedScreen) Refresh() {
	screen.mu.Lock()
	defer screen.mu.Unlock()
	screen.flush()
}

// flush 是实际操作硬件的私有方法 (必须在持有锁的状态下调用)
func (screen *LedScreen) flush() {
	// 计算 Status Byte
	// 根据概率随机决定每一位是否为 1
	var statusByte byte = 0

	// Bit 0: Time
	if shouldLight(screen.currentProbs[LedTime]) {
		statusByte |= 1
	}
	// Bit 1: Medal
	if shouldLight(screen.currentProbs[LedMedal]) {
		statusByte |= 2
	}
	// Bit 2: Upload
	if shouldLight(screen.currentProbs[LedUpload]) {
		statusByte |= 4
	}
	// Bit 3: Download
	if shouldLight(screen.currentProbs[LedDownload]) {
		statusByte |= 8
	}

	// 硬件写入 (假设 doWriteData 是内部非导出方法)
	// 注意：这里直接调用 leftScreen/rightScreen 的方法
	// 确保 ledScreenUnit 的操作是原子的或者受外层锁保护
	// 假设 screen.leftScreen.printf 等方法内部没有锁，由 LedScreen 统一管理
	_ = screen.doWriteData(screen.currentData, statusByte)
}

// 辅助函数：根据概率返回 true/false
func shouldLight(prob float64) bool {
	if prob >= 1.0 {
		return true
	}
	if prob <= 0.0 {
		return false
	}
	return rand.Float64() < prob
}

func probs2Status(Probs [4]float64) byte {
	var status byte = 0
	var prob float64
	prob = rand.Float64()
	if Probs[LedTime] >= prob {
		status |= 1
	}
	prob = rand.Float64()
	if Probs[LedMedal] >= prob {
		status |= 2
	}
	prob = rand.Float64()
	if Probs[LedUpload] >= prob {
		status |= 4
	}
	prob = rand.Float64()
	if Probs[LedDownload] >= prob {
		status |= 8
	}
	return status
}

// 滚动显示
func (screen *LedScreen) flow(data []byte, statusProbs [4]float64) {
	start := 0
	for i := 1; i <= len(data); i++ {
		off := [WIDTH]byte{}
		if i-WIDTH > 0 {
			start++
		}
		copy(off[:], data[start:i])
		err := screen.doWriteData(off[:], probs2Status(statusProbs))
		if err != nil {
			fmt.Printf("something error: %v\n", err)
			return
		}
		time.Sleep(128 * time.Millisecond)
	}
}

// 静态显示
func (screen *LedScreen) static(data []byte, statusProbs [4]float64) {
	length := len(data)
	if length < WIDTH {
		paddedData := make([]byte, WIDTH)
		offset := (WIDTH - length) / 2
		copy(paddedData[offset:], data)
		data = paddedData
	}

	err := screen.doWriteData(data, probs2Status(statusProbs))
	if err != nil {
		fmt.Printf("something error: %v\n", err)
		return
	}
}

func (screen *LedScreen) doWriteData(values []byte, status byte) error {
	if len(values) < WIDTH {
		tmp := make([]byte, WIDTH)
		copy(tmp, values)
		values = tmp
	}

	err := screen.leftScreen.printf(values[:14])
	if err != nil {
		return err
	}
	return screen.rightScreen.printf(append(values[14:WIDTH], status))
}
