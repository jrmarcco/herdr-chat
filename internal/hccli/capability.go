package hccli

import (
	"context"
	"strings"
	"time"
)

// Capabilities 是一台机器上的 hc 能做什么。
// 探测失败只表示 "按最低能力对待"，绝不因此拒绝启动：
// 需要更高能力的功能保持关闭，status 会如实说明。
type Capabilities struct {
	HCVersion         string
	HasTerminalAttach bool
}

// ProbeCapabilities 直接问 hc 自己。
// 版本来自 `hc --version`；terminal attach 则看 `hc terminal attach --help` 是否认识这个动词。
// 尽力而为、短超时：没有 hc 的机器自然什么都答不上来。
func (c *CLI) ProbeCapabilities(parent context.Context) Capabilities {
	caps := Capabilities{}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()

	if out, err := c.Run(ctx, "default", []string{"--version"}, 5*time.Second); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			caps.HCVersion = strings.TrimSpace(strings.TrimPrefix(line, "hc"))
			break
		}
	}

	if out, err := c.Run(ctx, "default", []string{"terminal", "attach", "--help"}, 5*time.Second); err == nil {
		// 能跑通：动词存在且能解析选项。
		_ = out
		caps.HasTerminalAttach = true
	} else if msg := err.Error(); strings.Contains(msg, "timed out") {
		// 它在等一个可 attach 的终端：动词存在。
		caps.HasTerminalAttach = true
	}
	return caps
}
