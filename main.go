package main

import (
	athenaLed "athenaLed/internal"
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
)

const Version = "v0.1.1"

var status byte = 0b00001111

var (
	oneShot      bool
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
	fmt.Printf("athena-led %s\n", Version)

	flag.BoolVar(&oneShot, "oneShot", false, "Display once and exit")
	flag.IntVar(&seconds, "seconds", 5, "Led switching time (seconds)")
	flag.IntVar(&lightLevel, "lightLevel", 5, "Led light level, 0-7")
	flag.IntVar(&urlCacheTime, "urlCacheTime", 60, `The cache time for getByUrl (seconds)`)
	flag.StringVar(&statusVar, "status", "", "Space separated light-on side led status types. "+
		`Defined side led types (from top to bottom): time medal upload download`)
	flag.StringVar(&options, "option", "date timeBlink", `Space separated led options. Possible values: `+
		`date time timeBlink temp string dino getByUrl"`)
	flag.StringVar(&value, "value", "In God We Trust", `The "string" option: text content. `+
		`Allowed chars: all visible ASCII chars, some special unicode symbols like `+
		`♥ (heart), ☀ (sunny), ☾ (moon), ☁ (cloudy), 🌧 (rainy), ⛈ (thunderstorm), ❄ (snow), 🌫 (fog), `+
		`←, →, ↑, ↓, ↗, ↘, ✓, ✗`)
	flag.StringVar(&url, "url", "https://ipinfo.io/ip", `The "getByUrl" option: api url for get content`)
	flag.StringVar(&tempFlag, "tempFlag", "4", `The "temp" option: space separated temperature types. `+
		`Possible values: 0-6. Corresponding to "/sys/class/thermal/thermal_zone%d"`)
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
			mainLoop(ctx, screen)
		}()

		// 等待信号 或 任务完成
		select {
		case <-reloadCh:
			fmt.Println("\nReceived SIGHUP. Refreshing display...")
			cancel() // 通知 mainLoop 停止
			// 注意：这里不需要 <-loopDone，因为 cancel 会导致 mainLoop 退出，随后 wg.Wait() 会处理同步
		case <-exitCh:
			fmt.Println("\nReceived Exit signal. Shutting down...")
			cancel()  // 通知 mainLoop 停止
			wg.Wait() // 等待 cleanup
			return    // 退出 main 函数，触发 defer screen.Destroy()
		case <-loopDone:
			// 新增：如果 mainLoop 自己执行完了（比如 oneShot），会走到这里
			cancel() // 释放 context 资源
		}

		// 确保 goroutine 彻底结束后再进行下一步
		wg.Wait()

		// 如果是 oneShot 模式，任务执行完就退出程序
		if oneShot {
			return
		}
	}
}

// 辅助函数：支持 Context 取消的 Sleep
func sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func mainLoop(ctx context.Context, screen athenaLed.LedScreen) {
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
		for i, option := range optionsArr {
			// 在每个操作开始前检查 context 是否已取消
			if ctx.Err() != nil {
				return
			}

			returnAfterFinish := oneShot && i == len(optionsArr)-1
			fmt.Println(option)
			switch option {
			case "date":
				formattedTime := timeFormat(zoneName, "01-02")
				screen.WriteData(formattedTime, status)
				if returnAfterFinish {
					return
				}
				if !sleep(ctx, time.Duration(seconds)*time.Second) {
					return
				}
			case "time":
				formattedTime := timeFormat(zoneName, "15:04")
				screen.WriteData(formattedTime, status)
				if returnAfterFinish {
					return
				}
				if !sleep(ctx, time.Duration(seconds)*time.Second) {
					return
				}
			case "timeBlink":
				// 创建子 context，同时监听父 context 的取消
				subCtx, subCancel := context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
				loopDone := false
				for !loopDone {
					select {
					case <-subCtx.Done(): // 超时或父 context 取消
						subCancel()
						loopDone = true
					default:
						formattedTime := timeFormat(zoneName, "15:04")
						if timeFlag {
							formattedTime = strings.ReplaceAll(formattedTime, ":", "  ")
						}
						timeFlag = !timeFlag
						screen.WriteData(formattedTime, status)

						// 这里使用较短的 sleep，也要响应 context
						if !sleep(subCtx, 1*time.Second) {
							subCancel()
							return // 如果是父 context 取消，直接从 mainLoop 返回
						}
					}
				}
			case "temp":
				tempString := getTemp(tempFlag)
				if strings.EqualFold(tempString, "") {
					continue
				}
				screen.WriteData(tempString, status)
				if returnAfterFinish {
					return
				}
				if !sleep(ctx, time.Duration(seconds)*time.Second) {
					return
				}
			case "string":
				screen.WriteData(value, status)
				if returnAfterFinish {
					return
				}
				if !sleep(ctx, time.Duration(seconds)*time.Second) {
					return
				}
			case "dino":
				// 传入 context 以便中断循环
				runDino(ctx, screen, status)
				// 检查是否因为 context 取消而返回的
				if ctx.Err() != nil || returnAfterFinish {
					return
				}
			case "getByUrl":
				now := time.Now()
				if urlBody != "" && now.Before(urlExpires) {
					screen.WriteData(urlBody, status)
					if returnAfterFinish {
						return
					}
					if !sleep(ctx, time.Duration(seconds)*time.Second) {
						return
					}
					continue
				}
				// 使用 WithContext 创建请求，以便 HTTP 请求能被中断
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
				if err != nil {
					fmt.Println("Error:", err)
					continue
				}
				res, err := http.DefaultClient.Do(req)
				if err != nil {
					fmt.Println("Error:", err)
					// 如果是因为 cancel 导致的错误，直接返回
					if ctx.Err() != nil {
						return
					}
					continue
				}
				if res.StatusCode != 200 {
					fmt.Printf("Error: status=%d\n", res.StatusCode)
					res.Body.Close()
					continue
				}
				body, err := io.ReadAll(res.Body)
				res.Body.Close()
				if err != nil {
					fmt.Println("Error reading response body:", err)
					continue
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
				fmt.Printf("url %s body %s expires %s", url, body, urlExpires)
				screen.WriteData(string(body), status)
				if returnAfterFinish {
					return
				}
				if !sleep(ctx, time.Duration(seconds)*time.Second) {
					return
				}
			}
		}
		if oneShot {
			return
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
		value += fmt.Sprintf("%s:%.1f℃   ", strings.ReplaceAll(strings.TrimSpace(string(zoneType)), "-thermal", ""), float64(tempInt)/1000.0)
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

const DINO_SECONDS = 5

// runDino 启动恐龙快跑动画
// status: LED 的状态字节（控制上面的指示灯等）
func runDino(parentCtx context.Context, screen athenaLed.LedScreen, status byte) {
	// 创建一个子 Context，设置超时时间。
	ctx, cancel := context.WithTimeout(parentCtx, time.Duration(DINO_SECONDS)*time.Second)
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
		screen.WriteRawData(renderBuffer, status)

		tick++

		// 使用 sleep 函数以便快速响应中断/超时
		// 注意：这里传入的是带有超时的 ctx
		if !sleep(ctx, frameDuration) {
			return
		}
	}
}
