package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/pquerna/cachecontrol"

	athenaLed "athenaLed/internal"
)

const Version = "v0.1.3-dev"

const (
	OPTION_DATE       = "date"
	OPTION_TIME       = "time"
	OPTION_TIME_BLINK = "timeBlink" // reserved for compatibility
	OPTION_TEMP       = "temp"
	OPTION_TEXT       = "text"
	OPTION_STRING     = "string"
	OPTION_CPU        = "cpu"      // cpu usage
	OPTION_UPLOAD     = "upload"   // network upload speed
	OPTION_DOWNLOAD   = "download" // network download speed
	OPTION_DINO       = "dino"
	OPTION_URL        = "url"
	OPTION_GET_BY_URL = "getByUrl" // reserved for compatibility

	HELP_OPTION = `Space separated led options. Possible values: ` + OPTION_DATE + ", " + OPTION_TIME +
		" (" + OPTION_TIME_BLINK + ")" + ", " + OPTION_TEXT + " (" + OPTION_STRING + ")" + ", " +
		OPTION_DINO + ", " + OPTION_TEMP + ", " + OPTION_CPU + ", " + OPTION_UPLOAD + ", " + OPTION_DOWNLOAD + ", " +
		OPTION_URL + " (" + OPTION_GET_BY_URL + "). " +
		`Use ":value" format suffix to set optional option value (replace space with _), ` +
		`values of each type option have different meanings: "` + OPTION_DATE + `", "` + OPTION_TIME + `", ` +
		OPTION_TIME_BLINK + `": Go time format layout, e.g. "` + DEFAULT_DATE_FORMAT + `" or "` + DEFAULT_TIME_FORMAT +
		`"; "` + OPTION_TEMP + `": temperature type digits string; "` + OPTION_TEXT + `": text contents; "` + OPTION_URL +
		`": the http(s):// url; "` + OPTION_UPLOAD + `", "` + OPTION_DOWNLOAD + `": network interface name` +
		`Use "#5" format suffix to set led switching time (duration seconds). ` +
		`E.g. "string:I_have_a_dream", "url:https://ipinfo.io/json#5"`
	DEFAULT_OPTION      = OPTION_DATE + " " + OPTION_TIME_BLINK
	DEFAULT_TIME_FORMAT = "15:04:05" // 28 width
	DEFAULT_DATE_FORMAT = "01-02"
	DEFAULT_IFNAME      = "wan"
)

type Option struct {
	Type     string
	Value    string
	Duration int
}

type ContentCache struct {
	Data    string
	Expires time.Time
}

var (
	OneShot      bool
	Seconds      int
	LightLevel   int
	UrlCacheTime int
	StatusVar    string
	OptionFlag   string
	Text         string
	Url          string
	TempFlag     string
	PrintStr     string
	Ifname       string
	Status       byte
	Options      []*Option
	Location     *time.Location
	Sm           *StatusManager

	// key: option index
	Cache = map[int]ContentCache{}
)

func main() {
	fmt.Printf("athena-led %s\n", Version)

	flag.BoolVar(&OneShot, "oneShot", false, "Display once and exit")
	flag.IntVar(&Seconds, "seconds", 5, "Default led switching time (seconds)")
	flag.IntVar(&LightLevel, "lightLevel", 5, "Led light level, 0-7")
	flag.IntVar(&UrlCacheTime, "urlCacheTime", 60, `The min cache time for "`+OPTION_URL+`" option (seconds). `+
		`Negative or zero value means no minimal cache time. It respects the url "Cache-Control" response header`)
	flag.StringVar(&StatusVar, "status", "", "Space separated light-on side led list. Force light on these led. "+
		`All Side led list (two each side, from top to bottom, left to right side): time medal upload download`)
	flag.StringVar(&OptionFlag, "option", DEFAULT_OPTION, HELP_OPTION)
	flag.StringVar(&Text, "value", "In God We Trust", `The "`+OPTION_TEXT+`" option: default text contents. `+
		`Allowed chars: all visible ASCII chars, some special unicode symbols like `+
		`♥ (heart), ☀ (sunny), ☾ (moon), ☁ (cloudy), 🌧 (rainy), ⛈ (thunderstorm), ❄ (snow), 🌫 (fog), `+
		`←, →, ↑, ↓, ↗, ↘, ✓, ✗`)
	flag.StringVar(&Url, "url", "https://ipinfo.io/ip", `The "`+OPTION_URL+`" option: default http(s):// url`)
	flag.StringVar(&TempFlag, "tempFlag", "4", `The "`+OPTION_TEMP+`" option: temperature type digits string. `+
		`Possible digits: 0-6. Corresponding to "/sys/class/thermal/thermal_zone[0-6]". `+
		`0: nss-top; 1: nss; 2: wcss-phya0; 3: wcss-phya1; 4: cpu; 5: lpass; 6: ddrss. `+
		`E.g. "124" will display temperatures of thermal_zone 1, 2 and 4 in order`)
	flag.StringVar(&Ifname, "ifname", "", `Default network interface name. `+
		`If not set, it detects internet outlet interface automatically, fallbacks to "`+DEFAULT_IFNAME+`" if failed`)
	flag.StringVar(&PrintStr, "print", "", "Debug: print string character graphes in terminal and exit")
	flag.Parse()

	if Ifname == "" {
		Ifname, _ = GetWanInterface()
		if Ifname == "" {
			Ifname = DEFAULT_IFNAME
		}
	}

	if PrintStr != "" {
		for _, r := range strings.ToUpper(PrintStr) {
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

	for _, item := range strings.Split(StatusVar, " ") {
		item = strings.TrimSpace(item)
		switch item {
		case "time":
			Status |= 1
		case "medal":
			Status |= 2
		case "upload":
			Status |= 4
		case "download":
			Status |= 8
		}
	}
	for _, optionStr := range strings.Split(OptionFlag, " ") {
		optionStr = strings.TrimSpace(optionStr)
		if optionStr == "" {
			continue
		}
		Options = append(Options, parseOption(optionStr))
	}
	err = screen.Power(true, byte(LightLevel))
	if err != nil {
		fmt.Printf("SetPower error: %v\n", err)
		return
	}
	Location, _ = time.LoadLocation(getZoneName()) // is this necessary?
	if Location == nil {
		Location = time.Local
	}
	fmt.Printf("tz=%s, status=%08b, seconds=%d, lightLevel=%d, text=%s, url=%s, option (%d): %s\n",
		Location.String(), Status, Seconds, LightLevel, Text, Url, len(Options), OptionFlag)
	Sm = NewStatusManager(Ifname, Options)

	// 信号处理设置
	reloadCh := make(chan os.Signal, 1)
	signal.Notify(reloadCh, syscall.SIGHUP)

	exitCh := make(chan os.Signal, 1)
	signal.Notify(exitCh,
		syscall.SIGINT,
		syscall.SIGQUIT,
		syscall.SIGILL,
		syscall.SIGTRAP,
		syscall.SIGABRT,
		syscall.SIGBUS,
		syscall.SIGFPE,
		syscall.SIGSEGV,
		syscall.SIGPIPE,
		syscall.SIGALRM,
		syscall.SIGTERM)

	if !OneShot {
		go func() {
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			for range ticker.C {
				status := getStatus()
				screen.Refresh(&status)
			}
		}()
	}

	// 主控制循环
	for {
		// 创建一个带取消功能的 context
		ctx, cancel := context.WithCancel(context.Background())

		// 使用 WaitGroup 确保 mainLoop 完全退出后再重启，避免硬件竞争
		var wg sync.WaitGroup
		wg.Add(1)

		// 新增：用于通知主线程 mainLoop 已经自然退出的通道
		loopDone := make(chan struct{})

		go func() {
			defer wg.Done()
			defer close(loopDone) // 任务结束时关闭通道
			Sm.Run(ctx)
			mainLoop(ctx, screen)
		}()

		// 等待信号 或 任务完成
		select {
		case <-reloadCh:
			fmt.Println("Received SIGHUP. Refreshing display...")
			cancel() // 通知 mainLoop 停止
			// 注意：这里不需要 <-loopDone，因为 cancel 会导致 mainLoop 退出，随后 wg.Wait() 会处理同步
		case <-exitCh:
			os.Exit(1)
		case <-loopDone:
			// 如果 mainLoop 自己执行完了（比如 oneShot），会走到这里
			cancel() // 释放 context 资源
		}

		// 确保 goroutine 彻底结束后再进行下一步
		wg.Wait()

		// 如果是 oneShot 模式，任务执行完就退出程序
		if OneShot {
			return
		}
	}
}

// 辅助函数：支持 Context 取消的 Sleep.
// It blocks and returns true if time over; Return false if it returns because of ctx is done
func sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func mainLoop(ctx context.Context, screen *athenaLed.LedScreen) {
	timeFlag := false
	for {
	optionLoop:
		for i, option := range Options {
			// 在每个操作开始前检查 context 是否已取消
			if ctx.Err() != nil {
				return
			}
			returnAfterFinish := OneShot && i == len(Options)-1
			fmt.Printf("option: %v\n", option)
			switch option.Type {
			case OPTION_DATE:
				formattedTime := time.Now().In(Location).Format(option.Value)
				screen.WriteData(formattedTime, getStatus())
				if returnAfterFinish {
					return
				}
				if !sleep(ctx, time.Duration(option.Duration)*time.Second) {
					return
				}
			case OPTION_TIME, OPTION_TIME_BLINK:
				// 创建子 context，同时监听父 context 的取消
				subCtx, subCancel := context.WithTimeout(ctx, time.Duration(option.Duration)*time.Second)
				for {
					select {
					case <-subCtx.Done(): // 超时或父 context 取消
						subCancel()
						continue optionLoop
					default:
						formattedTime := time.Now().In(Location).Format(option.Value)
						// 默认时间格式显示秒，所以 : 不需要闪烁
						if option.Value != DEFAULT_TIME_FORMAT {
							if timeFlag {
								// ":" is 2 columns width, while a single space is 1 column width.
								formattedTime = strings.ReplaceAll(formattedTime, ":", "  ")
							}
							timeFlag = !timeFlag
						}
						screen.WriteData(formattedTime, getStatus())
						if !sleep(ctx, time.Second/2) { // 使用小于1s的刷新间隔保证秒的数字一直变化
							subCancel()
							return // 如果是父 context 取消，直接从 mainLoop 返回
						}
					}
				}
			case OPTION_TEMP:
				tempString := getTemp(option.Value)
				if tempString == "" {
					continue
				}
				screen.WriteData(tempString, getStatus())
				if returnAfterFinish {
					return
				}
				if !sleep(ctx, time.Duration(option.Duration)*time.Second) {
					return
				}
			case OPTION_TEXT, OPTION_STRING:
				screen.WriteData(option.Value, getStatus())
				if returnAfterFinish {
					return
				}
				if !sleep(ctx, time.Duration(option.Duration)*time.Second) {
					return
				}
			case OPTION_CPU:
				cpuUsage, _, _, _, _ := Sm.Get(option.Value)
				displayStr := fmt.Sprintf("CPU %.1f%%", cpuUsage*100)
				screen.WriteData(displayStr, getStatus())
				if returnAfterFinish {
					return
				}
				if !sleep(ctx, time.Duration(option.Duration)*time.Second) {
					return
				}
			case OPTION_UPLOAD, OPTION_DOWNLOAD:
				_, txRate, rxRate, _, _ := Sm.Get(option.Value)
				var displayStr string
				if option.Value != Ifname {
					displayStr = option.Value
				}
				if option.Type == OPTION_UPLOAD {
					displayStr += "↑" + ByteCountIEC(int64(txRate))
				} else {
					displayStr += "↓" + ByteCountIEC(int64(rxRate))
				}
				screen.WriteData(displayStr, getStatus())
				if returnAfterFinish {
					return
				}
				if !sleep(ctx, time.Duration(option.Duration)*time.Second) {
					return
				}
			case OPTION_DINO:
				// 传入 context 以便中断循环
				runDino(ctx, screen, option.Duration)
				// 检查是否因为 context 取消而返回的
				if ctx.Err() != nil || returnAfterFinish {
					return
				}
			case OPTION_URL, OPTION_GET_BY_URL:
				now := time.Now()
				if now.Before(Cache[i].Expires) {
					screen.WriteData(Cache[i].Data, getStatus())
					if returnAfterFinish {
						return
					}
					if !sleep(ctx, time.Duration(option.Duration)*time.Second) {
						return
					}
					continue
				}
				// 使用 WithContext 创建请求，以便 HTTP 请求能被中断
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, option.Value, nil)
				if err != nil {
					fmt.Printf("Error create url %q http request: %v\n", option.Value, err)
					continue
				}
				res, err := http.DefaultClient.Do(req)
				if err != nil {
					fmt.Printf("Error fetching url %q: %v\n", option.Value, err)
					// 如果是因为 cancel 导致的错误，直接返回
					if ctx.Err() != nil {
						return
					}
					continue
				}
				if res.StatusCode != 200 {
					fmt.Printf("Error fetching url %q: status=%d\n", option.Value, res.StatusCode)
					res.Body.Close()
					continue
				}
				bodyBytes, err := io.ReadAll(res.Body)
				res.Body.Close()
				if err != nil {
					fmt.Printf("Error reading url %q response body: %v\n", option.Value, err)
					continue
				}
				body := strings.TrimSpace(string(bodyBytes))
				_, resExpires, _ := cachecontrol.CachableResponse(req, res, cachecontrol.Options{})
				if resExpires.After(now) || UrlCacheTime > 0 {
					var expires time.Time
					if UrlCacheTime > 0 {
						expires = now.Add(time.Second * time.Duration(UrlCacheTime))
					}
					if resExpires.After(expires) {
						expires = resExpires
					}
					Cache[i] = ContentCache{
						Data:    body,
						Expires: expires,
					}
				}
				fmt.Printf("url %s body %q expires %s\n", option.Value, body, Cache[i].Expires)
				screen.WriteData(body, getStatus())
				if returnAfterFinish {
					return
				}
				if !sleep(ctx, time.Duration(option.Duration)*time.Second) {
					return
				}
			}
		}
		if OneShot {
			return
		}
	}
}

func getTemp(tempFlags string) string {
	value := ""
	afterFirst := false
	for _, char := range tempFlags {
		if char < '0' || char > '6' {
			continue
		}
		i, _ := strconv.Atoi(string(char))
		typePath := fmt.Sprintf("/sys/class/thermal/thermal_zone%d/type", i)
		tempPath := fmt.Sprintf("/sys/class/thermal/thermal_zone%d/temp", i)

		zoneType, err := os.ReadFile(typePath) // "cpu-thermal"
		if err != nil {
			fmt.Printf("getTemp type from %s error: %v\n", typePath, err)
			continue
		}
		tempData, err := os.ReadFile(tempPath) // "58000" => 58℃
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
		if afterFirst {
			value += "  "
		}
		value += fmt.Sprintf("%s:%.1f℃", strings.TrimSuffix(strings.TrimSpace(string(zoneType)), "-thermal"),
			float64(tempInt)/1000.0)
		afterFirst = true
	}
	return value
}

func getZoneName() string {
	zoneName := os.Getenv("TZ")
	if zoneName != "" {
		return zoneName
	}
	zoneName = "Asia/Shanghai"
	file, err := os.Open("/etc/config/system")
	if err != nil {
		fmt.Printf("Error opening /etc/config/system file: %v\n", err)
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

// runDino 启动恐龙快跑动画
// status: LED 的状态字节（控制上面的指示灯等）
func runDino(parentCtx context.Context, screen *athenaLed.LedScreen, duration int) {
	// 创建一个子 Context，设置超时时间。
	ctx, cancel := context.WithTimeout(parentCtx, time.Duration(duration)*time.Second)
	defer cancel() // 确保函数退出时清理资源

	// 动画刷新间隔
	const frameDuration = 100 * time.Millisecond

	// --- 1. 定义素材 (低位在上，高位在下) ---

	// 恐龙 (2列宽)
	// Frame 1: 腿 1
	dinoFrame1 := []byte{
		0b00001111, // 列1: 身体+后腿 (Bit 0-3 身体, Bit 4 腿)
		0b00001010, // 列2: 头+前腿 (Bit 1 头, Bit 3 腿)
	}
	// Frame 2: 腿 2 (交换 Bit 4 的状态模拟跑动)
	dinoFrame2 := []byte{
		0b00001110, // 列1: 腿抬起
		0b00001011, // 列2: 腿放下
	}

	// 仙人掌 (1列宽)
	cactus := byte(0b00011110) // 底部实心，顶部留空

	// --- 2. 状态变量 ---

	// 背景缓冲区 (用于存储地面和障碍物)
	background := make([]byte, athenaLed.WIDTH)
	// 渲染缓冲区 (最终发给屏幕的数据)
	renderBuffer := make([]byte, athenaLed.WIDTH)

	tick := 0
	obstacleDistance := 0 // 距离下一个障碍物的计数器

	for {
		// 检查 context 是否被取消 (超时 或 收到信号)
		select {
		case <-ctx.Done():
			return
		default:
		}

		// --- 逻辑更新 ---

		// A. 背景向左滚动
		// 将 background[1:] 复制到 background[0:]，实现左移
		copy(background, background[1:])

		// B. 生成最右侧的新内容
		newCol := byte(0)

		// 生成地面 (Bit 4): 随机产生断裂感
		if rand.Intn(10) > 2 {
			newCol |= 0b00010000
		}

		// 生成障碍物 (逻辑：距离够远且随机触发)
		obstacleDistance++
		if obstacleDistance > 8 && rand.Intn(100) > 85 {
			newCol = cactus // 放置仙人掌 (仙人掌自带地面)
			obstacleDistance = 0
		}

		background[athenaLed.WIDTH-1] = newCol

		// --- 渲染合成 ---

		// C. 复制背景到渲染缓冲
		copy(renderBuffer, background)

		// D. 叠加恐龙 (固定在第 3, 4 列)
		// 根据 tick 切换恐龙的帧，实现跑动动画
		var currentDino []byte
		if tick%2 == 0 {
			currentDino = dinoFrame1
		} else {
			currentDino = dinoFrame2
		}

		// 将恐龙覆盖到渲染缓冲区 (使用覆盖模式，而非 OR 叠加，避免透视)
		// 注意：恐龙位置不要越界
		renderBuffer[3] = currentDino[0]
		renderBuffer[4] = currentDino[1]

		// --- 发送数据 ---
		screen.WriteRawData(renderBuffer, getStatus())

		tick++

		// 使用 sleep 函数以便快速响应中断/超时
		// 注意：这里传入的是带有超时的 ctx
		if !sleep(ctx, frameDuration) {
			return
		}
	}
}

func getStatus() [4]float64 {
	cpuUsage, _, _, upProb, dlProb := Sm.Get(Ifname)
	probs := [4]float64{0, 0, 0, 0}
	// Bit 0: Time
	if (Status & 1) != 0 {
		probs[athenaLed.LedTime] = 1.0
	}
	// Bit 1: Medal (cpu)
	if (Status & 2) == 0 {
		// optional: 限制最大概率 (永远闪烁)
		// CPU 10%: 概率 0.05 -> 极少闪烁。
		// CPU 100%: 概率 0.5 -> 疯狂闪烁 (不会常亮)。
		// cpuUsage *= 0.5
		probs[athenaLed.LedMedal] = cpuUsage
	} else {
		probs[athenaLed.LedMedal] = 1.0
	}
	// Bit 2: Upload
	if (Status & 4) == 0 {
		probs[athenaLed.LedUpload] = upProb
	} else {
		probs[athenaLed.LedUpload] = 1.0
	}
	// Bit 3: Download
	if (Status & 8) == 0 {
		probs[athenaLed.LedDownload] = dlProb
	} else {
		probs[athenaLed.LedDownload] = 1.0
	}
	return probs
}

// option example: `string:text_content#5`. both value and duration part are optional
func parseOption(option string) *Option {
	optionType := option
	value := ""
	duration := 0
	i := strings.IndexByte(option, ':')
	if i == -1 { // "string" or "string#5"
		i = strings.LastIndexByte(option, '#')
		if i != -1 {
			optionType = option[:i]
			duration, _ = strconv.Atoi(option[i+1:])
		}
	} else { // "string:text" or "string:text#5"
		optionType = option[:i]
		rest := option[i+1:]
		j := strings.LastIndexByte(rest, '#')
		if j != -1 {
			value = rest[:j]
			duration, _ = strconv.Atoi(rest[j+1:])
		} else {
			value = rest
		}
	}
	if value == "" {
		switch optionType {
		case OPTION_TEXT, OPTION_STRING:
			value = Text
		case OPTION_DATE:
			value = DEFAULT_DATE_FORMAT
		case OPTION_TIME, OPTION_TIME_BLINK:
			value = DEFAULT_TIME_FORMAT
		case OPTION_TEMP:
			value = TempFlag
		case OPTION_URL, OPTION_GET_BY_URL:
			value = Url
		case OPTION_UPLOAD, OPTION_DOWNLOAD:
			value = Ifname
		}
	} else {
		value = strings.ReplaceAll(value, "_", " ")
	}
	if duration <= 0 {
		duration = Seconds
	}
	return &Option{
		Type:     optionType,
		Value:    value,
		Duration: duration,
	}
}
