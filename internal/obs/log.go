package obs

import (
	"encoding/json"
	"log"
	"os"
	"sync"
	"time"
)

var (
	once         sync.Once
	base         = log.New(os.Stdout, "", 0)
	debugEnabled bool
)

// EnableDebug globally enables debug logs.
func EnableDebug(v bool) { debugEnabled = v }

type Fields map[string]any

func logWith(level, msg string, f Fields) {
	once.Do(func() { base.SetFlags(0) })
	if f == nil {
		f = Fields{}
	}
	f["ts"] = time.Now().UTC().Format(time.RFC3339Nano)
	f["level"] = level
	f["msg"] = msg
	b, err := json.Marshal(f)
	if err != nil {
		base.Printf("{\"level\":\"error\",\"msg\":\"log marshal failure\",\"err\":%q}", err.Error())
		return
	}
	base.Println(string(b))
}

func Info(msg string, f Fields)  { logWith("info", msg, f) }
func Error(msg string, f Fields) { logWith("error", msg, f) }
func Debug(msg string, f Fields) {
	if debugEnabled {
		logWith("debug", msg, f)
	}
}
