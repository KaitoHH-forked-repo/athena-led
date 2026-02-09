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
	leftScreen   *ledScreenUnit
	rightScreen  *ledScreenUnit
	mu           sync.Mutex // 互斥锁，保护 GPIO 操作
	currentData  []byte     // 缓存当前屏幕显示的像素数据, WIDTH size slice
	currentProbs [4]float64 // 缓存当前 4 个灯的概率状态
}

func Init() (screen *LedScreen, err error) {
	stbLeft, stbRight, clk, dio, err := getGpioPin()
	if err != nil {
		fmt.Printf("getGpioPin error: %v\n", err)
		return nil, err
	}
	screen = &LedScreen{
		currentData: make([]byte, WIDTH),
	}
	leftScreen := &ledScreenUnit{
		stb: stbLeft,
		clk: clk,
		dio: dio,
	}
	err = leftScreen.initGpio()
	if err != nil {
		fmt.Printf("getGpioPin error: %v\n", err)
		return nil, err
	}
	rightScreen := &ledScreenUnit{
		stb: stbRight,
		clk: clk,
		dio: dio,
	}
	err = rightScreen.initGpio()
	if err != nil {
		fmt.Printf("getGpioPin error: %v\n", err)
		return nil, err
	}
	screen.leftScreen = leftScreen
	screen.rightScreen = rightScreen
	err = screen.SetShowModel()
	if err != nil {
		fmt.Printf("SetShowModel error: %v\n", err)
		return nil, err
	}

	err = screen.SetDataModel()
	if err != nil {
		fmt.Printf("SetDataModel error: %v\n", err)
		return nil, err
	}
	return screen, nil
}

func getGpioPin() (stbLeft, stbRight, clk, dio int, err error) {
	file, err := os.Open("/etc/openwrt_release")
	if err != nil {
		fmt.Println("Error opening file:", err)
		return 581, 582, 585, 586, nil
	}
	defer func(file *os.File) {
		file.Close()
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
	screen.Power(false, 0)
	screen.doWriteData(make([]byte, WIDTH), 0b00000000)
	for index := range fileDict {
		fileDict[index].Close()
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

// 最多可以写入 WIDTH + 1 宽度的字符串而不需要滚动。
// It return true if the text is display in flow.
func (screen *LedScreen) WriteData(str string, statusProbs [4]float64) (flow bool, takenTime time.Duration) {
	str = strings.ToUpper(str)
	data := make([]byte, 0)
	for _, item := range str {
		data = append(data, charDict[item]...)
	}
	screen.mu.Lock()
	defer screen.mu.Unlock()
	// 所有字符最后一列都是空(0b00000000)，所以静态显示时可以去掉最后一列
	if len(data) == WIDTH+1 {
		data = data[:WIDTH]
	}
	start := time.Now()
	if len(data) > WIDTH {
		screen.flow(data, statusProbs)
		return true, time.Since(start)
	} else {
		screen.writeRawData(data, statusProbs)
		return false, time.Since(start)
	}
}

func (screen *LedScreen) WriteRawData(data []byte, statusProbs [4]float64) {
	screen.mu.Lock()
	defer screen.mu.Unlock()
	screen.writeRawData(data, statusProbs)
}

func (screen *LedScreen) writeRawData(data []byte, statusProbs [4]float64) {
	paddedData := make([]byte, WIDTH)
	// 居中显示
	offset := 0
	if len(data) < WIDTH {
		offset = (27 - len(data)) / 2
	}
	copy(paddedData[offset:], data)
	screen.currentData = paddedData
	screen.currentProbs = statusProbs
	screen.flush()
}

// Refresh 用于刷新灯光状态（使用缓存的文字数据）
// 如果 probs 不为 nil, 更新状态灯信息。
// 供 main.go 中的定时器调用
func (screen *LedScreen) Refresh(probs *[4]float64) {
	screen.mu.Lock()
	defer screen.mu.Unlock()
	if probs != nil {
		screen.currentProbs = *probs
	}
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
	screen.doWriteData(screen.currentData, statusByte)
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

// 滚动显示
func (screen *LedScreen) flow(data []byte, statusProbs [4]float64) {
	screen.currentProbs = statusProbs
	start := 0
	for i := 1; i <= len(data); i++ {
		off := make([]byte, WIDTH)
		if i-WIDTH > 0 {
			start++
		}
		copy(off[:], data[start:i])
		screen.currentData = off
		screen.flush()
		time.Sleep(128 * time.Millisecond)
	}
}

// value : a slice of WIDTH bytes
func (screen *LedScreen) doWriteData(values []byte, status byte) error {
	err := screen.leftScreen.printf(values[:14])
	if err != nil {
		return err
	}
	return screen.rightScreen.printf(append(values[14:WIDTH], status))
}
