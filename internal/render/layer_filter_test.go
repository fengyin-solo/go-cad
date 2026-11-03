// layer_filter_test.go 图层过滤（RenderOptions.IncludeLayers/ExcludeLayers）
// 测试：白名单/排除/先白后排除/空名单不限制、嵌套块按最终图层、同名图层
// 合并、无名图层句柄标识（含小写 hex）、未知名错误含名称，以及四个渲染
// 入口（PNG/SVG/单图框/全部图框）共用同一套过滤语义——SVG 分组与
// metadata 只含实际输出图层、单图框清晰度统计排除被过滤文字、无过滤
// 条件输出与原版本字节一致。
package render

import (
	"bytes"
	"image/png"
	"strings"
	"testing"

	"github.com/unitedrhino/go-cad/internal/drawing"
	"github.com/unitedrhino/go-cad/internal/entity"
)

// layerLine 构造指定图层的 LINE 图元。
func layerLine(layer uint64, x1, y1, x2, y2 float64) *entity.EntLine {
	return &entity.EntLine{
		BaseEntity: entity.BaseEntity{Layer: layer},
		Start:      entity.Point3{X: x1, Y: y1, Z: 0},
		End:        entity.Point3{X: x2, Y: y2, Z: 0},
	}
}

// layerText 构造指定图层的 TEXT 图元。
func layerText(layer uint64, text string, x, y, h float64) *entity.EntText {
	return &entity.EntText{
		BaseEntity: entity.BaseEntity{Layer: layer},
		Text:       text,
		Insertion:  entity.Point3{X: x, Y: y, Z: 0},
		Height:     h,
	}
}

// nestedLayerDoc 嵌套块文档：
//   - 块 B(0xB)：PIPE 图层(0x20) LINE + 无名图层(0x99，无 LayerColors) TEXT；
//   - 块 A(0xA)：BASE 图层(0x10) LINE + 引用块 B 的 INSERT；
//   - 模型空间：引用块 A 的 INSERT。
//
// 展开后共 3 个图元，分属 3 个最终图层。
func nestedLayerDoc() *drawing.Document {
	return &drawing.Document{
		ModelSpace: []any{
			&entity.EntInsert{Scale: entity.Point3{X: 1, Y: 1, Z: 1}, BlockHeader: 0xA},
		},
		Blocks: map[uint64][]any{
			0xA: {
				layerLine(0x10, 0, 0, 10, 0),
				&entity.EntInsert{Scale: entity.Point3{X: 1, Y: 1, Z: 1}, BlockHeader: 0xB},
			},
			0xB: {
				layerLine(0x20, 0, 5, 10, 5),
				layerText(0x99, "NB", 1, 6, 1),
			},
		},
		LayerColors: map[uint64]drawing.LayerColor{
			0x10: {Name: "BASE"},
			0x20: {Name: "PIPE"},
			// 0x99 故意不登记：无名图层只能按 handle-99 匹配
		},
	}
}

// primitiveLayers 展开并按选项过滤，返回保留图元的图层句柄清单。
func primitiveLayers(t *testing.T, doc *drawing.Document, opts RenderOptions) []uint64 {
	t.Helper()
	prims := drawing.NewTessellator(doc).ExpandAll()
	lf, err := newLayerFilter(doc, prims, opts)
	if err != nil {
		t.Fatalf("newLayerFilter: %v", err)
	}
	prims = lf.apply(prims)
	out := make([]uint64, 0, len(prims))
	for i := range prims {
		out = append(out, prims[i].Layer)
	}
	return out
}

// TestLayerIdentifierAndSelector 图层稳定标识与名单归一化：
// 有名用真名、无名用 handle-<大写 hex>，句柄标识十六进制大小写不敏感。
func TestLayerIdentifierAndSelector(t *testing.T) {
	doc := &drawing.Document{LayerColors: map[uint64]drawing.LayerColor{
		0x10: {Name: "WALL"},
		0x11: {Name: ""}, // 有记录但名为空：仍按无名句柄标识
	}}
	if got := layerIdentifier(doc, 0x10); got != "WALL" {
		t.Fatalf("有名图层标识应为真名，实际 %q", got)
	}
	if got := layerIdentifier(doc, 0x11); got != "handle-11" {
		t.Fatalf("空名图层应回退句柄标识，实际 %q", got)
	}
	if got := layerIdentifier(doc, 0x99); got != "handle-99" {
		t.Fatalf("无记录图层应回退句柄标识，实际 %q", got)
	}
	for _, in := range []string{"handle-99", "HANDLE-99", "handle-9f", "handle-9F"} {
		got := normalizeLayerSelector(in)
		if !strings.HasPrefix(got, "handle-") {
			t.Fatalf("%q 归一化异常: %q", in, got)
		}
	}
	if normalizeLayerSelector("handle-ab") != "handle-AB" {
		t.Fatal("句柄标识 hex 应统一大写")
	}
	if normalizeLayerSelector("PIPE") != "PIPE" {
		t.Fatal("图层名不应被归一化改动")
	}
}

// TestLayerFilterWhitelistExclude 白名单/排除/先白后排除/空名单语义，
// 覆盖嵌套块最终图层判定与同名图层合并。
func TestLayerFilterWhitelistExclude(t *testing.T) {
	doc := nestedLayerDoc()

	// 无选项/空名单：全部保留（嵌套块 3 图元：BASE 线/PIPE 线/无名文本）
	if got := primitiveLayers(t, doc, RenderOptions{}); len(got) != 3 {
		t.Fatalf("不过滤应保留 3 个嵌套展开图元，实际 %d（%v）", len(got), got)
	}
	if got := primitiveLayers(t, doc, RenderOptions{IncludeLayers: []string{}, ExcludeLayers: []string{}}); len(got) != 3 {
		t.Fatalf("空名单应不限制，实际保留 %d", len(got))
	}

	// 白名单：只保留 PIPE 图层（块 B 内部图元按最终图层命中）
	got := primitiveLayers(t, doc, RenderOptions{IncludeLayers: []string{"PIPE"}})
	if len(got) != 1 || got[0] != 0x20 {
		t.Fatalf("白名单 PIPE 应只保留 0x20，实际 %v", got)
	}

	// 白名单 + 无名图层句柄标识（大小写均接受）：保留 PIPE 线 + 块 B 文本
	for _, sel := range []string{"handle-99", "handle-99"} {
		got = primitiveLayers(t, doc, RenderOptions{IncludeLayers: []string{"PIPE", sel}})
		if len(got) != 2 || got[0] != 0x20 || got[1] != 0x99 {
			t.Fatalf("白名单 PIPE+%s 应保留 [0x20 0x99]，实际 %v", sel, got)
		}
	}

	// 排除名单：剔除 BASE 后剩 PIPE 线与无名文本
	got = primitiveLayers(t, doc, RenderOptions{ExcludeLayers: []string{"BASE"}})
	if len(got) != 2 {
		t.Fatalf("排除 BASE 应保留 2 个图元，实际 %v", got)
	}
	for _, h := range got {
		if h == 0x10 {
			t.Fatalf("BASE 不应出现: %v", got)
		}
	}
	// 按句柄标识排除无名图层文本
	got = primitiveLayers(t, doc, RenderOptions{ExcludeLayers: []string{"handle-99"}})
	if len(got) != 2 {
		t.Fatalf("按句柄排除后应保留 2 个图元，实际 %v", got)
	}
	for _, h := range got {
		if h == 0x99 {
			t.Fatalf("handle-99 文本应被排除: %v", got)
		}
	}

	// 白名单与排除同时给出：先白后排除——白名单 [BASE PIPE] 再排除 PIPE
	got = primitiveLayers(t, doc, RenderOptions{
		IncludeLayers: []string{"BASE", "PIPE"},
		ExcludeLayers: []string{"PIPE"},
	})
	if len(got) != 1 || got[0] != 0x10 {
		t.Fatalf("先白后排除应只剩 BASE(0x10)，实际 %v", got)
	}
	// 排除名单无法把白名单外的图元加回来
	got = primitiveLayers(t, doc, RenderOptions{
		IncludeLayers: []string{"BASE"},
		ExcludeLayers: []string{"PIPE"},
	})
	if len(got) != 1 || got[0] != 0x10 {
		t.Fatalf("白名单 BASE + 排除 PIPE 应仍只有 BASE，实际 %v", got)
	}
}

// TestLayerFilterSameNameHandles 同名图层：两个句柄共用一个图层名时，
// 按名过滤应同时命中/排除（同名合并，与 SVG 同图层同组口径一致）。
func TestLayerFilterSameNameHandles(t *testing.T) {
	doc := &drawing.Document{
		ModelSpace: []any{
			layerLine(0x40, 0, 0, 10, 0),
			layerLine(0x50, 20, 0, 30, 0),
			layerLine(0x10, 40, 0, 50, 0),
		},
		LayerColors: map[uint64]drawing.LayerColor{
			0x40: {Name: "DUP"},
			0x50: {Name: "DUP"}, // 另一句柄同名
			0x10: {Name: "OTHER"},
		},
	}
	got := primitiveLayers(t, doc, RenderOptions{IncludeLayers: []string{"DUP"}})
	if len(got) != 2 {
		t.Fatalf("同名 DUP 的两个句柄都应命中，实际 %v", got)
	}
	for _, h := range got {
		if h != 0x40 && h != 0x50 {
			t.Fatalf("只应有 0x40/0x50，实际 %x", h)
		}
	}
	got = primitiveLayers(t, doc, RenderOptions{ExcludeLayers: []string{"DUP"}})
	if len(got) != 1 || got[0] != 0x10 {
		t.Fatalf("按名排除应同时剔除两个同名句柄，实际 %v", got)
	}
}

// TestLayerFilterUnknownName 未知图层标识：白名单/排除名单中的未知名
// 都返回错误且错误包含该名称；多个未知名去重并全部带出；已声明但无图元
// 的图层名属于已知（输出为空但不报错）。
func TestLayerFilterUnknownName(t *testing.T) {
	doc := nestedLayerDoc()
	check := func(opts RenderOptions, want ...string) {
		t.Helper()
		prims := drawing.NewTessellator(doc).ExpandAll()
		_, err := newLayerFilter(doc, prims, opts)
		if err == nil {
			t.Fatalf("未知名应报错: %+v", opts)
		}
		for _, w := range want {
			if !strings.Contains(err.Error(), w) {
				t.Fatalf("错误应包含 %q，实际: %v", w, err)
			}
		}
	}
	check(RenderOptions{IncludeLayers: []string{"GHOST"}}, "GHOST")
	check(RenderOptions{ExcludeLayers: []string{"NOT-A-LAYER"}}, "NOT-A-LAYER")
	check(RenderOptions{
		IncludeLayers: []string{"BASE", "ZZZ"},
		ExcludeLayers: []string{"AAA", "handle-7f"}, // 0x7f 无任何引用 → 未知
	}, "ZZZ", "AAA", "handle-7f")

	// 句柄 hex 大小写不同不应误报未知
	prims := drawing.NewTessellator(doc).ExpandAll()
	if _, err := newLayerFilter(doc, prims, RenderOptions{IncludeLayers: []string{"handle-99"}}); err != nil {
		t.Fatalf("小写 hex 句柄标识应可匹配: %v", err)
	}

	// 已声明图层即使没有任何图元也属已知：白名单合法、结果为空
	emptyDoc := &drawing.Document{
		ModelSpace:  []any{layerLine(0x10, 0, 0, 1, 1)},
		LayerColors: map[uint64]drawing.LayerColor{0x10: {Name: "BASE"}, 0x20: {Name: "DECLARED"}},
	}
	got := primitiveLayers(t, emptyDoc, RenderOptions{IncludeLayers: []string{"DECLARED"}})
	if len(got) != 0 {
		t.Fatalf("白名单为无图元的已声明图层时应输出空，实际 %v", got)
	}
}

// filterEquivDocs 生成「多图层文档」与「只保留 BASE 图层的等价文档」，
// 用于断言过滤后 PNG/SVG 与仅含该图层的文档渲染字节一致。
func filterEquivDocs() (full, onlyBase *drawing.Document) {
	lc := map[uint64]drawing.LayerColor{
		0x10: {Name: "BASE"},
		0x20: {Name: "PIPE", Index: 1},
		0x30: {Name: "NOTE"},
	}
	full = &drawing.Document{
		ModelSpace: []any{
			layerLine(0x10, 0, 0, 100, 0),
			layerLine(0x20, 0, 300, 100, 300), // 远离 BASE：参与视口会稀释画面
			layerText(0x30, "NOTE", 0, 310, 8),
		},
		LayerColors: lc,
	}
	onlyBase = &drawing.Document{
		ModelSpace: []any{
			layerLine(0x10, 0, 0, 100, 0),
		},
		LayerColors: map[uint64]drawing.LayerColor{0x10: {Name: "BASE"}},
	}
	return full, onlyBase
}

// TestRenderPNGLayerFilterEquivalent PNG 白名单过滤后的画面与只含该
// 图层的文档完全一致（视口、几何、颜色同管线）；排除名单剔除内容。
func TestRenderPNGLayerFilterEquivalent(t *testing.T) {
	full, onlyBase := filterEquivDocs()
	want, err := RenderPNG(onlyBase, RenderOptions{Width: 512})
	if err != nil {
		t.Fatal(err)
	}
	got, err := RenderPNG(full, RenderOptions{Width: 512, IncludeLayers: []string{"BASE"}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, got) {
		t.Fatal("白名单 BASE 的 PNG 应与仅含 BASE 文档字节一致（视口只拟合输出内容）")
	}
	// 无过滤的全图必须与白名单结果不同（NOTE/PIPE 改变视口与画面）
	all, err := RenderPNG(full, RenderOptions{Width: 512})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(all, got) {
		t.Fatal("含全部图层的 PNG 不应与白名单结果相同")
	}
	// 排除名单同样剔除：排除 PIPE/NOTE 后只剩 BASE，结果一致
	ex, err := RenderPNG(full, RenderOptions{Width: 512, ExcludeLayers: []string{"PIPE", "NOTE"}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, ex) {
		t.Fatal("排除 PIPE/NOTE 后 PNG 应与仅含 BASE 文档一致")
	}
	if img, err := png.Decode(bytes.NewReader(got)); err != nil || img.Bounds().Dx() != 512 {
		t.Fatalf("过滤后 PNG 应可解码且宽 512: %v", err)
	}
}

// TestRenderSVGLayerFilterGroupsMetadata SVG 过滤：<g id="layer:*">
// 分组与 <metadata> 图层清单/图元数/文本数只反映实际输出内容。
func TestRenderSVGLayerFilterGroupsMetadata(t *testing.T) {
	full, onlyBase := filterEquivDocs()

	// 白名单 BASE：分组只有 BASE，metadata 图层清单/图元数同步
	svg, err := RenderSVG(full, RenderOptions{Width: 512, IncludeLayers: []string{"BASE"}})
	if err != nil {
		t.Fatal(err)
	}
	s := string(svg)
	if !strings.Contains(s, `id="layer:BASE"`) {
		t.Fatalf("白名单 SVG 应含 BASE 组:\n%s", s)
	}
	for _, gone := range []string{`id="layer:PIPE"`, `id="layer:NOTE"`, `id="layer:handle-`} {
		if strings.Contains(s, gone) {
			t.Fatalf("被过滤图层不应出现分组 %q:\n%s", gone, s)
		}
	}
	if strings.Contains(s, ">NOTE</text>") {
		t.Fatalf("被过滤 NOTE 文本不应输出:\n%s", s)
	}
	mm := svgMetaJSON(t, s)
	if mm["entities"] != float64(1) || mm["texts"] != float64(0) {
		t.Fatalf("metadata 图元/文本数应为 1/0: %v", mm)
	}
	ls, _ := mm["layers"].([]any)
	if len(ls) != 1 || ls[0] != "BASE" {
		t.Fatalf("metadata layers 应只含 BASE: %v", mm["layers"])
	}
	svgWellFormed(t, svg)

	// 与仅含 BASE 文档的 SVG 字节一致
	want, err := RenderSVG(onlyBase, RenderOptions{Width: 512})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, svg) {
		t.Fatal("白名单 BASE 的 SVG 应与仅含 BASE 文档字节一致")
	}

	// 排除 PIPE：BASE 线 + NOTE 文本输出，分组/清单不含 PIPE，文本计数 1
	svg2, err := RenderSVG(full, RenderOptions{Width: 512, ExcludeLayers: []string{"PIPE"}})
	if err != nil {
		t.Fatal(err)
	}
	s2 := string(svg2)
	if strings.Contains(s2, `id="layer:PIPE"`) {
		t.Fatalf("排除后 PIPE 不应成组:\n%s", s2)
	}
	if !strings.Contains(s2, `id="layer:BASE"`) || !strings.Contains(s2, ">NOTE</text>") {
		t.Fatalf("BASE 与 NOTE 应保留:\n%s", s2)
	}
	mm2 := svgMetaJSON(t, s2)
	if mm2["entities"] != float64(2) || mm2["texts"] != float64(1) {
		t.Fatalf("metadata 图元/文本数应为 2/1: %v", mm2)
	}
	layers := mm2["layers"].([]any)
	if len(layers) != 2 {
		t.Fatalf("metadata layers 应为 [BASE NOTE]，实际 %v", mm2["layers"])
	}

	// 无名图层按 handle-99 排除：分组与清单均不含该图层
	doc := nestedLayerDoc()
	svg3, err := RenderSVG(doc, RenderOptions{Width: 512, ExcludeLayers: []string{"handle-99"}})
	if err != nil {
		t.Fatal(err)
	}
	s3 := string(svg3)
	if strings.Contains(s3, "handle-99") {
		t.Fatalf("被排除的无名图层不应出现在分组/metadata: %s", s3[strings.Index(s3, "<metadata>"):])
	}
	if !strings.Contains(s3, `id="layer:BASE"`) || !strings.Contains(s3, `id="layer:PIPE"`) {
		t.Fatalf("BASE/PIPE 分组应保留:\n%s", s3)
	}
	svgWellFormed(t, svg3)
}

// TestRenderLayerFilterUnknownAllEntries 四个公共入口 + 全部图框批量
// 对未知名都返回包含名称的错误（同一套校验）。
func TestRenderLayerFilterUnknownAllEntries(t *testing.T) {
	doc := nestedLayerDoc()
	sheet := Sheet{Name: "S", Box: [4]float64{0, 0, 100, 50}}
	opts := RenderOptions{Width: 256, IncludeLayers: []string{"GHOST"}}
	check := func(name string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "GHOST") {
			t.Fatalf("%s 应返回含 GHOST 的错误，实际 %v", name, err)
		}
	}
	if _, err := RenderPNG(doc, opts); err == nil {
		t.Fatal("RenderPNG 未知名应报错")
	} else {
		check("RenderPNG", err)
	}
	_, err := RenderSVG(doc, opts)
	check("RenderSVG", err)
	_, err = RenderSheetPNG(doc, sheet, opts)
	check("RenderSheetPNG", err)
	_, err = RenderSheetSVG(doc, sheet, opts)
	check("RenderSheetSVG", err)
	_, err = RenderAllSheets(doc, opts, "png")
	check("RenderAllSheets", err)
	_, err = RenderAllSheets(doc, opts, "svg")
	check("RenderAllSheets(svg)", err)
}

// TestLayerFilterNoopByteIdentical 不传任何过滤条件与显式空名单的输出
// 必须字节一致（PNG/SVG/单图框/全部图框），保证与当前版本行为零变化。
func TestLayerFilterNoopByteIdentical(t *testing.T) {
	full, _ := filterEquivDocs()
	empty := RenderOptions{
		Width:         512,
		IncludeLayers: []string{},
		ExcludeLayers: []string{},
	}
	pngA, err := RenderPNG(full, RenderOptions{Width: 512})
	if err != nil {
		t.Fatal(err)
	}
	pngB, err := RenderPNG(full, empty)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pngA, pngB) {
		t.Fatal("PNG 空名单应与不传过滤字节一致")
	}
	svgA, err := RenderSVG(full, RenderOptions{Width: 512})
	if err != nil {
		t.Fatal(err)
	}
	svgB, err := RenderSVG(full, empty)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(svgA, svgB) {
		t.Fatal("SVG 空名单应与不传过滤字节一致")
	}

	// 单图框（显式宽度，无重渲）
	sheet := Sheet{Name: "S", Box: [4]float64{-50, -50, 350, 350}}
	sp1, err := prepareSheetsFiltered(full, RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sp2, err := prepareSheetsFiltered(full, empty)
	if err != nil {
		t.Fatal(err)
	}
	if len(sp1.prims) != len(sp2.prims) {
		t.Fatalf("空名单管线图元数不一致 %d/%d", len(sp1.prims), len(sp2.prims))
	}
	d1, _, r1, n1, err := sp1.renderPNG(sheet, RenderOptions{Width: 256})
	if err != nil {
		t.Fatal(err)
	}
	d2, _, r2, n2, err := sp2.renderPNG(sheet, RenderOptions{Width: 256})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(d1, d2) || r1 != r2 || n1 != n2 {
		t.Fatal("单图框 PNG 空名单应与不过滤一致（含达标率/重渲次数）")
	}
	v1, _, err := sp1.renderSVG(sheet, RenderOptions{Width: 256})
	if err != nil {
		t.Fatal(err)
	}
	v2, _, err := sp2.renderSVG(sheet, RenderOptions{Width: 256})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(v1, v2) {
		t.Fatal("单图框 SVG 空名单应与不过滤一致")
	}

	// 全部图框（无图框兜底单张；显式小宽度避免自适应放大）
	sheetsEmpty := RenderOptions{Width: 256, IncludeLayers: []string{}, ExcludeLayers: []string{}}
	ra, err := RenderAllSheets(full, RenderOptions{Width: 256}, "png")
	if err != nil {
		t.Fatal(err)
	}
	rb, err := RenderAllSheets(full, sheetsEmpty, "png")
	if err != nil {
		t.Fatal(err)
	}
	if len(ra) != len(rb) {
		t.Fatalf("全部图框张数不一致 %d/%d", len(ra), len(rb))
	}
	for i := range ra {
		if ra[i].Name != rb[i].Name || !bytes.Equal(ra[i].Data, rb[i].Data) {
			t.Fatalf("第 %d 张全部图框结果空名单不一致", i)
		}
	}
}

// TestSheetLayerFilterPassRate 单图框清晰度统计与自适应宽度都不得计入
// 被图层过滤排除的文字：框内正常文字（50 世界单位）+ 微缩文字
// （1 世界单位），显式 1000 宽 scale=1 时微缩文字 1px 不达标；
// 排除其图层后达标率恢复 1，自适应起步宽度也不再被微缩文字放大。
func TestSheetLayerFilterPassRate(t *testing.T) {
	doc := &drawing.Document{
		ModelSpace: []any{
			layerText(0x10, "OK", 250, 500, 50), // 正常标注：1000 宽下 50px
			layerText(0x20, "X", 750, 500, 1),   // 微缩标注：1px，不达标
			layerLine(0x10, 0, 0, 1000, 0),      // 视口基准几何
		},
		LayerColors: map[uint64]drawing.LayerColor{
			0x10: {Name: "KEEP"},
			0x20: {Name: "TINY"},
		},
	}
	sheet := Sheet{Name: "框", Box: [4]float64{0, 0, 1000, 1000}}

	// 不过滤：两条文本一达标一不达标 → 0.5
	sp := prepareSheets(doc)
	_, width, rate, _, err := sp.renderPNG(sheet, RenderOptions{Width: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if width != 1000 {
		t.Fatalf("显式宽度应为 1000，实际 %d", width)
	}
	if rate < 0.49 || rate > 0.51 {
		t.Fatalf("未过滤达标率应 0.5，实际 %v", rate)
	}

	// 排除 TINY：清晰度统计只剩达标文本 → 1（被过滤文字不算入）
	spf, err := prepareSheetsFiltered(doc, RenderOptions{ExcludeLayers: []string{"TINY"}})
	if err != nil {
		t.Fatal(err)
	}
	_, width, rate, rerenders, err := spf.renderPNG(sheet, RenderOptions{Width: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if rate != 1 {
		t.Fatalf("排除微缩文字后达标率应为 1，实际 %v", rate)
	}
	if rerenders != 0 {
		t.Fatalf("全达标不应重渲，实际 %d 次", rerenders)
	}

	// 白名单 KEEP 同口径（先白后排除语义在单入口同样成立）
	spw, err := prepareSheetsFiltered(doc, RenderOptions{IncludeLayers: []string{"KEEP"}})
	if err != nil {
		t.Fatal(err)
	}
	_, _, rate, _, err = spw.renderPNG(sheet, RenderOptions{Width: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if rate != 1 {
		t.Fatalf("白名单 KEEP 达标率应为 1，实际 %v", rate)
	}

	// 自适应宽度：微缩文字会把起步宽度推到 8320（p25=1 世界单位，
	// 视口 1040 世界单位（图框外扩 2%）× 8px），排除后正常文字只需
	// 钳制下限 4096——证明直属文本也不参与宽度统计
	vp := sheetViewport(sheet.Box)
	wAll := sp.sheetWidthFor(vp, sheet, 0)
	wFiltered := spf.sheetWidthFor(vp, sheet, 0)
	if wAll != 8320 {
		t.Fatalf("含微缩文字自适应宽度应 8320，实际 %d", wAll)
	}
	if wFiltered != SheetDefaultWidth {
		t.Fatalf("排除微缩文字后自适应宽度应回落下限 %d，实际 %d", SheetDefaultWidth, wFiltered)
	}
}

// TestRenderAllSheetsLayerFilter 全部图框批量结果：被排除图层的内容
// 不出现在任何一张 SVG（分组与 metadata）中，过滤不改变图框识别。
func TestRenderAllSheetsLayerFilter(t *testing.T) {
	doc := buildSheetDoc(t, []struct {
		h        uint64
		name     string
		title    string
		offset   [2]float64
		W, hgt   float64
		scale    float64
		rotation float64
	}{
		aSeriesSheet(100, "A1图框", "配电系统图", [2]float64{0, 0}),
		aSeriesSheet(101, "A1图框", "照明平面图", [2]float64{1500, 0}),
	})
	doc.LayerColors[0x90] = drawing.LayerColor{Name: "PIPE", Index: 3}
	// 每个图框内加一条 PIPE 图层的直属管线
	for _, off := range [][2]float64{{0, 0}, {1500, 0}} {
		doc.ModelSpace = append(doc.ModelSpace, layerLine(0x90,
			off[0]+200, off[1]+400, off[0]+900, off[1]+400))
	}

	all, err := RenderAllSheets(doc, RenderOptions{Width: 800}, "svg")
	if err != nil {
		t.Fatal(err)
	}
	filtered, err := RenderAllSheets(doc, RenderOptions{Width: 800, ExcludeLayers: []string{"PIPE"}}, "svg")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != len(filtered) {
		t.Fatalf("过滤不应改变图框张数：%d/%d", len(all), len(filtered))
	}
	sawPipe := false
	for i := range all {
		if all[i].Name != filtered[i].Name {
			t.Fatalf("第 %d 张图名不一致 %q/%q", i, all[i].Name, filtered[i].Name)
		}
		if !strings.Contains(string(all[i].Data), `id="layer:PIPE"`) {
			t.Fatalf("未过滤结果第 %d 张 %q 应含 PIPE 组", i, all[i].Name)
		}
		sawPipe = true
		if strings.Contains(string(filtered[i].Data), `id="layer:PIPE"`) {
			t.Fatalf("过滤结果第 %d 张 %q 不应含 PIPE 组", i, filtered[i].Name)
		}
		mm := svgMetaJSON(t, string(filtered[i].Data))
		if layers, ok := mm["layers"].([]any); ok {
			for _, l := range layers {
				if l == "PIPE" {
					t.Fatalf("第 %d 张 metadata 不应含 PIPE: %v", i, mm["layers"])
				}
			}
		}
	}
	if !sawPipe {
		t.Fatal("测试前提：未过滤结果中应出现过 PIPE 组")
	}

	// PNG 批量同样生效：未知名整体报错
	if _, err := RenderAllSheets(doc, RenderOptions{Width: 800, IncludeLayers: []string{"MISSING"}}, "png"); err == nil ||
		!strings.Contains(err.Error(), "MISSING") {
		t.Fatalf("批量 PNG 未知名应报错且含名称，实际 %v", err)
	}
}
