package athenaLed

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"
)

var fileDict = map[int]*os.File{}

// LedScreen 完整屏幕
type LedScreen struct {
	leftScreen  ledScreenUnit
	rightScreen ledScreenUnit
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

func (screen LedScreen) Destroy() error {
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

func (screen LedScreen) SetShowModel() error {
	err := screen.leftScreen.setShowModel()
	if err != nil {
		return err
	}
	return screen.rightScreen.setShowModel()
}

// SetDataModel 数据模式
func (screen LedScreen) SetDataModel() error {
	err := screen.leftScreen.setDataModel()
	if err != nil {
		return err
	}
	return screen.rightScreen.setDataModel()
}

// Power 显示控制、亮度开关等
func (screen LedScreen) Power(run bool, lightLevel byte) error {
	err := screen.leftScreen.power(run, lightLevel)
	if err != nil {
		return err
	}
	return screen.rightScreen.power(run, lightLevel)
}

func (screen LedScreen) WriteData(str string, status byte) {
	data := make([]byte, 0)
	for _, item := range strings.ToUpper(str) {
		data = append(data, charDict[item]...)
	}

	length := len(data)
	if length > 27 {
		screen.flow(data, status)
	} else {
		screen.static(data, status)
	}
}

// 滚动显示
func (screen LedScreen) flow(data []byte, status byte) {
	start := 0
	for i := 1; i <= len(data); i++ {
		off := [27]byte{}
		if i-27 > 0 {
			start++
		}
		copy(off[:], data[start:i])
		err := screen.doWriteData(off[:], status)
		if err != nil {
			fmt.Printf("something error: %v\n", err)
			return
		}
		time.Sleep(128 * time.Millisecond)
	}
}

// 静态显示
func (screen LedScreen) static(data []byte, status byte) {
	length := len(data)
	if length < 27 {
		paddedData := make([]byte, 27)
		offset := (27 - length) / 2
		copy(paddedData[offset:], data)
		data = paddedData
	}

	err := screen.doWriteData(data, status)
	if err != nil {
		fmt.Printf("something error: %v\n", err)
		return
	}
}

func (screen LedScreen) doWriteData(values []byte, status byte) error {
	if len(values) < 27 {
		tmp := make([]byte, 27)
		copy(tmp, values)
		values = tmp
	}

	err := screen.leftScreen.printf(values[:14])
	if err != nil {
		return err
	}
	return screen.rightScreen.printf(append(values[14:27], status))
}
