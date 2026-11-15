// 服务文件的骨架：launchd 的 plist 与 systemd 的 unit 各一份模板，
// 收在 templates/ 子目录里嵌入（go:embed 只认包目录内的文件）。
// Go 侧只准备数据；空白（换行、缩进）被服务快照测试逐字节锁死，
// 改模板后 `go test ./internal/service` 不绿就是输出变了。
package service

import (
	"embed"
	"strings"
	"text/template"
)

//go:embed templates/launchd.plist.tmpl templates/systemd.unit.tmpl
var templateFiles embed.FS

// templates 两份骨架共用一个 FuncMap（xml 只被 plist 用到）。
var templates = template.Must(template.New("").
	Funcs(template.FuncMap{"xml": xmlText}).
	ParseFS(templateFiles, "templates/*.tmpl"))

// kv 是 plist 环境变量段的一对键值（排序在 Go 侧做，模板不该管顺序）。
type kv struct{ Key, Value string }

// plistData 是 launchd.plist.tmpl 的全部输入。
type plistData struct {
	Label    string
	Args     []string
	Throttle int
	Env      []kv
	Out      string
}

// unitData 是 systemd.unit.tmpl 的全部输入（引号与 $ 转义在 Go 侧做齐）。
type unitData struct {
	ID        string
	ExecStart string
	Env       []string
	Out       string
	Restart   int
}

// render 渲染一份骨架。模板是静态的、数据是纯字符串，执行出错即编程错误
// （快照测试兜住），panic 与包内其余不可恢复错误同级。
func render(name string, data any) string {
	var b strings.Builder
	if err := templates.ExecuteTemplate(&b, name, data); err != nil {
		panic("service template " + name + ": " + err.Error())
	}
	return b.String()
}
