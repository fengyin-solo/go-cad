// inspect_render_test.go 以外部测试包验证只读检查的渲染不变性：
// 健康检查前后 RenderPNG 字节逐字节一致（in-package 测试无法导入
// render 包——render 依赖 drawing 会形成循环，外部 xtest 包可安全
// 经门面 cad 同时消费两者）。
package drawing_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	cad "github.com/unitedrhino/go-cad"
)

// TestInspectRenderInvariant 健康检查不得改变渲染结果。
func TestInspectRenderInvariant(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "lw_example2018.dwg")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skip("testdata 不可用")
	}
	doc, err := cad.Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	opts := cad.RenderOptions{Width: 1024}
	png0, err := cad.RenderPNG(doc, opts)
	if err != nil {
		t.Fatalf("RenderPNG baseline0: %v", err)
	}
	png1, err := cad.RenderPNG(doc, opts)
	if err != nil {
		t.Fatalf("RenderPNG baseline1: %v", err)
	}
	if !bytes.Equal(png0, png1) {
		t.Fatalf("渲染器自身不确定，无法做不变性基线")
	}
	report, err := cad.InspectDocument(doc)
	if err != nil {
		t.Fatalf("InspectDocument: %v", err)
	}
	png2, err := cad.RenderPNG(doc, opts)
	if err != nil {
		t.Fatalf("RenderPNG after inspect: %v", err)
	}
	if !bytes.Equal(png0, png2) {
		t.Fatalf("健康检查改变了渲染输出（before=%d after=%d 字节, status=%s diags=%d）",
			len(png0), len(png2), report.Status, len(report.Diagnostics))
	}
}
