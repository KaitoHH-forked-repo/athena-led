# athena-led

自用的控制京东云 AX6600 雅典娜路由器 (JDCloud AX6600 athena, RE-CS-02) 的 LED
点阵显示屏的用户态程序。这是对[原项目](https://github.com/NONGFAH/athena-led)的 fork。加入一些新特性，包括：

- 增加支持显示的字符数量。现在所有 ASCII 字符和部分特殊 Unicode 符号都能够正常显示。
- 增加几种 option :
  - cpu : cpu 占用率。
  - upload : 当前上传速度。
  - download : 当前下载速度。
  - dino : 显示 [Chrome Dino](https://en.wikipedia.org/wiki/Dinosaur_Game) 风格的动画。
- 点阵屏幕两侧的4个 LED 状态灯(status)现在会动态变化：
  - time
  - medal : 表示系统 CPU 占用率。占用率越高闪烁越快。
  - upload & download : 表示当前路由器网口的实时上传/下载状态。传输速率越大闪烁越快。默认显示外网出口接口的信息。
- `-option` flag 里（空格分隔）的每个 option 现在支持独立设置参数，例如，`string:i_have_a_dream` : 显示 "i have a dream" 文字；
  `url:https://ipinfo.io/json#3` : 显示 `https://ipinfo.io/json` 这个 URL 的内容，显示时长为3秒；
  `upload:lan1` : 显示 `lan1` 这个网络接口的上传速度；`time:15 04`: 显示 "15 04" 格式的时间(Go 时间格式字符串)。`dino#15` : 显示 15秒的恐龙动画。
- 支持通过 `TZ` 环境变量修改显示的日期/时间的时区。
- 通过 url 获取的显示内容默认缓存至少 60 秒，并且支持通过 `Cache-Control` 响应头设置缓存有效期。

命令行参数与原版保持兼容，所以仍然可以用 [luci-app-athena-led](https://github.com/NONGFAH/luci-app-athena-led) 控制。
但一些新特性需要手动编辑 `/etc/config/athena_led` 里的参数才能启用，无法在 OpenWrt Web UI 里配置。

运行 `athena-led -h` 查看详细帮助。

## 构建

1. 设置环境变量
GOARCH=arm64;GOOS=linux
2. 编译
go build -ldflags="-s -w" -trimpath -o athena-led
3. 打包
复制`athena-led` 到`luci-app-athena-led/root/usr/sbin/`
