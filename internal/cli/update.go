package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/jrmarcco/herdr-chat/internal/cliversion"
	"github.com/jrmarcco/herdr-chat/internal/config"
)

// update 把二进制换成本次 release 的，再把 bot 重启到新的上面。
//
// 查最新 release → 下载本平台的 tar.gz 与 checksums → sha256 对上 →
// 解出 herdrchat → 同目录临时文件 + rename 原子替换 → 重启：
//
//	服务形态 launchctl/systemctl 重启 ( 干净 )；
//	hc 终端形态 SIGTERM 心跳文件里的 pid，终端里的循环 3 秒后拉起新的。
//
// bot 没跑也照样换 ( 下次启动就是新的 )；
// --check 只看不动。
//
// DefaultRepo 是 release 所在的仓库 ( HERDRCHAT_UPDATE_REPO 可换 )。
const DefaultRepo = "jrmarcco/herdr-chat"

// GitHubAPI 是查询与下载的入口 ( HERDRCHAT_UPDATE_API 可换 )。
const GitHubAPI = "https://api.github.com"

// downloadMax 是一个产物的大小上限 ( 二进制几十 MB，出格就是拿错了 )。
const downloadMax = 128 << 20

// Release 是一次 release 里 update 关心的东西。
type Release struct {
	Tag    string
	Assets map[string]string // 资产名 -> 下载地址
}

// assetName 算本平台的资产名——与 .goreleaser.yaml 的 name_template 一致
// ( Os 首字母大写、amd64→x86_64、arm64→aarch64 )。
func assetName(version string) string {
	osName := strings.ToUpper(runtime.GOOS[:1]) + runtime.GOOS[1:]
	arch := runtime.GOARCH
	switch arch {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "aarch64"
	}
	return fmt.Sprintf("herdrchat_%s_%s_%s.tar.gz", version, osName, arch)
}

// checksumName 是校验和资产的名字 ( 同一模板 )。
func checksumName(version string) string {
	return fmt.Sprintf("herdrchat_%s_checksums.txt", version)
}

// updateHTTP 是真实的查/下载
// ( App.Latest / App.Download 的默认实现；走环境里的代理设置 )。
var updateHTTP = &http.Client{Timeout: 5 * time.Minute}

//nolint:gosec // G704: url 来自 HERDRCHAT_UPDATE_API/REPO 与 release 自带的资产地址——用户自己的发布源，不是别人的输入
func updateGet(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := updateHTTP.Do(req)
	if err != nil {
		return nil, err
	}

	//nolint:errcheck // 只读一次。
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, downloadMax))
}

// latestOf 从 GitHub 问最新的 release ( tag 前缀 v 剥掉即是版本 )。
func latestOf(ctx context.Context) (Release, error) {
	api := os.Getenv("HERDRCHAT_UPDATE_API")
	if api == "" {
		api = GitHubAPI
	}
	repo := os.Getenv("HERDRCHAT_UPDATE_REPO")
	if repo == "" {
		repo = DefaultRepo
	}
	body, err := updateGet(ctx, api+"/repos/"+repo+"/releases/latest")
	if err != nil {
		return Release{}, err
	}
	var out struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return Release{}, err
	}
	if out.TagName == "" {
		return Release{}, fmt.Errorf("no release tag in the answer")
	}
	rel := Release{Tag: strings.TrimPrefix(out.TagName, "v"), Assets: map[string]string{}}
	for _, asset := range out.Assets {
		rel.Assets[asset.Name] = asset.BrowserDownloadURL
	}
	return rel, nil
}

// sha256Of 算一段内容的 sha256。
func sha256Of(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// checksumOf 从 checksums.txt 里找一个资产的 sha256 ( 找不到即校验不过 )。
func checksumOf(checksums []byte, asset string) (string, bool) {
	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == asset {
			return fields[0], true
		}
	}
	return "", false
}

// binaryFromTarball 从 release 的 tar.gz 里解出 herdrchat 本体。
func binaryFromTarball(tgz []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(tgz))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("the tarball has no herdrchat in it")
		}
		if err != nil {
			return nil, err
		}
		if filepath.Base(hdr.Name) == "herdrchat" && hdr.Typeflag == tar.TypeReg {
			return io.ReadAll(io.LimitReader(tr, downloadMax))
		}
	}
}

// replaceBin 原子替换二进制：
// 写到同目录的临时文件 ( 同一文件系统才有rename 的原子性 )，
// chmod 后 rename 盖上——正在跑的旧进程继续用旧 inode，不受影响。
func replaceBin(bin string, data []byte) error {
	tmp := bin + ".herdrchat-new"

	//nolint:gosec // G306: 可执行文件就要可执行。
	if err := os.WriteFile(tmp, data, 0o755); err != nil {
		return err
	}

	//nolint:gosec // G302: 可执行文件就要 0755。
	if err := os.Chmod(tmp, 0o755); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, bin)
}

// runningPID 读心跳文件里的 pid ( 没有即 0 )。
func runningPID(pl config.Places) int {
	raw, err := os.ReadFile(filepath.Join(pl.StateDir, "running"))
	if err != nil {
		return 0
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) < 2 {
		return 0
	}
	var pid int
	if _, err := fmt.Sscanf(lines[1], "%d", &pid); err != nil {
		return 0
	}
	return pid
}

// restartInto 换好的二进制要有人跑：服务走服务，hc 终端形态给 bot 进程一个
// SIGTERM ( 循环 3 秒后拉起新的 )。返回是否重启了。
func (a *App) restartInto(ctx context.Context, pl config.Places) (bool, error) {
	if svc := a.serviceOf(pl); svc != nil && svc.Installed() {
		if _, running := svc.Running(ctx); !running {
			return false, nil
		}
		return true, svc.Start(ctx) // start 本就是先卸再装的重启
	}
	pid := runningPID(pl)
	if pid <= 0 || pid == os.Getpid() {
		return false, nil
	}
	// 心跳文件两分钟内动过才认这个 pid：隔夜的 pid 早被别人用了。
	if info, err := os.Stat(filepath.Join(pl.StateDir, "running")); err != nil || time.Since(info.ModTime()) > 2*time.Minute {
		//nolint:nilerr // 心跳不在/过期：bot 不在跑，不是错误。
		return false, nil
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return false, err
	}
	return true, nil
}

// cmdUpdate 是 `herdrchat update`：--check 只看不动。
func (a *App) cmdUpdate(args []string) int {
	checkOnly := false
	for _, arg := range args {
		switch arg {
		case "--check":
			checkOnly = true
		default:
			a.errf("herdrchat update: unknown option %q\n", arg)
			return 2
		}
	}
	ctx := context.Background()

	rel, err := a.latest(ctx)
	if err != nil {
		a.errf("herdrchat update: could not ask for the newest release: %v\n", err)
		return 1
	}
	a.printf("newest release: %s (this is %s)\n", rel.Tag, a.Version)
	if !cliversion.Newer(rel.Tag, a.Version) && !cliversion.DevVersion(a.Version) {
		a.println("already the newest")
		return 0
	}
	if cliversion.DevVersion(a.Version) {
		a.println("this is a dev build: bringing in the release anyway")
	}
	if checkOnly {
		a.println("a newer one is out: `herdrchat update` brings it in (and restarts the bot)")
		return 0
	}

	asset := assetName(rel.Tag)
	url, ok := rel.Assets[asset]
	if !ok {
		a.errf("herdrchat update: the release has no %s for %s/%s\n", asset, runtime.GOOS, runtime.GOARCH)
		return 1
	}
	sumURL, ok := rel.Assets[checksumName(rel.Tag)]
	if !ok {
		a.errf("herdrchat update: the release has no %s to check against\n", checksumName(rel.Tag))
		return 1
	}
	tgz, err := a.download(ctx, url)
	if err != nil {
		a.errf("herdrchat update: %v\n", err)
		return 1
	}
	checksums, err := a.download(ctx, sumURL)
	if err != nil {
		a.errf("herdrchat update: %v\n", err)
		return 1
	}
	want, ok := checksumOf(checksums, asset)
	if !ok || want != sha256Of(tgz) {
		a.errf("herdrchat update: %s did not pass its checksum: not installed\n", asset)
		return 1
	}
	data, err := binaryFromTarball(tgz)
	if err != nil {
		a.errf("herdrchat update: %v\n", err)
		return 1
	}
	if rerr := replaceBin(a.Bin, data); rerr != nil {
		a.errf("herdrchat update: could not replace %s: %v (is its folder writable?)\n", a.Bin, rerr)
		return 1
	}
	a.printf("updated %s to %s\n", a.Bin, rel.Tag)

	// 换好了要有人跑新的：bot 在跑就重启它 ( 服务或 hc 终端循环 )。
	pl, okPlaces := a.places()
	if !okPlaces {
		return 0
	}
	cfg := configOf(a, pl)
	restarted, err := a.restartInto(ctx, pl)
	switch {
	case err != nil:
		a.errf("herdrchat update: in place, but the bot did not restart: %v (`herdrchat start` starts the new one)\n", err)
		return 1
	case restarted:
		a.printf("restarting the bot onto it…\n")
		if code := a.waitConnected(pl, cfg.Chat, a.logOffset(pl)); code != 0 {
			return code
		}
		a.println("✅ the new one is in and connected")
		return 0
	default:
		a.println("the bot was not running; the next `herdrchat start` uses the new one")
		return 0
	}
}

// latest / download：真实现之外的注入口 ( 测试 )。
func (a *App) latest(ctx context.Context) (Release, error) {
	if a.Latest != nil {
		return a.Latest(ctx)
	}
	return latestOf(ctx)
}

func (a *App) download(ctx context.Context, url string) ([]byte, error) {
	if a.Download != nil {
		return a.Download(ctx, url)
	}
	return updateGet(ctx, url)
}
