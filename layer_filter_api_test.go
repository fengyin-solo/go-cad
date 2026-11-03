// layer_filter_api_test.go 公共门面层（cad.RenderOptions）图层过滤冒烟：
// 白名单/排除字段经类型别名直达内部渲染，四个公共渲染入口行为与内部
// 同语义（未知名错误含名称；SVG 分组只含实际输出图层）。
package cad

import (
	"strings"
	"testing"

	"github.com/unitedrhino/go-cad/internal/drawing"
	"github.com/unitedrhino/go-cad/internal/entity"
)

// apiLayerDoc 门面测试文档：BASE 图层 LINE + NOTE 图层 TEXT。
func apiLayerDoc() *drawing.Document {
	return &drawing.Document{
		ModelSpace: []any{
			&entity.EntLine{
				BaseEntity: entity.BaseEntity{Layer: 0x10},
				Start:      entity.Point3{X: 0, Y: 0, Z: 0},
				End:        entity.Point3{X: 100, Y: 0, Z: 0},
			},
			&entity.EntText{
				BaseEntity: entity.BaseEntity{Layer: 0x20},
				Text:       "NOTE",
				Insertion:  entity.Point3{X: 0, Y: 20, Z: 0},
				Height:     3,
			},
		},
		LayerColors: map[uint64]drawing.LayerColor{
			0x10: {Name: "BASE"},
			0x20: {Name: "NOTE"},
		},
	}
}

// TestAPIRenderLayerFilter 公共 RenderOptions 的白名单经门面在
// PNG/SVG/单图框/全部图框四入口生效；未知名错误透传且含名称。
func TestAPIRenderLayerFilter(t *testing.T) {
	doc := apiLayerDoc()
	sheet := Sheet{Name: "S", Box: [4]float64{-50, -50, 150, 80}}

	// SVG 白名单：只输出 BASE，分组/文本随之过滤
	svg, err := RenderSVG(doc, RenderOptions{Width: 256, IncludeLayers: []string{"BASE"}})
	if err != nil {
		t.Fatalf("RenderSVG 白名单: %v", err)
	}
	s := string(svg)
	if !strings.Contains(s, `id="layer:BASE"`) || strings.Contains(s, ">NOTE</text>") {
		t.Fatalf("门面白名单 SVG 应只含 BASE:\n%s", s)
	}

	// PNG 排除名单不报错且产出可解码图片
	if png, err := RenderPNG(doc, RenderOptions{Width: 256, ExcludeLayers: []string{"NOTE"}}); err != nil || len(png) < 100 {
		t.Fatalf("RenderPNG 排除名单异常: err=%v len=%d", err, len(png))
	}
	if data, err := RenderSheetPNG(doc, sheet, RenderOptions{Width: 256, IncludeLayers: []string{"BASE"}}); err != nil || len(data) < 100 {
		t.Fatalf("RenderSheetPNG 白名单异常: err=%v len=%d", err, len(data))
	}
	if data, err := RenderSheetSVG(doc, sheet, RenderOptions{Width: 256, IncludeLayers: []string{"BASE"}}); err != nil || !strings.Contains(string(data), `id="layer:BASE"`) {
		t.Fatalf("RenderSheetSVG 白名单异常: err=%v", err)
	}
	results, err := RenderAllSheets(doc, RenderOptions{Width: 256, ExcludeLayers: []string{"NOTE"}}, "svg")
	if err != nil || len(results) == 0 {
		t.Fatalf("RenderAllSheets 排除名单异常: err=%v n=%d", err, len(results))
	}
	for _, r := range results {
		if strings.Contains(string(r.Data), ">NOTE</text>") {
			t.Fatalf("批量结果 %q 不应含被排除文本", r.Name)
		}
	}

	// 未知名：四入口错误都必须带上名称
	for _, tc := range []struct {
		name string
		do   func() error
	}{
		{"RenderPNG", func() error {
			_, e := RenderPNG(doc, RenderOptions{Width: 256, IncludeLayers: []string{"GHOST"}})
			return e
		}},
		{"RenderSVG", func() error {
			_, e := RenderSVG(doc, RenderOptions{Width: 256, IncludeLayers: []string{"GHOST"}})
			return e
		}},
		{"RenderSheetPNG", func() error {
			_, e := RenderSheetPNG(doc, sheet, RenderOptions{Width: 256, ExcludeLayers: []string{"GHOST"}})
			return e
		}},
		{"RenderSheetSVG", func() error {
			_, e := RenderSheetSVG(doc, sheet, RenderOptions{Width: 256, ExcludeLayers: []string{"GHOST"}})
			return e
		}},
		{"RenderAllSheets", func() error {
			_, e := RenderAllSheets(doc, RenderOptions{Width: 256, IncludeLayers: []string{"GHOST"}}, "png")
			return e
		}},
	} {
		err := tc.do()
		if err == nil || !strings.Contains(err.Error(), "GHOST") {
			t.Fatalf("%s 应返回含 GHOST 的错误，实际 %v", tc.name, err)
		}
	}
}
