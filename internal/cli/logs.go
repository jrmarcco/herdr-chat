package cli

import (
	"strconv"
	"strings"
	"time"
)

// logs 查看 bot 的日志。
// --errors 只看出错的行，
// --day 挑一天，
// -n 只看尾部。
func (a *App) cmdLogs(args []string) int {
	day := a.logDay()
	onlyErrors := false
	tail := 0

	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--errors":
			onlyErrors = true
		case args[i] == "--day" && i+1 < len(args):
			day = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--day="):
			day = strings.TrimPrefix(args[i], "--day=")
		case args[i] == "-n" && i+1 < len(args):
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n < 1 {
				a.errf("herdrchat logs: -n takes a number\n")
				return 2
			}
			tail = n
			i++
		case args[i] == "-n" || strings.HasPrefix(args[i], "-n="):
			a.errf("herdrchat logs: -n takes a number\n")
			return 2
		default:
			a.errf("herdrchat logs: unknown option %q\n", args[i])
			return 2
		}
	}

	if _, err := time.Parse("2006-01-02", day); err != nil {
		a.errf("herdrchat logs: --day takes YYYY-MM-DD\n")
		return 2
	}

	pl, ok := a.places()
	if !ok {
		return 2
	}

	var lines []string
	for _, line := range a.logSince(pl, day, 0) {
		if onlyErrors {
			if f := strings.Fields(line); len(f) >= 2 && f[1] == levelError {
				lines = append(lines, line)
			}
			continue
		}
		lines = append(lines, line)
	}
	if lines == nil {
		a.errf("herdrchat: no log for %s\n", day)
		return 1
	}
	if tail > 0 && len(lines) > tail {
		lines = lines[len(lines)-tail:]
	}
	for _, line := range lines {
		a.println(line)
	}
	return 0
}
