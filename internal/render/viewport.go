// viewport.go 实现可选的世界坐标渲染视口：调用方框选一块世界区域后，
// PNG 像素变换与 SVG viewBox 严格对应该区域，不再计算整图自动包围盒、
// 也不套用 5% 边距。图元按视口裁剪——完全在外的不输出，跨越边界的
// 线段在世界坐标内裁到视口内（Liang-Barsky），文字按实际占位有向
// 包围盒（OBB）与视口的相交判定整体保留或剔除。
//
// 未设置视口（RenderOptions.Viewport == nil）时本文件不参与渲染管线，
// RenderPNG/RenderSVG/RenderSheet* 全部保持历史自动范围 + 边距行为。
package render

import (
	"fmt"
	"math"
	"strings"

	"github.com/unitedrhino/go-cad/internal/drawing"
)

// Viewport 世界坐标视口：Min/Max 定义世界 XY 矩形（Y 轴向上，与 CAD
// 一致，允许负坐标）。设置后输出宽度仍由 RenderOptions.Width 指定，
// 高度按视口宽高比确定；PNG 世界→像素变换与 SVG viewBox 严格映射
// 该矩形，不做自动包围盒、不加 5% 边距。
type Viewport struct {
	MinX, MinY float64 // 视口左下角世界坐标
	MaxX, MaxY float64 // 视口右上角世界坐标
}

// box 转为内部包围盒表示。
func (v Viewport) box() drawing.Box2 {
	return drawing.Box2{MinX: v.MinX, MinY: v.MinY, MaxX: v.MaxX, MaxY: v.MaxY}
}

// resolveViewport 校验渲染选项中的视口。返回视口包围盒、是否设置了
// 视口；零宽/零高（含反向）或非有限坐标返回明确错误，由调用方在展开
// 图元前拦截，避免生成空图。
func resolveViewport(v *Viewport) (drawing.Box2, bool, error) {
	if v == nil {
		return drawing.Box2{}, false, nil
	}
	if math.IsNaN(v.MinX) || math.IsInf(v.MinX, 0) ||
		math.IsNaN(v.MinY) || math.IsInf(v.MinY, 0) ||
		math.IsNaN(v.MaxX) || math.IsInf(v.MaxX, 0) ||
		math.IsNaN(v.MaxY) || math.IsInf(v.MaxY, 0) {
		return drawing.Box2{}, true, fmt.Errorf(
			"cad: 视口坐标必须为有限数值（MinX=%v MinY=%v MaxX=%v MaxY=%v）",
			v.MinX, v.MinY, v.MaxX, v.MaxY)
	}
	if v.MaxX <= v.MinX {
		return drawing.Box2{}, true, fmt.Errorf(
			"cad: 视口宽度必须为正（MinX=%v MaxX=%v）", v.MinX, v.MaxX)
	}
	if v.MaxY <= v.MinY {
		return drawing.Box2{}, true, fmt.Errorf(
			"cad: 视口高度必须为正（MinY=%v MaxY=%v）", v.MinY, v.MaxY)
	}
	return v.box(), true, nil
}

// clipPrimsToViewport 按世界坐标视口裁剪展开图元：
//   - stroke 图元：逐段与视口求交（Liang-Barsky 线段裁剪），完全在
//     视口外的段剔除、跨越边界的段裁到视口边界，全部落空则丢弃图元；
//   - label 图元：按文字实际占位 OBB 与视口的相交判定，相交则整体
//     保留（文字不切开），完全在外剔除。
//
// 坐标不可信（Plausible 失败）的段与既有绘制路径同口径直接丢弃。
func clipPrimsToViewport(prims []drawing.Primitive, vp drawing.Box2) []drawing.Primitive {
	out := make([]drawing.Primitive, 0, len(prims))
	for i := range prims {
		p := &prims[i]
		switch p.Kind {
		case 0:
			kept := make([]drawing.Stroke, 0, len(p.Strokes))
			for _, s := range p.Strokes {
				if cs, ok := clipStrokeToBox(s, vp); ok {
					kept = append(kept, cs)
				}
			}
			if len(kept) > 0 {
				p.Strokes = kept
				out = append(out, *p)
			}
		case 1:
			if labelIntersectsBox(&p.Lb, vp) {
				out = append(out, *p)
			}
		}
	}
	return out
}

// clipStrokeToBox Liang-Barsky 线段裁剪：返回线段在矩形视口内的部分。
// 完全在外（含端点不可信）返回 ok=false；端点恰在边界上的零长结果
// （外部线段仅接触边界）视为无几何贡献剔除。
func clipStrokeToBox(s drawing.Stroke, b drawing.Box2) (drawing.Stroke, bool) {
	if !drawing.Plausible(s.X1, s.Y1) || !drawing.Plausible(s.X2, s.Y2) {
		return s, false
	}
	t0, t1 := 0.0, 1.0
	dx, dy := s.X2-s.X1, s.Y2-s.Y1
	// 四条边的 (p,q)：左 p=-dx q=x1-minX、右 p=dx q=maxX-x1、
	// 下 p=-dy q=y1-minY、上 p=dy q=maxY-y1。p<0 收紧下界 t0、
	// p>0 收紧上界 t1；p==0 且 q<0 表示线段整段平行且在该边外侧。
	var ok bool
	if t0, t1, ok = lbEdge(-dx, s.X1-b.MinX, t0, t1); !ok {
		return s, false
	}
	if t0, t1, ok = lbEdge(dx, b.MaxX-s.X1, t0, t1); !ok {
		return s, false
	}
	if t0, t1, ok = lbEdge(-dy, s.Y1-b.MinY, t0, t1); !ok {
		return s, false
	}
	if t0, t1, ok = lbEdge(dy, b.MaxY-s.Y1, t0, t1); !ok {
		return s, false
	}
	cx1, cy1 := s.X1+t0*dx, s.Y1+t0*dy
	cx2, cy2 := s.X1+t1*dx, s.Y1+t1*dy
	if cx1 == cx2 && cy1 == cy2 {
		return s, false
	}
	return drawing.Stroke{X1: cx1, Y1: cy1, X2: cx2, Y2: cy2}, true
}

// lbEdge Liang-Barsky 单边约束：返回收紧后的参数区间，区间反转或
// 平行边外（p==0 && q<0）时 ok=false。
func lbEdge(p, q, t0, t1 float64) (float64, float64, bool) {
	if p == 0 {
		if q < 0 {
			return t0, t1, false
		}
		return t0, t1, true
	}
	r := q / p
	if p < 0 {
		if r > t1 {
			return t0, t1, false
		}
		if r > t0 {
			t0 = r
		}
	} else {
		if r < t0 {
			return t0, t1, false
		}
		if r < t1 {
			t1 = r
		}
	}
	return t0, t1, true
}

// labelIntersectsBox 文字实际占位范围与视口矩形是否相交（含边界接触）。
// 占位为随文字方向旋转的 OBB，OBB-AABB 用四轴 SAT 判定。
func labelIntersectsBox(lb *drawing.Label, b drawing.Box2) bool {
	if !drawing.Plausible(lb.X, lb.Y) || lb.H <= 0 {
		return false
	}
	cx, cy, ux, uy, vx, vy, hu, hv := labelOccupiedOBB(lb)
	return obbIntersectsAABB(cx, cy, ux, uy, vx, vy, hu, hv, b)
}

// labelOccupiedOBB 计算文字实际占位的世界坐标有向包围盒：
// 中心 (cx,cy)、单位推进轴 u（书写方向）/单位字面向上轴 v、沿轴半宽
// hu/hv。占位范围按锚点语义确定（与 SVG text-anchor、PNG 对齐偏移
// 同源）：
//   - 推进方向：start 锚点 [0,W]、middle [-W/2,W/2]、end [-W,0]；
//   - 单行 TEXT/ATTRIB：基线下方留 0.2 字高沉降部（g/p 等下伸笔画），
//     基线上方按整字高；VAlign 下/中/上对齐时整体随之平移；
//   - MTEXT：按附着点九宫格确定水平/竖直方向的占位区间，块高按
//     显式换行行数与行距系数估算。
func labelOccupiedOBB(lb *drawing.Label) (cx, cy, ux, uy, vx, vy, hu, hv float64) {
	cosr, sinr := math.Cos(lb.Rot), math.Sin(lb.Rot)
	ux, uy = cosr, sinr
	vx, vy = -sinr, cosr
	w := math.Abs(lb.W)
	h := lb.H
	if h <= 0 {
		h = 0
	}
	// 沿推进轴的占位区间（相对锚点）。
	u0, u1 := 0.0, w
	switch lb.Anchor {
	case 1:
		u0, u1 = -w/2, w/2
	case 2:
		u0, u1 = -w, 0
	}
	// 沿字面向上轴的占位区间（相对锚点）。
	v0, v1 := -0.2*h, h
	if lb.Tx != nil {
		if a := lb.Tx.Attachment; a >= 1 && a <= 9 {
			// MTEXT 九宫格附着点：列 1/2/3 → 左/中/右，行 1/2/3 →
			// 上/中/下（行号 (a-1)/3+1）。
			switch (a - 1) % 3 {
			case 1:
				u0, u1 = -w/2, w/2
			case 2:
				u0, u1 = -w, 0
			}
			bh := mtextBlockWorldHeight(lb)
			switch (a - 1) / 3 {
			case 0: // 顶行附着：块体向锚点下方排布
				v0, v1 = -bh, 0
			case 1: // 居中附着
				v0, v1 = -bh/2, bh/2
			case 2: // 底行附着：块体向锚点上方排布
				v0, v1 = 0, bh
			}
		} else {
			// 单行 TEXT/ATTRIB 的垂直对齐（0=基线 1=下 2=中 3=上）。
			switch lb.Tx.VAlign {
			case 1:
				v0, v1 = -0.2*h, 0.8*h
			case 2:
				v0, v1 = -0.5*h, 0.5*h
			case 3:
				v0, v1 = -h, 0
			}
		}
	}
	hu = (u1 - u0) / 2
	hv = (v1 - v0) / 2
	mu := (u0 + u1) / 2
	mv := (v0 + v1) / 2
	cx = lb.X + mu*ux + mv*vx
	cy = lb.Y + mu*uy + mv*vy
	return cx, cy, ux, uy, vx, vy, hu, hv
}

// mtextBlockWorldHeight MTEXT 块的世界占位高：显式行数 × 行距系数 +
// 一个字高（与包围盒估算 drawing.mtextBlockHeightEstimate 同口径，
// 此处消费 label 已剥离格式码后的 Text）。
func mtextBlockWorldHeight(lb *drawing.Label) float64 {
	h := lb.H
	if h <= 0 {
		h = 1
	}
	lines := strings.Count(lb.Text, "\n") + 1
	if lines < 1 {
		lines = 1
	}
	f := drawing.MtextLineFactor
	if lb.Tx != nil && lb.Tx.LineFactor > 0 {
		f = lb.Tx.LineFactor
	}
	return float64(lines-1)*f*h + h
}

// obbIntersectsAABB OBB（中心+单位轴+半宽）与轴对齐矩形的相交判定
// （分离轴定理 SAT，四个候选轴：x、y、u、v；任意轴分离即不相交）。
func obbIntersectsAABB(cx, cy, ux, uy, vx, vy, hu, hv float64, b drawing.Box2) bool {
	bw := b.MaxX - b.MinX
	bh := b.MaxY - b.MinY
	acx, acy := (b.MinX+b.MaxX)/2, (b.MinY+b.MaxY)/2
	dx, dy := acx-cx, acy-cy
	// AABB 自身 x/y 轴：OBB 的投影半宽 = hu|u_axis| + hv|v_axis|。
	if math.Abs(dx) > bw/2+hu*math.Abs(ux)+hv*math.Abs(vx) {
		return false
	}
	if math.Abs(dy) > bh/2+hu*math.Abs(uy)+hv*math.Abs(vy) {
		return false
	}
	// OBB u 轴：AABB 的投影半宽 = (bw/2)|ux| + (bh/2)|uy|。
	if math.Abs(dx*ux+dy*uy) > hu+(bw/2)*math.Abs(ux)+(bh/2)*math.Abs(uy) {
		return false
	}
	// OBB v 轴。
	if math.Abs(dx*vx+dy*vy) > hv+(bw/2)*math.Abs(vx)+(bh/2)*math.Abs(vy) {
		return false
	}
	return true
}
