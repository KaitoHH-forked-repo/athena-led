package main

import (
	athenaLed "athenaLed/internal"
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/pquerna/cachecontrol"
)

var status byte = 0b00001111

var (
	seconds      int
	lightLevel   int
	urlCacheTime int
	statusVar    string
	options      string
	value        string
	url          string
	tempFlag     string
	printStr     string
)

func main() {
	flag.IntVar(&seconds, "seconds", 5, "Led switching time (seconds)")
	flag.IntVar(&lightLevel, "lightLevel", 5, "Led light level, 0-7")
	flag.IntVar(&urlCacheTime, "urlCacheTime", 60, `The cache time for getByUrl (seconds)`)
	flag.StringVar(&statusVar, "status", "", "Space separated status types. Possible values: "+
		`time medal upload download. If set, display them after displaying content of each option`)
	flag.StringVar(&options, "option", "date timeBlink", `Space separated led options. Possible values: `+
		`date time timeBlink temp string getByUrl"`)
	flag.StringVar(&value, "value", "In God We Trust", `The "string" option: text content. `+
		`Allowed chars: all visible ASCII chars, some special unicode symbols like `+
		`♥ (heart), ☀ (sunny), ☁ (cloudy), 🌧 (rainy), ⛈ (thunderstorm), ❄ (snow), 🌫 (fog)`)
	flag.StringVar(&url, "url", "https://ipinfo.io/ip", `The "getByUrl" option: api url for get content`)
	flag.StringVar(&tempFlag, "tempFlag", "4", `The "temp" option: space separated temperature types. `+
		`possible values: 0-6. Corresponding to "/sys/class/thermal/thermal_zone%d"`)
	flag.StringVar(&printStr, "print", "", "Debug: print string graph in terminal and exit")
	flag.Parse()

	if printStr != "" {
		for _, r := range strings.ToUpper(printStr) {
			fmt.Printf("Character: %c\n", r)
			athenaLed.PrintChar(os.Stdout, r)
			fmt.Println(strings.Repeat("-", 20))
		}
		return
	}

	screen, err := athenaLed.Init()
	if err != nil {
		fmt.Printf("Init error: %v\n", err)
		return
	}
	defer func() {
		err := screen.Destroy()
		if err != nil {
			fmt.Printf("DestroyExport error: %v\n", err)
		}
	}()
	go mainLoop(screen)
	<-getExitSign()
}

func mainLoop(screen athenaLed.LedScreen) {

	var statusFlag byte = 0
	for _, item := range strings.Split(statusVar, " ") {
		switch item {
		case "time":
			statusFlag |= 1
		case "medal":
			statusFlag |= 2
		case "upload":
			statusFlag |= 4
		case "download":
			statusFlag |= 8
		}
	}

	status = statusFlag << 4 >> 4

	fmt.Println(statusVar, seconds, lightLevel, options, value, url)
	err := screen.Power(true, byte(lightLevel))
	if err != nil {
		fmt.Printf("SetPower error: %v\n", err)
		return
	}
	zoneName := getZoneName()
	timeFlag := false
	optionsArr := strings.Split(options, " ")
	urlBody := ""
	urlExpires := time.Time{}
	for {
	optionLoop:
		for _, option := range optionsArr {
			fmt.Println(option)
			switch option {
			case "date":
				formattedTime := timeFormat(zoneName, "01-02")
				screen.WriteData(formattedTime, status)
				time.Sleep(time.Duration(seconds) * time.Second)
			case "time":
				formattedTime := timeFormat(zoneName, "15:04")
				screen.WriteData(formattedTime, status)
				time.Sleep(time.Duration(seconds) * time.Second)
			case "timeBlink":
				ctx, cancel := context.WithTimeout(context.Background(), time.Duration(seconds)*time.Second)
				for {
					select {
					case <-ctx.Done():
						cancel()
						continue optionLoop
					default:
						formattedTime := timeFormat(zoneName, "15:04")
						if timeFlag {
							formattedTime = strings.ReplaceAll(formattedTime, ":", "  ")
						}
						timeFlag = !timeFlag
						screen.WriteData(formattedTime, status)
					}
					time.Sleep(1 * time.Second)
				}
			case "temp":
				tempString := getTemp(tempFlag)
				if strings.EqualFold(tempString, "") {
					continue
				}
				screen.WriteData(tempString, status)
				time.Sleep(time.Duration(seconds) * time.Second)
			case "string":
				screen.WriteData(value, status)
				time.Sleep(time.Duration(seconds) * time.Second)
			case "getByUrl":
				now := time.Now()
				if urlBody != "" && now.Before(urlExpires) {
					screen.WriteData(urlBody, status)
					time.Sleep(time.Duration(seconds) * time.Second)
					continue optionLoop
				}
				req, err := http.NewRequest(http.MethodGet, url, nil)
				if err != nil {
					fmt.Println("Error:", err)
					continue optionLoop
				}
				res, err := http.DefaultClient.Do(req)
				if err != nil {
					fmt.Println("Error:", err)
					continue optionLoop
				}
				if res.StatusCode != 200 {
					fmt.Printf("Error: status=%d\n", res.StatusCode)
					res.Body.Close()
					continue optionLoop
				}
				body, err := io.ReadAll(res.Body)
				res.Body.Close()
				if err != nil {
					fmt.Println("Error reading response body:", err)
					continue optionLoop
				}
				_, resExpires, _ := cachecontrol.CachableResponse(req, res, cachecontrol.Options{})
				if resExpires.After(now) || urlCacheTime > 0 {
					urlBody = string(body)
					if urlCacheTime > 0 {
						urlExpires = now.Add(time.Second * time.Duration(urlCacheTime))
					}
					if resExpires.After(urlExpires) {
						urlExpires = resExpires
					}
				}
				screen.WriteData(string(body), status)
				time.Sleep(time.Duration(seconds) * time.Second)
			}
		}
	}
}

func getTemp(tempFlags string) string {
	value := ""
	for i := 0; i <= 6; i++ {
		if !strings.Contains(tempFlags, strconv.Itoa(i)) {
			continue
		}

		typePath := fmt.Sprintf("/sys/class/thermal/thermal_zone%d/type", i)
		tempPath := fmt.Sprintf("/sys/class/thermal/thermal_zone%d/temp", i)

		zoneType, err := os.ReadFile(typePath)
		if err != nil {
			fmt.Printf("getTemp type from %s error: %v\n", typePath, err)
			continue
		}
		tempData, err := os.ReadFile(tempPath)
		if err != nil {
			fmt.Printf("getTemp value from %s error: %v\n", tempPath, err)
			continue
		}

		tempStr := strings.TrimSpace(string(tempData))
		tempInt, err := strconv.Atoi(tempStr)
		if err != nil {
			fmt.Printf("getTemp strconv.Atoi error: %v\n", tempStr)
			continue
		}
		value += fmt.Sprintf("%s:%.1f℃   ", strings.ReplaceAll(strings.TrimSpace(string(zoneType)), "-thermal", ""), float64(tempInt)/1000.0)
	}
	return value
}

func timeFormat(zoneName, layout string) string {
	loc, _ := time.LoadLocation(zoneName)
	currentTime := time.Now().In(loc)
	formattedTime := currentTime.Format(layout)
	return formattedTime
}

func getZoneName() string {
	zoneName := os.Getenv("TZ")
	if zoneName != "" {
		return zoneName
	}
	zoneName = "Asia/Shanghai"
	file, err := os.Open("/etc/config/system")
	if err != nil {
		fmt.Println("Error opening file:", err)
		return zoneName
	}
	defer func(file *os.File) {
		_ = file.Close()
	}(file)

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "option zonename") {
			zoneName = strings.TrimSpace(strings.Split(line, "'")[1])
			if strings.Contains(zoneName, " ") {
				zoneName = strings.ReplaceAll(zoneName, " ", "_")
			}
			continue
		}
	}
	return zoneName
}

func getExitSign() <-chan os.Signal {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit,
		syscall.SIGHUP,
		syscall.SIGINT,
		syscall.SIGQUIT,
		syscall.SIGILL,
		syscall.SIGTRAP,
		syscall.SIGABRT,
		syscall.SIGBUS,
		syscall.SIGFPE,
		syscall.SIGKILL,
		syscall.SIGSEGV,
		syscall.SIGPIPE,
		syscall.SIGALRM,
		syscall.SIGTERM)
	return quit
}
