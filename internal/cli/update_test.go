package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jrmarcco/herdr-chat/internal/config"
	"github.com/jrmarcco/herdr-chat/internal/service"
)

// `herdrchat update` 的行为测试：
// 查、比、下载、校验、替换、重启，全部在注入的 Latest/Download 与临时目录上跑，网络一个不出。
//
// makeTarball 打一个 release 形状的 tar.gz：根目录 herdrchat + README。
func makeTarball(t *testing.T, binary []byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	write := func(name string, data []byte) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}

	write("herdrchat", binary)
	write("README.md", []byte("the readme rides along\n"))

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sum256(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

// fakeRelease 造一次 release 与它的下载应答。
//
//nolint:unparam // 各用例的 release 版本本就一致。
func fakeRelease(t *testing.T, version string, tgz []byte) (Release, map[string][]byte) {
	t.Helper()
	asset := assetName(version)
	rel := Release{Tag: version, Assets: map[string]string{
		asset:                 "https://x/" + asset,
		checksumName(version): "https://x/" + checksumName(version),
	}}
	files := map[string][]byte{
		rel.Assets[asset]:                 tgz,
		rel.Assets[checksumName(version)]: fmt.Appendf(nil, "%s  %s\n", sum256(tgz), asset),
	}
	return rel, files
}

const cmdUpdate = "update"

//nolint:unparam // 升级前都是 1.0.0。
func updateApp(t *testing.T, version string, rel Release, files map[string][]byte) (*App, *bytes.Buffer) {
	t.Helper()
	a, out, _ := newApp(t)
	a.Version = version
	a.Latest = func(context.Context) (Release, error) { return rel, nil }
	a.Download = func(_ context.Context, url string) ([]byte, error) {
		data, ok := files[url]
		if !ok {
			return nil, fmt.Errorf("no such asset %s", url)
		}
		return data, nil
	}
	return a, out
}

//nolint:unparam // 旧二进制都叫 old。
func (a *App) putOldBin(t *testing.T, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(a.Bin), 0o700); err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // G306: 二进制要可执行。
	if err := os.WriteFile(a.Bin, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestAssetNamesMatchTheReleaseTemplate(t *testing.T) {
	if got := assetName("1.2.3"); !strings.HasPrefix(got, "herdrchat_1.2.3_") || !strings.HasSuffix(got, ".tar.gz") {
		t.Fatalf("asset = %s", got)
	}

	os, arch := runtime.GOOS, runtime.GOARCH
	switch arch {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "aarch64"
	}
	want := fmt.Sprintf("herdrchat_1.2.3_%s_%s.tar.gz", strings.ToUpper(os[:1])+os[1:], arch)
	if assetName("1.2.3") != want {
		t.Fatalf("asset = %s, want %s (keep .goreleaser.yaml's name_template in step)", assetName("1.2.3"), want)
	}
	if checksumName("1.2.3") != "herdrchat_1.2.3_checksums.txt" {
		t.Fatalf("checksum = %s", checksumName("1.2.3"))
	}
}

func TestBinaryFromTarballTakesOnlyTheBinary(t *testing.T) {
	tgz := makeTarball(t, []byte("the new binary"))
	bin, err := binaryFromTarball(tgz)

	if err != nil || string(bin) != "the new binary" {
		t.Fatalf("bin = %q err = %v", bin, err)
	}

	if _, err := binaryFromTarball([]byte("not a tarball")); err == nil {
		t.Fatal("a broken tarball is an error")
	}
}

func TestUpdateChecksAndStopsThere(t *testing.T) {
	a, out, _ := newApp(t)
	a.Version = "1.0.0"
	a.Latest = func(context.Context) (Release, error) { return Release{Tag: "1.0.1"}, nil }

	a.putOldBin(t, "old")
	if code := a.Run([]string{cmdUpdate, "--check"}); code != 0 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(out.String(), "newest release: 1.0.1") || !strings.Contains(out.String(), "a newer one is out") {
		t.Fatalf("out %q", out.String())
	}
	if raw, _ := os.ReadFile(a.Bin); string(raw) != "old" {
		t.Fatal("--check touched the binary")
	}

	// 已是最新：不说升级。
	a.Latest = func(context.Context) (Release, error) {
		return Release{Tag: "1.0.0"}, nil
	}
	out.Reset()
	if code := a.Run([]string{cmdUpdate}); code != 0 || !strings.Contains(out.String(), "already the newest") {
		t.Fatalf("code %d out %q", code, out.String())
	}
}

func TestUpdateReplacesTheBinary(t *testing.T) {
	tgz := makeTarball(t, []byte("the new binary"))
	rel, files := fakeRelease(t, "1.0.1", tgz)
	a, out := updateApp(t, "1.0.0", rel, files)
	a.putOldBin(t, "old")
	if code := a.Run([]string{cmdUpdate}); code != 0 {
		t.Fatalf("code %d out %q", code, out.String())
	}
	raw, err := os.ReadFile(a.Bin)
	if err != nil || string(raw) != "the new binary" {
		t.Fatalf("the binary was not replaced: %v %q", err, raw)
	}
	if info, _ := os.Stat(a.Bin); info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %o, an executable it must be", info.Mode().Perm())
	}
	if !strings.Contains(out.String(), "was not running") {
		t.Fatalf("out %q", out.String())
	}
	if _, err := os.Stat(a.Bin + ".herdrchat-new"); !os.IsNotExist(err) {
		t.Fatal("the temp file was left behind")
	}
}

func TestUpdateRefusesATamperedDownload(t *testing.T) {
	tgz := makeTarball(t, []byte("the new binary"))
	rel, files := fakeRelease(t, "1.0.1", tgz)
	// 偷掉一个字节：校验和不再对。
	asset := rel.Assets[assetName("1.0.1")]
	files[asset] = append([]byte{}, tgz[:len(tgz)-1]...)
	a, _ := updateApp(t, "1.0.0", rel, files)
	a.putOldBin(t, "old")
	if code := a.Run([]string{cmdUpdate}); code != 1 {
		t.Fatalf("code %d", code)
	}
	if raw, _ := os.ReadFile(a.Bin); string(raw) != "old" {
		t.Fatal("a failed checksum still replaced the binary")
	}
	// release 里没有本平台的资产。
	rel.Assets = map[string]string{checksumName("1.0.1"): "https://x/c"}
	a2, _ := updateApp(t, "1.0.0", rel, files)
	a2.putOldBin(t, "old")
	if code := a2.Run([]string{cmdUpdate}); code != 1 {
		t.Fatalf("missing asset: code %d", code)
	}
}

// hc 终端形态：换好后 SIGTERM 心跳文件里的 pid，终端循环自己拉起新的。
func TestUpdateRestartsTheBotByItsPid(t *testing.T) {
	tgz := makeTarball(t, []byte("the new binary"))
	rel, files := fakeRelease(t, "1.0.1", tgz)
	a, _ := updateApp(t, "1.0.0", rel, files)
	a.putOldBin(t, "old")
	if pl0, okp := a.places(); okp {
		a.writeConfig(t, pl0, "FEISHU_APP_ID=a\nFEISHU_APP_SECRET=b\n")
	}

	// 一个真进程当 bot ( sleep 30 )，pid 写进心跳文件。
	sleep := exec.CommandContext(t.Context(), "sleep", "30")
	if err := sleep.Start(); err != nil {
		t.Skipf("cannot spawn a child here: %v", err)
	}
	t.Cleanup(func() { _ = sleep.Process.Kill })
	done := make(chan struct{})
	go func() { _ = sleep.Wait; close(done) }()

	pl, _ := config.PlacesOf(a.Env, a.Home)
	if err := os.MkdirAll(pl.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	running := filepath.Join(pl.StateDir, "running")
	if err := os.WriteFile(running, []byte(time.Now().Format(time.RFC3339)+"\n"+fmt.Sprint(sleep.Process.Pid)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// 重启后 waitConnected 等的那行"connected"在第一次 sleep 时落进日志。
	a.Sleep = func(time.Duration) {
		if err := os.MkdirAll(a.logDir(pl), 0o700); err != nil {
			return
		}
		day := a.logDay
		if _, err := os.Stat(a.logFile(pl, day())); os.IsNotExist(err) {
			_ = os.WriteFile(a.logFile(pl, day()), []byte("12:00:00.000 INFO  feishu     connected\n"), 0o600)
		}
	}
	if code := a.Run([]string{cmdUpdate}); code != 0 {
		t.Fatalf("code %d", code)
	}
	select {
	case <-done:
	//  SIGTERM 到了：循环 ( 这里由测试代替 ) 会拉起新二进制。
	case <-time.After(5 * time.Second):
		t.Fatal("the bot process was not told to restart")
	}
}

// 服务形态：换好后服务重启 ( start 本就是先卸再装 )。
func TestUpdateRestartsTheService(t *testing.T) {
	tgz := makeTarball(t, []byte("the new binary"))
	rel, files := fakeRelease(t, "1.0.1", tgz)
	a, out := updateApp(t, "1.0.0", rel, files)
	a.putOldBin(t, "old")
	if pl0, okp := a.places(); okp {
		a.writeConfig(t, pl0, "FEISHU_APP_ID=a\nFEISHU_APP_SECRET=b\n")
	}

	var calls []string
	svcFile := filepath.Join(a.Home, "svc.service")
	//nolint:gosec // G306: systemd 的 unit 文件本来就是 0644。
	if err := os.WriteFile(svcFile, []byte("[Unit]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.SvcOf = func(spec service.Spec) *service.Service {
		return &service.Service{
			Kind: service.KindSystemd, File: svcFile, Label: "svc.service", Spec: spec, UID: 1000,
			Run: func(_ context.Context, name string, args ...string) (string, error) {
				line := name + " " + strings.Join(args, " ")
				calls = append(calls, line)
				if strings.HasPrefix(line, "systemctl --user show") {
					return "ActiveState=active\nMainPID=5\n", nil
				}
				return "", nil
			},
		}
	}
	// 重启后 waitConnected 等的那行"connected"。
	a.Sleep = func(time.Duration) {
		if err := os.MkdirAll(a.logDir(mustPlaces(t, a)), 0o700); err != nil {
			return
		}
		pl, _ := config.PlacesOf(a.Env, a.Home)
		if _, err := os.Stat(a.logFile(pl, a.logDay())); os.IsNotExist(err) {
			_ = os.WriteFile(a.logFile(pl, a.logDay()), []byte("12:00:00.000 INFO  feishu     connected\n"), 0o600)
		}
	}
	if code := a.Run([]string{cmdUpdate}); code != 0 {
		t.Fatalf("code %d", code)
	}
	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "systemctl --user restart svc.service") {
		t.Fatalf("the service was not restarted:\n%s", joined)
	}
	if !strings.Contains(out.String(), "✅ the new one is in and connected") {
		t.Fatalf("out %q", out.String())
	}
}

func mustPlaces(t *testing.T, a *App) config.Places {
	t.Helper()

	pl, ok := a.places()
	if !ok {
		t.Fatal("places")
	}
	return pl
}
