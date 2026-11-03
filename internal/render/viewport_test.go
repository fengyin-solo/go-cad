// viewport_test.go 覆盖可选世界坐标视口：横向/纵向区域的画布尺寸与
// 严格坐标映射（含负坐标）、跨边界线段的世界坐标裁剪、文字按实际占位
// 范围的相交判定、SVG viewBox 精确对应、单图框入口的视口覆盖、非法
// 视口错误以及未设置视口时的旧行为兼容（自动包围盒 + 5% 边距）。
package render

import (
	"bytes"
	"image"
	"image/png"
	"math"
	"strings"
	"testing"

	"github.com/unitedrhino/go-cad/internal/drawing"
	"github.com/unitedrhino/go-cad/internal/entity"
)

// vpLineDoc 构造单 LINE 合成文档（线段由用例给定）。
func vpLineDoc(x1, y1, x2, y2 float64) *drawing.Document {
	return &drawing.Document{ModelSpace: []any{
		&entity.EntLine{Start: entity.Point3{X: x1, Y: y1}, End: entity.Point3{X: x2, Y: y2}},
	}}
}

// decodePNG 解码 PNG 字节流。
func decodePNG(t *testing.T, data []byte) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("PNG 解码失败: %v", err)
	}
	return img
}

// darkAt 像素是否为深色墨迹（白底黑线）。
func darkAt(img image.Image, x, y int) bool {
	r, g, b, _ := img.At(x, y).RGBA()
	return r+g+b < 3*30000
}

// countDarkColumn 统计一列上的深色像素数。
func countDarkColumn(img image.Image, x int) int {
	b := img.Bounds()
	n := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		if darkAt(img, x, y) {
			n++
		}
	}
	return n
}

// TestViewportPNGLandscape 横向区域：宽由调用方指定，高严格按视口
// 宽高比确定；跨边界对角线被裁到视口内，四角映射到手算像素位置。
func TestViewportPNGLandscape(t *testing.T) {
	// 对角线 (-100,-100)→(200,200)，视口 (0,0)-(100,50) 横向：
	// 裁剪后 (0,0)→(50,50)。
	doc := vpLineDoc(-100, -100, 200, 200)
	data, err := RenderPNG(doc, RenderOptions{
		Width:    1000,
		Viewport: &Viewport{MinX: 0, MinY: 0, MaxX: 100, MaxY: 50},
	})
	if err != nil {
		t.Fatal(err)
	}
	img := decodePNG(t, data)
	b := img.Bounds()
	if b.Dx() != 1000 || b.Dy() != 500 {
		t.Fatalf("横向视口画布应为 1000x500, got %dx%d", b.Dx(), b.Dy())
	}
	// 世界 (0,0) → 像素 (0,499)；世界 (50,50) → (500,0)；中点 (25,25) → (250,250)
	if !darkAt(img, 0, 499) {
		t.Fatal("世界 (0,0) 左下角应有墨迹")
	}
	if !darkAt(img, 500, 0) {
		t.Fatal("世界 (50,50) 处应有墨迹")
	}
	if !darkAt(img, 250, 250) {
		t.Fatal("对角线中点应有墨迹")
	}
	// 裁掉的部分：世界 (-50,-50) 对应视口外，不应出现在画布上
	if darkAt(img, 999, 499) {
		t.Fatal("视口右下角 (100,0) 无几何，应为背景")
	}
}

// TestViewportPNGPortrait 纵向区域：高度按视口宽高比大于宽度。
func TestViewportPNGPortrait(t *testing.T) {
	doc := vpLineDoc(0, 0, 50, 100)
	data, err := RenderPNG(doc, RenderOptions{
		Width:    500,
		Viewport: &Viewport{MinX: 0, MinY: 0, MaxX: 50, MaxY: 100},
	})
	if err != nil {
		t.Fatal(err)
	}
	img := decodePNG(t, data)
	b := img.Bounds()
	if b.Dx() != 500 || b.Dy() != 1000 {
		t.Fatalf("纵向视口画布应为 500x1000, got %dx%d", b.Dx(), b.Dy())
	}
	// (0,0)→(0,999)、(50,100)→(499,0)
	if !darkAt(img, 0, 999) || !darkAt(img, 499, 0) {
		t.Fatal("纵向视口两端映射不符")
	}
}

// TestViewportPNGNegative 负坐标区域：世界→像素平移严格对应视口原点。
func TestViewportPNGNegative(t *testing.T) {
	// 竖线 x=-75 自视口底到顶，视口 (-100,-100)-(-50,-50)，宽 500 → scale 10。
	doc := vpLineDoc(-75, -100, -75, -50)
	data, err := RenderPNG(doc, RenderOptions{
		Width:    500,
		Viewport: &Viewport{MinX: -100, MinY: -100, MaxX: -50, MaxY: -50},
	})
	if err != nil {
		t.Fatal(err)
	}
	img := decodePNG(t, data)
	if img.Bounds().Dx() != 500 || img.Bounds().Dy() != 500 {
		t.Fatalf("正方形负坐标视口应为 500x500, got %dx%d", img.Bounds().Dx(), img.Bounds().Dy())
	}
	// 世界 x=-75 → 像素列 (-75+100)*10 = 250；该列应有大量墨迹
	if n := countDarkColumn(img, 250); n < 400 {
		t.Fatalf("x=-75 映射列墨迹不足 %d", n)
	}
	if n := countDarkColumn(img, 100); n > 0 {
		t.Fatalf("x=-90 列应无几何, got %d 暗像素", n)
	}
}

// TestViewportSVGViewBoxAndRatio SVG viewBox 必须严格等于视口世界尺寸，
// width 由调用方指定、height 按宽高比（横向/纵向各一），且不再有 5% 边距。
func TestViewportSVGViewBoxAndRatio(t *testing.T) {
	doc := vpLineDoc(0, 0, 100, 50)
	// 横向
	s, err := RenderSVG(doc, RenderOptions{
		Width:    1000,
		Viewport: &Viewport{MinX: 0, MinY: 0, MaxX: 100, MaxY: 50},
	})
	if err != nil {
		t.Fatal(err)
	}
	str := string(s)
	if !strings.Contains(str, `viewBox="0 0 100 50"`) {
		t.Fatalf("viewBox 应严格为 0 0 100 50:\n%s", str[:min(len(str), 300)])
	}
	if !strings.Contains(str, `width="1000" height="500"`) {
		t.Fatalf("SVG 宽高应为 1000x500:\n%s", str[:min(len(str), 300)])
	}
	// 无 5% 边距：线段 (0,0)-(100,50) 两端严格落在 viewBox 两角
	// （世界 (0,0)→SVG (0,50)）；旧自动范围路径首点为 "M5 52.5"。
	if !strings.Contains(str, "M0 50l100-50") {
		t.Fatalf("视口映射应无 5%% 边距（端点严格落在 viewBox 角上）:\n%s", str)
	}
	svgWellFormed(t, s)

	// 纵向
	s2, err := RenderSVG(doc, RenderOptions{
		Width:    500,
		Viewport: &Viewport{MinX: 0, MinY: 0, MaxX: 50, MaxY: 100},
	})
	if err != nil {
		t.Fatal(err)
	}
	str2 := string(s2)
	if !strings.Contains(str2, `viewBox="0 0 50 100"`) {
		t.Fatalf("纵向 viewBox 应为 0 0 50 100:\n%s", str2[:min(len(str2), 300)])
	}
	if !strings.Contains(str2, `width="500" height="1000"`) {
		t.Fatalf("纵向 SVG 宽高应为 500x1000:\n%s", str2[:min(len(str2), 300)])
	}
	svgWellFormed(t, s2)
}

// TestViewportSVGNegative 负坐标视口的 SVG 坐标换算：平移基准为
// MinX/MaxY，视口内竖线输出位置手算可验。
func TestViewportSVGNegative(t *testing.T) {
	doc := vpLineDoc(-75, -100, -75, -50)
	s, err := RenderSVG(doc, RenderOptions{
		Width:    500,
		Viewport: &Viewport{MinX: -100, MinY: -100, MaxX: -50, MaxY: -50},
	})
	if err != nil {
		t.Fatal(err)
	}
	str := string(s)
	// (-75,-50) → 毫坐标 (25000,50000) = (25,50)；(-75,-100) → (25,0)
	if !strings.Contains(str, "M25 50") || !strings.Contains(str, "v-50") {
		t.Fatalf("负坐标视口竖线映射不符:\n%s", str)
	}
	if !strings.Contains(str, `viewBox="0 0 50 50"`) {
		t.Fatalf("负坐标视口 viewBox 尺寸不符:\n%s", str)
	}
	svgWellFormed(t, s)
}

// TestViewportLineClipSVG 跨越视口边界的线段必须在世界坐标内裁到边界：
// 水平/垂直/对角三类跨越，输出 path 端点严格落在视口边上；完全在外的
// 线段不产生 path。
func TestViewportLineClipSVG(t *testing.T) {
	doc := &drawing.Document{ModelSpace: []any{
		&entity.EntLine{Start: entity.Point3{X: -100, Y: 25}, End: entity.Point3{X: 200, Y: 25}},    // 水平跨越
		&entity.EntLine{Start: entity.Point3{X: 25, Y: -100}, End: entity.Point3{X: 25, Y: 200}},    // 垂直跨越
		&entity.EntLine{Start: entity.Point3{X: -100, Y: -100}, End: entity.Point3{X: 200, Y: 200}}, // 对角跨越
		&entity.EntLine{Start: entity.Point3{X: 300, Y: 300}, End: entity.Point3{X: 400, Y: 400}},   // 完全在外
	}}
	s, err := RenderSVG(doc, RenderOptions{
		Width:    1000,
		Viewport: &Viewport{MinX: 0, MinY: 0, MaxX: 100, MaxY: 50},
	})
	if err != nil {
		t.Fatal(err)
	}
	str := string(s)
	// 水平线裁为 (0,25)→(100,25)：毫坐标 (0,25000)→(100000,25000)
	if !strings.Contains(str, "M0 25") || !strings.Contains(str, "h 100") {
		t.Fatalf("水平跨越段应裁为 M0 25 h 100:\n%s", str)
	}
	// 垂直线原序自下而上，裁为 (25,0)→(25,50)：输出从 SVG 上端
	// (25,0) 走 v-50 到 (25,50)
	if !strings.Contains(str, "M25 50") || !strings.Contains(str, "v-50") {
		t.Fatalf("垂直跨越段应裁为 M25 50 v-50:\n%s", str)
	}
	// 对角线段在 y=50 边出界（早于 x=100），裁为 (0,0)→(50,50)
	if !strings.Contains(str, "l50-50") {
		t.Fatalf("对角跨越段应裁为 (0,0)→(50,50):\n%s", str)
	}
	// 完全在外的线段不得留下任何 300/400 世界坐标（毫坐标 300000+ ）
	if strings.Contains(str, "300000") || strings.Contains(str, "400000") {
		t.Fatalf("视口外线段不应输出:\n%s", str)
	}
	svgWellFormed(t, s)
}

// TestClipStrokeToBoxUnit 裁剪算法的直接单元测试：内/外/四边跨越/
// 平行在边外/端点贴边/负坐标。
func TestClipStrokeToBoxUnit(t *testing.T) {
	b := drawing.Box2{MinX: 0, MinY: 0, MaxX: 100, MaxY: 50}
	cases := []struct {
		name           string
		s              drawing.Stroke
		ok             bool
		x1, y1, x2, y2 float64
	}{
		{"完全在内", drawing.Stroke{X1: 10, Y1: 10, X2: 20, Y2: 20}, true, 10, 10, 20, 20},
		{"完全在右外", drawing.Stroke{X1: 110, Y1: 10, X2: 120, Y2: 20}, false, 0, 0, 0, 0},
		{"完全在上外", drawing.Stroke{X1: 10, Y1: 60, X2: 20, Y2: 70}, false, 0, 0, 0, 0},
		{"水平跨越", drawing.Stroke{X1: -50, Y1: 25, X2: 150, Y2: 25}, true, 0, 25, 100, 25},
		{"垂直跨越", drawing.Stroke{X1: 25, Y1: -50, X2: 25, Y2: 100}, true, 25, 0, 25, 50},
		{"右边界切入", drawing.Stroke{X1: 50, Y1: 25, X2: 150, Y2: 25}, true, 50, 25, 100, 25},
		{"左边界切出", drawing.Stroke{X1: -50, Y1: 25, X2: 50, Y2: 25}, true, 0, 25, 50, 25},
		{"平行边外", drawing.Stroke{X1: 10, Y1: 60, X2: 90, Y2: 60}, false, 0, 0, 0, 0},
		{"贴边平行在内", drawing.Stroke{X1: 0, Y1: 25, X2: 100, Y2: 25}, true, 0, 25, 100, 25},
		{"负坐标视口跨越", drawing.Stroke{X1: -110, Y1: -75, X2: -40, Y2: -75}, true, -100, -75, -50, -75},
	}
	nb := b
	for _, tc := range cases {
		if strings.HasPrefix(tc.name, "负坐标") {
			nb = drawing.Box2{MinX: -100, MinY: -100, MaxX: -50, MaxY: -50}
		} else {
			nb = b
		}
		got, ok := clipStrokeToBox(tc.s, nb)
		if ok != tc.ok {
			t.Fatalf("%s: ok=%v want %v", tc.name, ok, tc.ok)
		}
		const eps = 1e-9
		near := func(a, b float64) bool { return math.Abs(a-b) <= eps }
		if ok && !(near(got.X1, tc.x1) && near(got.Y1, tc.y1) && near(got.X2, tc.x2) && near(got.Y2, tc.y2)) {
			t.Fatalf("%s: clip=(%g,%g)-(%g,%g) want (%g,%g)-(%g,%g)",
				tc.name, got.X1, got.Y1, got.X2, got.Y2, tc.x1, tc.y1, tc.x2, tc.y2)
		}
	}
}

// TestViewportTextBounds 文字按实际占位范围判定相交：
//   - 完全在视口外的文字不输出；
//   - 锚点在视口外但字面占位与视口相交（沉降部/字面压边）的文字保留；
//   - 整体在视口外（即便离边不远）的文字剔除。
func TestViewportTextBounds(t *testing.T) {
	doc := &drawing.Document{ModelSpace: []any{
		&entity.EntText{Text: "INSIDE", Insertion: entity.Point3{X: 10, Y: 20}, Height: 2},    // 完整在内
		&entity.EntText{Text: "OUTSIDE", Insertion: entity.Point3{X: 200, Y: 200}, Height: 2}, // 完整在外
		// 锚点 (95,-1) 在视口外，但字面向上 2 单位、沉降 0.4 单位，
		// 占位 y∈[-1.4,1] 与视口 y∈[0,50] 相交（旧的锚点判定会误删）。
		&entity.EntText{Text: "CROSSEDGE", Insertion: entity.Point3{X: 95, Y: -1}, Height: 2},
		// 整体在视口下方 8 单位，占位与视口不相交。
		&entity.EntText{Text: "BELOW", Insertion: entity.Point3{X: 50, Y: -10}, Height: 2},
	}}
	s, err := RenderSVG(doc, RenderOptions{
		Width:    1000,
		Viewport: &Viewport{MinX: 0, MinY: 0, MaxX: 100, MaxY: 50},
	})
	if err != nil {
		t.Fatal(err)
	}
	str := string(s)
	for _, want := range []string{">INSIDE</text>", ">CROSSEDGE</text>"} {
		if !strings.Contains(str, want) {
			t.Fatalf("应保留与视口相交的文字 %q:\n%s", want, str)
		}
	}
	for _, gone := range []string{">OUTSIDE</text>", ">BELOW</text>"} {
		if strings.Contains(str, gone) {
			t.Fatalf("完全在视口外的文字 %q 不应输出:\n%s", gone, str)
		}
	}
	svgWellFormed(t, s)
}

// TestViewportTextRotatedBounds 旋转文字：占位为 OBB，文字整体在
// 视口外时即使 AABB 投影靠近也应剔除（OBB 判定而非锚点判定）。
func TestViewportTextRotatedBounds(t *testing.T) {
	doc := &drawing.Document{ModelSpace: []any{
		// 90° 旋转：文字沿 +Y 方向书写，占位压在视口顶边（相交保留）
		&entity.EntText{Text: "ROTIN", Insertion: entity.Point3{X: 10, Y: 45}, Height: 2, Rotation: math.Pi / 2},
		// 同方向但远在视口右侧（书写方向 +Y，不与视口相交）
		&entity.EntText{Text: "ROTOUT", Insertion: entity.Point3{X: 150, Y: 25}, Height: 2, Rotation: math.Pi / 2},
	}}
	s, err := RenderSVG(doc, RenderOptions{
		Width:    1000,
		Viewport: &Viewport{MinX: 0, MinY: 0, MaxX: 100, MaxY: 50},
	})
	if err != nil {
		t.Fatal(err)
	}
	str := string(s)
	if !strings.Contains(str, ">ROTIN</text>") {
		t.Fatalf("压边的旋转文字应保留:\n%s", str)
	}
	if strings.Contains(str, ">ROTOUT</text>") {
		t.Fatalf("视口外的旋转文字应剔除:\n%s", str)
	}
	svgWellFormed(t, s)
}

// TestViewportSheetClip 单图框入口携带视口：输出严格对应视口区域，
// 无视口时图框自带的 2% 外扩与按文字自适应宽度均不生效——PNG/SVG
// 两条路径都验。
func TestViewportSheetClip(t *testing.T) {
	doc := vpLineDoc(0, 0, 100, 50)
	sheet := Sheet{Name: "大图框", Box: [4]float64{-1000, -1000, 1000, 1000}}
	opts := RenderOptions{
		Width:    500,
		Viewport: &Viewport{MinX: 0, MinY: 0, MaxX: 100, MaxY: 50},
	}
	// SVG：viewBox 取视口而非外扩后的图框
	svg, err := RenderSheetSVG(doc, sheet, opts)
	if err != nil {
		t.Fatalf("RenderSheetSVG: %v", err)
	}
	str := string(svg)
	if !strings.Contains(str, `viewBox="0 0 100 50"`) {
		t.Fatalf("单图框 SVG viewBox 应严格为视口:\n%s", str[:min(len(str), 300)])
	}
	if !strings.Contains(str, `width="500" height="250"`) {
		t.Fatalf("单图框 SVG 宽高应按视口比例 500x250:\n%s", str[:min(len(str), 300)])
	}
	svgWellFormed(t, svg)
	// PNG：画布尺寸按视口（图框路径默认自适应会给 4096，这里必须 500x250）
	pngData, err := RenderSheetPNG(doc, sheet, opts)
	if err != nil {
		t.Fatalf("RenderSheetPNG: %v", err)
	}
	img := decodePNG(t, pngData)
	if img.Bounds().Dx() != 500 || img.Bounds().Dy() != 250 {
		t.Fatalf("单图框 PNG 应严格按视口 500x250, got %dx%d", img.Bounds().Dx(), img.Bounds().Dy())
	}
}

// TestViewportErrors 零宽、零高、反向区间与非有限坐标必须返回清晰
// 错误且不产出图像；三个入口（PNG/SVG/单图框）同口径。
func TestViewportErrors(t *testing.T) {
	doc := vpLineDoc(0, 0, 100, 100)
	sheet := Sheet{Box: [4]float64{0, 0, 100, 100}}
	bad := []struct {
		name string
		v    *Viewport
	}{
		{"零宽", &Viewport{MinX: 50, MinY: 0, MaxX: 50, MaxY: 100}},
		{"零高", &Viewport{MinX: 0, MinY: 50, MaxX: 100, MaxY: 50}},
		{"X 反向", &Viewport{MinX: 100, MinY: 0, MaxX: 0, MaxY: 100}},
		{"Y 反向", &Viewport{MinX: 0, MinY: 100, MaxX: 100, MaxY: 0}},
		{"NaN", &Viewport{MinX: math.NaN(), MinY: 0, MaxX: 100, MaxY: 100}},
		{"正无穷", &Viewport{MinX: 0, MinY: 0, MaxX: math.Inf(1), MaxY: 100}},
		{"负无穷", &Viewport{MinX: 0, MinY: math.Inf(-1), MaxX: 100, MaxY: 100}},
	}
	for _, tc := range bad {
		opts := RenderOptions{Width: 800, Viewport: tc.v}
		if data, err := RenderPNG(doc, opts); err == nil {
			t.Fatalf("%s: RenderPNG 应报错", tc.name)
		} else if data != nil {
			t.Fatalf("%s: RenderPNG 出错时不应返回图像", tc.name)
		} else if !strings.Contains(err.Error(), "cad: 视口") {
			t.Fatalf("%s: 错误信息不清晰: %v", tc.name, err)
		}
		if data, err := RenderSVG(doc, opts); err == nil {
			t.Fatalf("%s: RenderSVG 应报错", tc.name)
		} else if data != nil {
			t.Fatalf("%s: RenderSVG 出错时不应返回图像", tc.name)
		}
		if data, err := RenderSheetPNG(doc, sheet, opts); err == nil {
			t.Fatalf("%s: RenderSheetPNG 应报错", tc.name)
		} else if data != nil {
			t.Fatalf("%s: RenderSheetPNG 出错时不应返回图像", tc.name)
		}
		if data, err := RenderSheetSVG(doc, sheet, opts); err == nil {
			t.Fatalf("%s: RenderSheetSVG 应报错", tc.name)
		} else if data != nil {
			t.Fatalf("%s: RenderSheetSVG 出错时不应返回图像", tc.name)
		}
	}
}

// TestViewportEmptyRegion 合法视口内无任何图元时输出背景空图（不是
// 错误），尺寸仍严格按视口比例。
func TestViewportEmptyRegion(t *testing.T) {
	doc := vpLineDoc(0, 0, 10, 10)
	opts := RenderOptions{Width: 400, Viewport: &Viewport{MinX: 1000, MinY: 1000, MaxX: 1200, MaxY: 1100}}
	pngData, err := RenderPNG(doc, opts)
	if err != nil {
		t.Fatalf("空区域不应报错: %v", err)
	}
	img := decodePNG(t, pngData)
	if img.Bounds().Dx() != 400 || img.Bounds().Dy() != 200 {
		t.Fatalf("空区域画布应按视口比例 400x200, got %dx%d", img.Bounds().Dx(), img.Bounds().Dy())
	}
	if darkAt(img, 200, 100) {
		t.Fatal("空区域中心应为背景")
	}
	svg, err := RenderSVG(doc, opts)
	if err != nil {
		t.Fatalf("空区域 SVG 不应报错: %v", err)
	}
	if strings.Contains(string(svg), `<path`) {
		t.Fatal("空区域 SVG 不应含 path")
	}
	svgWellFormed(t, svg)
}

// TestViewportLegacyCompat 未设置视口时必须保持旧行为：自动包围盒 +
// 5% 边距。同一张图 (0,0)-(100,50)：旧路径首点 M5 52.5，视口路径
// 首点 M0 0；nil 与不传字段两种写法等价。
func TestViewportLegacyCompat(t *testing.T) {
	doc := vpLineDoc(0, 0, 100, 50)
	for _, opts := range []RenderOptions{
		{Width: 1000},
		{Width: 1000, Viewport: nil},
	} {
		s, err := RenderSVG(doc, opts)
		if err != nil {
			t.Fatal(err)
		}
		str := string(s)
		if !strings.Contains(str, "M5 52.5") {
			t.Fatalf("无视口应保持 5%% 边距旧行为（M5 52.5）:\n%s", str)
		}
		if !strings.Contains(str, `viewBox="0 0 110 55"`) {
			t.Fatalf("无视口 viewBox 应为含边距的 110x55:\n%s", str)
		}
		svgWellFormed(t, s)
	}
	// PNG 旧路径：自动包围盒 + 5% 边距（视口 (-5,-2.5)-(105,52.5)），
	// 世界 (0,0) 映射到约 (45,477) 而非严格视口下的画布角 (0,499)。
	data, err := RenderPNG(doc, RenderOptions{Width: 1000})
	if err != nil {
		t.Fatal(err)
	}
	img := decodePNG(t, data)
	if img.Bounds().Dx() != 1000 || img.Bounds().Dy() != 500 {
		t.Fatalf("旧行为画布尺寸应为 1000x500, got %dx%d", img.Bounds().Dx(), img.Bounds().Dy())
	}
	if darkAt(img, 0, 499) {
		t.Fatal("旧行为 5% 边距下画布左下角不应有墨迹")
	}
	if !darkAt(img, 45, 477) {
		t.Fatal("旧行为世界 (0,0) 应映射在边距后的约 (45,477)")
	}
}
