package athenaLed

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

const (
	low  = 0x00
	high = 0x01

	// 显示模式
	command1 byte = 0b00000011
	// 数据模式
	command2 byte = 0b01000000
	// 显示地址
	command3 byte = 0b11000000
)

// 屏幕单元
type ledScreenUnit struct {
	stb int
	clk int
	dio int
}

func (screen *ledScreenUnit) initGpio() error {
	err := screen.exportAndSetPinDirection(screen.stb)
	if err != nil {
		return err
	}

	err = screen.exportAndSetPinDirection(screen.clk)
	if err != nil {
		return err
	}

	return screen.exportAndSetPinDirection(screen.dio)
}

func (screen *ledScreenUnit) exportAndSetPinDirection(pin int) error {
	err := screen.export(pin)
	if err != nil {
		return err
	}
	return screen.setPinDirection(pin)
}

func (screen *ledScreenUnit) export(pin int) error {
	filename := fmt.Sprintf("/sys/class/gpio/gpio%d", pin)
	_, err := os.Stat(filename)
	if os.IsNotExist(err) {
		cmd := exec.Command("sh", "-c", "echo "+strconv.Itoa(pin)+" > /sys/class/gpio/export")
		return cmd.Run()
	}
	return err
}

// Set the direction of a pin.
func (screen *ledScreenUnit) setPinDirection(pin int) error {
	filename := fmt.Sprintf("/sys/class/gpio/gpio%d/direction", pin)
	f, err := os.OpenFile(filename, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer func(f *os.File) {
		_ = f.Close()
	}(f)
	_, err = f.Write([]byte("out\n"))
	return err
}

func (screen *ledScreenUnit) destroyGpio() error {
	err := screen.unExport(screen.clk)
	if err != nil {
		return err
	}
	err = screen.unExport(screen.dio)
	if err != nil {
		return err
	}
	return screen.unExport(screen.stb)
}

func (screen *ledScreenUnit) unExport(pin int) error {
	filename := fmt.Sprintf("/sys/class/gpio/gpio%d", pin)
	_, err := os.Stat(filename)

	if err == nil {
		cmd := exec.Command("sh", "-c", "echo "+strconv.Itoa(pin)+" > /sys/class/gpio/unexport")
		return cmd.Run()
	}
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// setShowModel 显示模式
func (screen *ledScreenUnit) setShowModel() error {
	return screen.doWriteData(screen.stb, command1, nil)
}

// setDataModel 数据模式
func (screen *ledScreenUnit) setDataModel() error {
	return screen.doWriteData(screen.stb, command2, nil)
}

// power 显示控制、亮度开关等
func (screen *ledScreenUnit) power(run bool, lightLevel byte) error {
	// 0b1000 1111 第四位是开关 低位3位是亮度
	if run {
		command := (lightLevel<<5>>5 | 0b11111000) & 0b10001111
		return screen.doWriteData(screen.stb, command, nil)
	}
	return screen.doWriteData(screen.stb, 0b10000000, nil)
}

func (screen *ledScreenUnit) printf(values []byte) error {
	return screen.doWriteData(screen.stb, command3, values)
}

func (screen *ledScreenUnit) doWriteData(stb int, command byte, values []byte) error {
	if len(values) > 14 {
		values = values[:14]
	}
	err := screen.doWriteBit(stb, low)
	if err != nil {
		return err
	}
	err = screen.writeCommandByte(command)
	if err != nil {
		return err
	}
	for i := range values {
		value := values[i]
		err = screen.writeDataByte(value, i%2 != 0)
		if err != nil {
			return err
		}
	}
	return screen.doWriteBit(stb, high)
}

func (screen *ledScreenUnit) writeCommandByte(value byte) error {
	for i := 0; i <= 7; i++ {
		var bit = (value >> i) & 0x01
		err := screen.writeBit(bit)
		if err != nil {
			return err
		}
	}
	return nil
}

func (screen *ledScreenUnit) writeDataByte(value byte, fillData bool) error {
	for i := 0; i <= 4; i++ {
		var bit = (value >> i) & 0x01
		err := screen.writeBit(bit)
		if err != nil {
			return err
		}
	}
	if fillData {
		for i := 0; i <= 5; i++ {
			err := screen.writeBit(low)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (screen *ledScreenUnit) writeBit(bit byte) error {
	err := screen.doWriteBit(screen.clk, low)
	if err != nil {
		return err
	}
	err = screen.doWriteBit(screen.dio, bit)
	if err != nil {
		return err
	}
	return screen.doWriteBit(screen.clk, high)
}

func (screen *ledScreenUnit) doWriteBit(gpioNum int, bit byte) error {
	file := fileDict[gpioNum]
	if file == nil {
		gpioPath := fmt.Sprintf("/sys/class/gpio/gpio%d/value", gpioNum)
		fileOpen, err := os.OpenFile(gpioPath, os.O_WRONLY, 0644)
		if err != nil {
			return err
		}
		fileDict[gpioNum] = fileOpen
		file = fileOpen
	}
	var err error
	if bit == high {
		_, err = file.Write([]byte("1"))
	} else {
		_, err = file.Write([]byte("0"))
	}
	return err
}
