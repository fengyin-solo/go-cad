// viewport_api_test.go 覆盖门面层公开视口选项：cad.Viewport 经
// cad.RenderOptions 对 RenderPNG/RenderSVG/RenderSheet* 四个入口可达，
// 输出尺寸严格由视口宽高比决定，非法视口返回错误。内部裁剪/文字边界/
// 坐标映射的细粒度覆盖见 internal/render/viewport_test.go。
package cad

import (
	"bytes"
	"image/png"
	"strings"
	"testing"

	"github.com/unitedrhino/go-cad/internal/drawing"
	"github.com/unitedrhino/go-cad/internal/entity"
)

func viewportAPIDoc() *Document {
	return &drawing.Document{ModelSpace: []any{
		&entity.EntLine{Start: entity.Point3{X: 0, Y: 0}, End: entity.Point3{X: 100, Y: 50}},
	}}
}

// TestViewportAPIPublic 公开选项端到端：PNG/SVG/单图框 PNG/SVG 均按
// 视口严格出图（横向区域 100x50，宽 500 → 高 250）。
func TestViewportAPIPublic(t *testing.T) {
	doc := viewportAPIDoc()
	opts := RenderOptions{
		Width:    500,
		Viewport: &Viewport{MinX: 0, MinY: 0, MaxX: 100, MaxY: 50},
	}
	// PNG
	pngData, err := RenderPNG(doc, opts)
	if err != nil {
		t.Fatalf("RenderPNG: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(pngData))
	if err != nil {
		t.Fatalf("PNG 解码: %v", err)
	}
	if w, h := img.Bounds().Dx(), img.Bounds().Dy(); w != 500 || h != 250 {
		t.Fatalf("PNG 视口尺寸应为 500x250, got %dx%d", w, h)
	}
	// SVG
	svgData, err := RenderSVG(doc, opts)
	if err != nil {
		t.Fatalf("RenderSVG: %v", err)
	}
	if !strings.Contains(string(svgData), `viewBox="0 0 100 50"`) {
		t.Fatalf("SVG viewBox 应严格对应视口:\n%s", svgData)
	}
	// 单图框入口（图框远大于视口，视口必须覆盖图框几何）
	sheet := Sheet{Name: "框", Box: [4]float64{-1000, -1000, 1000, 1000}}
	sheetPNG, err := RenderSheetPNG(doc, sheet, opts)
	if err != nil {
		t.Fatalf("RenderSheetPNG: %v", err)
	}
	img2, err := png.Decode(bytes.NewReader(sheetPNG))
	if err != nil {
		t.Fatalf("RenderSheetPNG 解码: %v", err)
	}
	if img2.Bounds().Dx() != 500 || img2.Bounds().Dy() != 250 {
		t.Fatalf("RenderSheetPNG 应按视口 500x250 出图, got %dx%d", img2.Bounds().Dx(), img2.Bounds().Dy())
	}
	sheetSVG, err := RenderSheetSVG(doc, sheet, opts)
	if err != nil {
		t.Fatalf("RenderSheetSVG: %v", err)
	}
	if !strings.Contains(string(sheetSVG), `viewBox="0 0 100 50"`) {
		t.Fatalf("RenderSheetSVG viewBox 应严格对应视口:\n%s", sheetSVG)
	}
}

// TestViewportAPIErrors 门面层非法视口错误口径（零宽/非有限）。
func TestViewportAPIErrors(t *testing.T) {
	doc := viewportAPIDoc()
	for _, v := range []*Viewport{
		{MinX: 0, MinY: 0, MaxX: 0, MaxY: 50},
		{MinX: 0, MinY: 0, MaxX: 100, MaxY: 0},
	} {
		if _, err := RenderPNG(doc, RenderOptions{Width: 500, Viewport: v}); err == nil ||
			!strings.Contains(err.Error(), "cad: 视口") {
			t.Fatalf("非法视口 %v 应返回 cad: 视口 错误, got %v", v, err)
		}
		if _, err := RenderSVG(doc, RenderOptions{Width: 500, Viewport: v}); err == nil {
			t.Fatalf("RenderSVG 非法视口 %v 应报错", v)
		}
	}
}

// TestViewportAPIBatchIgnores 批量入口以图框为视口来源，Viewport 字段
// 被显式忽略不影响批量出图（不报错、产物正常）。
func TestViewportAPIBatchIgnores(t *testing.T) {
	doc := viewportAPIDoc()
	results, err := RenderAllSheets(doc, RenderOptions{
		Width:    400,
		Viewport: &Viewport{MinX: 0, MinY: 0, MaxX: 1, MaxY: 1},
	}, "png")
	if err != nil {
		t.Fatalf("RenderAllSheets 应忽略视口选项: %v", err)
	}
	if len(results) == 0 || len(results[0].Data) == 0 {
		t.Fatal("批量出图应返回正常产物")
	}
}
