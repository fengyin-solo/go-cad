// layer_filter.go 实现渲染图层过滤：RenderOptions 的图层白名单/排除名单
// 在 PNG（RenderPNG）、SVG（RenderSVG）、单图框（RenderSheetPNG/SVG）与
// 全部图框（RenderAllSheets）四条渲染入口共用同一套语义——
//   - 白名单非空时仅输出最终图层标识命中名单的图元；
//   - 排除名单非空时命中名单的图元不输出；
//   - 两者同时给出时先应用白名单、再应用排除；
//   - 空名单（nil 或长度 0）表示不限制，输出与不过滤完全一致；
//   - 块引用（INSERT，含嵌套块与标注匿名块）内部展开后的图元按其最终
//     解析出的所属图层判定（展开图元携带的是内部实体自身的图层句柄，
//     而非外层 INSERT 的图层）；
//   - 有名图层按图层真名匹配；无名图层（LayerColors 无记录或名为空）
//     按稳定句柄标识 "handle-<大写十六进制句柄>" 匹配，与 SVG 图层组
//     id（layer:<标识>）及 <metadata> 图层清单同源同形；
//   - 名单中出现文档不存在的图层标识时返回错误，错误信息包含该标识。
//
// 过滤在图元展开之后、放射线/原点锚定等几何过滤与包围盒计算之前执行，
// 保证视口、分组、metadata、清晰度统计与自适应宽度都只基于实际输出内容。
package render

import (
	"fmt"
	"sort"
	"strings"

	"github.com/unitedrhino/go-cad/internal/drawing"
)

// layerHandlePrefix 无名图层的稳定句柄标识前缀（与 SVG 组 id 回退口径一致）。
const layerHandlePrefix = "handle-"

// layerIdentifier 返回图层句柄的稳定标识：有名图层用图层真名，
// 无名/无图层记录用 handle-<大写十六进制句柄>。SVG 图层组 id 与
// metadata 图层清单、图层过滤名单三者共用本函数，保证同形同口径。
func layerIdentifier(doc *drawing.Document, h uint64) string {
	if lc, ok := doc.LayerColors[h]; ok && lc.Name != "" {
		return lc.Name
	}
	return fmt.Sprintf("%s%X", layerHandlePrefix, h)
}

// normalizeLayerSelector 归一化调用方给出的图层标识：句柄标识的十六进制
// 部分统一转大写（句柄以数字身份匹配，大小写不应造成两种写法），图层名
// 原样保留（图层名大小写敏感，与 CAD 语义一致）。
func normalizeLayerSelector(s string) string {
	if len(s) >= len(layerHandlePrefix) && strings.EqualFold(s[:len(layerHandlePrefix)], layerHandlePrefix) {
		return layerHandlePrefix + strings.ToUpper(s[len(layerHandlePrefix):])
	}
	return s
}

// layerFilter 图层过滤器：include 为白名单（nil=不限制），exclude 为
// 排除名单（nil=不限制）。过滤顺序固定为先白名单后排除。
type layerFilter struct {
	doc     *drawing.Document
	include map[string]struct{}
	exclude map[string]struct{}
	ids     map[uint64]string // 句柄 → 稳定标识缓存（免逐图元格式化）
}

// newLayerFilter 按渲染选项构造过滤器并校验名单：白名单/排除名单中的
// 每个标识都必须能在文档中解析到（已声明图层或被图元实际引用的图层，
// 含块定义内部图元的图层——prims 为展开后图元，已覆盖嵌套块），
// 否则返回包含全部未知标识（去重、字典序）的错误。
func newLayerFilter(doc *drawing.Document, prims []drawing.Primitive, opts RenderOptions) (*layerFilter, error) {
	f := &layerFilter{doc: doc, ids: make(map[uint64]string, 32)}
	f.include = selectorSet(opts.IncludeLayers)
	f.exclude = selectorSet(opts.ExcludeLayers)

	// 已知标识全集：文档声明的全部图层 + 展开图元实际引用的图层
	// （后者覆盖 LayerColors 缺记录、只能以句柄标识匹配的无名图层）。
	known := make(map[string]struct{})
	for h := range doc.LayerColors {
		known[f.id(h)] = struct{}{}
	}
	for i := range prims {
		known[f.id(prims[i].Layer)] = struct{}{}
	}
	var unknown []string
	seen := make(map[string]struct{})
	for _, s := range opts.IncludeLayers {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		if _, ok := known[normalizeLayerSelector(s)]; !ok {
			unknown = append(unknown, s)
		}
	}
	for _, s := range opts.ExcludeLayers {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		if _, ok := known[normalizeLayerSelector(s)]; !ok {
			unknown = append(unknown, s)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		quoted := make([]string, len(unknown))
		for i, s := range unknown {
			quoted[i] = fmt.Sprintf("%q", s)
		}
		return nil, fmt.Errorf("cad: 图层过滤引用了文档中不存在的图层 %s（有名图层按图层名，无名图层按 %s<十六进制句柄>，如 %s2F）",
			strings.Join(quoted, ", "), layerHandlePrefix, layerHandlePrefix)
	}
	return f, nil
}

// selectorSet 名单转集合：空名单返回 nil 表示该阶段不限制。
func selectorSet(list []string) map[string]struct{} {
	if len(list) == 0 {
		return nil
	}
	m := make(map[string]struct{}, len(list))
	for _, s := range list {
		m[normalizeLayerSelector(s)] = struct{}{}
	}
	return m
}

// id 句柄 → 稳定标识（带缓存）。
func (f *layerFilter) id(h uint64) string {
	if id, ok := f.ids[h]; ok {
		return id
	}
	id := layerIdentifier(f.doc, h)
	f.ids[h] = id
	return id
}

// active 是否存在任何过滤（空名单时四条渲染管线走原始路径，输出零变化）。
func (f *layerFilter) active() bool {
	return f != nil && (f.include != nil || f.exclude != nil)
}

// keepHandle 判定指定最终图层句柄的图元是否输出：
// 先白名单（非空时必须命中）、后排除（命中即剔除）。
func (f *layerFilter) keepHandle(h uint64) bool {
	if !f.active() {
		return true
	}
	id := f.id(h)
	if f.include != nil {
		if _, ok := f.include[id]; !ok {
			return false
		}
	}
	if f.exclude != nil {
		if _, ok := f.exclude[id]; ok {
			return false
		}
	}
	return true
}

// apply 过滤展开图元列表：原地写回复用底层数组（写位置恒不超前读位置，
// 与 filterRadiatingStrokes/dropOriginAnchored 的复用口径一致）。
// 无过滤时原样返回同一切片，保证不过滤的输出字节级不变。
func (f *layerFilter) apply(prims []drawing.Primitive) []drawing.Primitive {
	if !f.active() {
		return prims
	}
	out := prims[:0]
	for i := range prims {
		if f.keepHandle(prims[i].Layer) {
			out = append(out, prims[i])
		}
	}
	return out
}
