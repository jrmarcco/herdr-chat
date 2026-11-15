package hccli

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"
)

// 终端所在机器 home 目录下的文件：本机直接读，远端经 `hc exec --on`。

var homeDirFn = defaultHome

func defaultHome() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return home
}

// SetHomeDir 换本机 home ( 测试隔离用 )。
func SetHomeDir(dir string) { homeDirFn = func() string { return dir } }

// HomeFile 读终端所在机器 home 下的一个文件 ( tail>0 时只取尾部那些行 )。
func (c *CLI) HomeFile(ctx context.Context, session, machine, rel string, tail int) (string, error) {
	if machine == "" {
		//nolint:gosec // G304: 路径由 reader 按固定布局拼出。
		text, err := os.ReadFile(homeDirFn() + "/" + rel)
		if err != nil {
			return "", err
		}
		if tail > 0 {
			lines := strings.Split(string(text), "\n")
			if len(lines) > tail {
				lines = lines[len(lines)-tail:]
			}
			return strings.Join(lines, "\n"), nil
		}
		return string(text), nil
	}
	argv := []string{"cat", rel}
	if tail > 0 {
		argv = []string{"tail", "-n", strconv.Itoa(tail), rel}
	}
	return c.Run(ctx, session, append([]string{"exec", "--on", machine}, argv...), 30*time.Second)
}
