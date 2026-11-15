package cli

import (
	"context"
)

// install 把 bot 装成系统服务 ( launchd / systemd user unit )。
// 服务文件每次重写 ( 换了二进制路径或变量就跟着换 )；
// 装之前先停干净。
func (a *App) cmdInstall([]string) int {
	pl, _, ok := a.ready()
	if !ok {
		return 1
	}

	svc := a.serviceOf(pl)
	if svc == nil {
		a.errf("herdrchat: no service on %s; `herdrchat start` runs it in an hc terminal\n", a.Platform)
		return 1
	}

	ctx := context.Background()
	if stopped := a.stopEverywhere(ctx, pl, configOf(a, pl)); len(stopped) > 0 {
		a.printf("stopped %s first\n", joinWords(stopped))
	}
	if err := svc.Write(); err != nil {
		a.errf("herdrchat: the service file could not be written: %v\n", err)
		return 1
	}

	offset := a.logOffset(pl)
	if err := svc.Start(ctx); err != nil {
		a.errf("herdrchat: the service did not start: %v\n", err)
		return 1
	}

	a.printf("installed: %s\n", svc.File)
	if svc.Kind == "systemd" {
		a.printf("to keep it running after you log out: loginctl enable-linger\n")
	}

	a.printf("starting the service…\n")
	code := a.waitConnected(pl, "feishu", offset)
	if code == 0 {
		a.printf("it runs as a service now; `herdrchat status` says how.\n")
	}
	return code
}

// uninstall 停下、摘掉服务文件；bot 回到 hc 终端的形态 ( start )。
func (a *App) cmdUninstall([]string) int {
	pl, ok := a.places()
	if !ok {
		return 2
	}

	svc := a.serviceOf(pl)
	if svc == nil || !svc.Installed() {
		a.println("no service installed (herdrchat start runs it in an hc terminal)")
		return 0
	}

	ctx := context.Background()
	if err := svc.Disable(ctx); err != nil {
		a.errf("herdrchat: the service did not stop: %v\n", err)
	}

	if err := svc.Remove(); err != nil {
		a.errf("herdrchat: the service file could not be removed: %v\n", err)
		return 1
	}

	svc.Reload(ctx)
	a.println("removed. herdrchat start runs it in an hc terminal again")

	return 0
}
