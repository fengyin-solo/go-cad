// main.go 实现 cadinspect 命令行工具：CAD 文档只读健康检查入口，供
// 批处理系统在一次调用中取得机器可消费的 JSON 体检报告——按实体类型与
// 图层统计模型空间/图纸空间/块内对象，列出跳过数量与可取得的失败句柄，
// 检查非有限几何、缺失图层名、空块与重复句柄，诊断项稳定排序。
//
// 用法：
//
//	cadinspect <input.dwg|.dxf|.dxfb>
//
// 参数说明：
//   - input：输入图纸文件路径（必填）；按扩展名分派解析器——.dxf/.dxfb
//     走 cad.ParseDXF（ASCII 与二进制 DXF 均支持），其余（.dwg）走 cad.Parse
//
// 退出码（批处理编排依据）：
//   - 0：文档健康，无诊断项
//   - 2：检查完成但存在需要人工处理的诊断项；完整 JSON 报告仍写入 stdout
//   - 1：用法错误/文件无法读取/输入无法解析/文档为空；错误信息写入 stderr，
//     stdout 不产生半截 JSON
//
// stdout 仅输出一份完整 JSON（无其他杂行），便于调用方直接反序列化。
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/unitedrhino/go-cad"
)

// exitCodeDiagnostics 存在需人工处理诊断时的独立非零退出码（与一般
// 错误退出码 1 区分，批处理系统可直接按码编排）。
const exitCodeDiagnostics = 2

// exitError 携带进程退出码的运行错误（诊断场景=2，一般错误=1）。
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		var ee *exitError
		if errors.As(err, &ee) {
			fmt.Fprintln(os.Stderr, "cadinspect:", ee.Error())
			os.Exit(ee.code)
		}
		fmt.Fprintln(os.Stderr, "cadinspect:", err)
		os.Exit(1)
	}
}

// run 执行一次完整的「读取 → 解析 → 只读检查 → JSON 写出」流程。
// stdout 独立成参供测试捕获；检查完成但存在诊断时返回 code=2 的
// exitError（JSON 已完整写出），其余失败返回普通错误（退出码 1）。
func run(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("cadinspect", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			writeUsage(stdout)
			return nil
		}
		return fmt.Errorf("无法解析参数: %w；用法: cadinspect <input.dwg|.dxf|.dxfb>", err)
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("缺少输入文件，用法: cadinspect <input.dwg|.dxf|.dxfb>")
	}
	if fs.NArg() > 1 {
		return fmt.Errorf("多余的参数: %s", fs.Arg(1))
	}
	input := fs.Arg(0)
	data, err := os.ReadFile(input)
	if err != nil {
		return fmt.Errorf("读取 %s: %w", input, err)
	}
	// 按扩展名分派解析器：.dxf（ASCII）/ .dxfb（二进制）走 DXF 读取，
	// 其余按 DWG 处理（大小写不敏感）
	var doc *cad.Document
	switch strings.ToLower(filepath.Ext(input)) {
	case ".dxf", ".dxfb":
		doc, err = cad.ParseDXF(data)
	default:
		doc, err = cad.Parse(data)
	}
	if err != nil {
		return fmt.Errorf("解析 %s: %w", input, err)
	}
	report, err := cad.InspectDocument(doc)
	if err != nil {
		return fmt.Errorf("检查 %s: %w", input, err)
	}
	report.Source = filepath.Base(input)
	buf, err := report.JSON()
	if err != nil {
		return fmt.Errorf("编码健康报告: %w", err)
	}
	if _, err := stdout.Write(buf); err != nil {
		return fmt.Errorf("写出报告: %w", err)
	}
	if report.Status != "ok" {
		return &exitError{
			code: exitCodeDiagnostics,
			err:  fmt.Errorf("发现 %d 项需要人工处理的诊断（status=%s），完整 JSON 报告已写出", len(report.Diagnostics), report.Status),
		}
	}
	return nil
}

// writeUsage 输出帮助文本（-h/--help）。
func writeUsage(w io.Writer) {
	fmt.Fprintln(w, "用法: cadinspect <input.dwg|.dxf|.dxfb>")
	fmt.Fprintln(w, "对 CAD 文档执行只读健康检查，将完整 JSON 报告写到标准输出。")
	fmt.Fprintln(w, "退出码: 0=健康 2=存在需人工处理的诊断 1=用法/解析错误")
}
