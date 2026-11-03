// Package logx 提供带滚动与限流的文件日志。
//
// 设计要点（都是 Python 版踩过坑之后定下来的）：
//   - 同一句话在短时间内重复出现时必须限流，否则一次网络抖动会把日志刷满，
//     反而看不到真正的状态变化。
//   - 日志文件按大小滚动，长期运行不会无限增长。
//   - 写日志绝不能因为磁盘问题把主流程带崩 —— 所有错误都吞掉。
package logx

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Level 是日志级别。
type Level int

const (
	Debug Level = iota
	Info
	Warn
	Error
)

func (l Level) String() string {
	switch l {
	case Debug:
		return "DEBUG"
	case Info:
		return "INFO"
	case Warn:
		return "WARN"
	case Error:
		return "ERROR"
	}
	return "INFO"
}

// MaxBytes 是单个日志文件的上限，超过后滚动。
const MaxBytes = 2 << 20 // 2MB

var (
	mu       sync.Mutex
	filePath string
	file     *os.File
	written  int64

	// 限流表：key -> 上次输出时间
	lastEmit = map[string]time.Time{}
)

// Init 指定日志文件路径并打开。
func Init(path string) {
	mu.Lock()
	defer mu.Unlock()
	filePath = path
	openLocked()
}

// Path 返回当前日志文件路径。
func Path() string {
	mu.Lock()
	defer mu.Unlock()
	return filePath
}

func openLocked() {
	if file != nil {
		file.Close()
		file = nil
	}
	if filePath == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(filePath), 0o755)
	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	file = f
	if st, err := f.Stat(); err == nil {
		written = st.Size()
	}
}

// rotateLocked 在文件超限时把它挪到 .1 再重开。
func rotateLocked() {
	if file == nil || written < MaxBytes {
		return
	}
	file.Close()
	file = nil
	_ = os.Rename(filePath, filePath+".1")
	openLocked()
}

// Log 写一条日志。
func Log(lv Level, format string, args ...any) {
	write(lv, "", format, args...)
}

// LogRate 写一条计流日志：同一个 throttleKey 在 window 内只写一次，
// 被压掉的次数在下次输出时以 (+N) 的形式带出来。
func LogRate(lv Level, throttleKey string, window time.Duration, format string, args ...any) {
	write(lv, throttleKey, format, args...)
	_ = window
}

var suppressed = map[string]int{}

func write(lv Level, throttleKey, format string, args ...any) {
	mu.Lock()
	defer mu.Unlock()

	now := time.Now()
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}

	if throttleKey != "" {
		if last, ok := lastEmit[throttleKey]; ok {
			// 与上次输出间隔过短则抑制，但记录被压掉的条数
			if now.Sub(last) < throttleWindow(throttleKey) {
				suppressed[throttleKey]++
				return
			}
		}
		if n := suppressed[throttleKey]; n > 0 {
			msg = fmt.Sprintf("%s（此前同类 %d 条已省略）", msg, n)
			suppressed[throttleKey] = 0
		}
		lastEmit[throttleKey] = now
	}

	line := fmt.Sprintf("[%s] [%-5s] %s\n",
		now.Format("2006-01-02 15:04:05"), lv.String(), msg)

	if file == nil {
		return
	}
	n, err := file.WriteString(line)
	if err != nil {
		return
	}
	written += int64(n)
	rotateLocked()
}

// throttleWindow 允许按 key 前缀指定不同的抑制窗口，默认 30 秒。
func throttleWindow(key string) time.Duration {
	if w, ok := throttleWindows[key]; ok {
		return w
	}
	return 30 * time.Second
}

var throttleWindows = map[string]time.Duration{}

// SetThrottleWindow 为某个 key 指定抑制窗口。
func SetThrottleWindow(key string, w time.Duration) {
	mu.Lock()
	defer mu.Unlock()
	throttleWindows[key] = w
}

// Tail 返回日志最后 n 行，供界面显示。
func Tail(n int) []string {
	mu.Lock()
	path := filePath
	mu.Unlock()

	if path == "" || n <= 0 {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	// 从后往前数换行符，避免把整个文件按行切分（文件可能很大）
	end := len(data)
	if end > 0 && data[end-1] == '\n' {
		end--
	}
	start := end
	count := 0
	for i := end - 1; i >= 0; i-- {
		if data[i] == '\n' {
			count++
			if count >= n {
				start = i + 1
				break
			}
			start = i + 1
		}
	}
	seg := string(data[start:])
	if seg == "" {
		return nil
	}
	lines := splitLines(seg)
	return lines
}

func splitLines(s string) []string {
	out := make([]string, 0, 64)
	cur := make([]byte, 0, 128)
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, string(cur))
			cur = cur[:0]
			continue
		}
		if s[i] == '\r' {
			continue
		}
		cur = append(cur, s[i])
	}
	if len(cur) > 0 {
		out = append(out, string(cur))
	}
	return out
}

// Close 关闭文件句柄。
func Close() {
	mu.Lock()
	defer mu.Unlock()
	if file != nil {
		file.Close()
		file = nil
	}
}
