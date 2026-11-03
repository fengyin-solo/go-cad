// inspect.go 实现只读文档健康检查（InspectDocument）：面向批处理系统的
// 一次性体检——按实体类型与图层统计模型空间/图纸空间/块内对象，汇总跳过
// 记录数与可取得的失败句柄，检查非有限几何（NaN/Inf）、缺失图层名（仅
// 能以数字编号标识的图层）、空块定义与重复句柄，输出稳定排序的诊断项。
//
// 设计约束：
//   - 纯只读：全部信息来自 Document 既有导出（ModelSpace/Blocks/
//     PspaceSpace/ByHandle/LayerColors/InternalObjs/Skipped/DebugFailures），
//     反射遍历几何也只读取字段，绝不修改文档，不影响既有解析与渲染结果；
//   - 图纸空间实体有两个来源：pre-R13 的 PspaceSpace 与 R13+ 解析时
//     classify 仅登记在 ByHandle 的 mode==1 实体（不进任何实体列表），
//     按指针身份去重后合并，三个空间构成实体全集的一个划分；
//   - 健康（无诊断）→ status="ok"；存在需人工处理的诊断 →
//     status="diagnostics"，由命令行入口转换为独立非零退出码。
package drawing

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"

	"github.com/unitedrhino/go-cad/internal/entity"
)

// HealthSchemaVersion 健康报告合同版本：字段集锁死，新增字段才升版。
const HealthSchemaVersion = 1

// 诊断严重级别与稳定状态串（JSON 键值锁死）。
const (
	healthStatusOK          = "ok"
	healthStatusDiagnostics = "diagnostics"
	severityError           = "error"
	severityWarning         = "warning"
)

// 诊断码集合（JSON 值锁死，批处理系统按码分派）。
const (
	// diagNonFiniteGeometry 实体几何含 NaN/±Inf（fields 给出字段路径）。
	diagNonFiniteGeometry = "non_finite_geometry"
	// diagMissingLayerName 实体引用的图层无图层表记录或记录缺名，
	// 图层只能以数字编号（句柄合成名 LAYER_<HEX>）标识。
	diagMissingLayerName = "missing_layer_name"
	// diagEmptyBlock 非布局块定义不包含任何图元。
	diagEmptyBlock = "empty_block"
	// diagDuplicateHandle 同一句柄被多个实体占用。
	diagDuplicateHandle = "duplicate_handle"
	// diagSkippedRecords 解析阶段被跳过的对象数大于 0（汇总项）。
	diagSkippedRecords = "skipped_records"
	// diagDecodeFailure 单个对象的解码失败原因（句柄可得时逐条列出）。
	diagDecodeFailure = "decode_failure"
)

// HealthReport 文档健康报告（schema_version=1，字段名锁死）。
type HealthReport struct {
	SchemaVersion int           `json:"schema_version"`
	Generator     string        `json:"generator"`
	Source        string        `json:"source"`
	DwgVersion    string        `json:"dwg_version"`
	Status        string        `json:"status"` // ok | diagnostics
	Summary       HealthSummary `json:"summary"`
	Spaces        HealthSpaces  `json:"spaces"`
	Layers        []LayerHealth `json:"layers"`
	Blocks        []BlockHealth `json:"blocks"`
	Skipped       SkippedStat   `json:"skipped"`
	Diagnostics   []Diagnostic  `json:"diagnostics"`
}

// HealthSummary 汇总计数。
type HealthSummary struct {
	Entities         int `json:"entities"`
	ModelSpace       int `json:"model_space"`
	PaperSpace       int `json:"paper_space"`
	InBlocks         int `json:"in_blocks"`
	Blocks           int `json:"blocks"`
	EmptyBlocks      int `json:"empty_blocks"`
	Layers           int `json:"layers"`
	Skipped          int `json:"skipped"`
	FailureHandles   int `json:"failure_handles"`
	DuplicateHandles int `json:"duplicate_handles"`
	Diagnostics      int `json:"diagnostics"`
}

// SpaceStats 单个空间的按类型/按图层计数（map 键 JSON 序列化天然有序）。
type SpaceStats struct {
	Total   int            `json:"total"`
	ByType  map[string]int `json:"by_type"`
	ByLayer map[string]int `json:"by_layer"`
}

// BlockSpaceStats 块内对象统计：总量 + 按块名分桶。
type BlockSpaceStats struct {
	SpaceStats
	ByBlock map[string]int `json:"by_block"`
}

// HealthSpaces 三个空间的统计。
type HealthSpaces struct {
	ModelSpace SpaceStats      `json:"model_space"`
	PaperSpace SpaceStats      `json:"paper_space"`
	Blocks     BlockSpaceStats `json:"blocks"`
}

// LayerHealth 单个图层的解析情况：named=false 表示图层真名缺失，
// 只能以数字编号（句柄合成名）标识。
type LayerHealth struct {
	Handle   uint64 `json:"handle"`
	Name     string `json:"name"`
	Named    bool   `json:"named"`
	Entities int    `json:"entities"`
}

// BlockHealth 单个块定义：layout=true 为模型/图纸空间布局块
// （不计空块诊断）。
type BlockHealth struct {
	Handle   uint64 `json:"handle"`
	Name     string `json:"name"`
	Layout   bool   `json:"layout"`
	Entities int    `json:"entities"`
}

// SkippedStat 跳过记录统计与逐条失败原因。
type SkippedStat struct {
	Count    int           `json:"count"`
	Failures []FailureInfo `json:"failures"`
}

// FailureInfo 单个可定位句柄的解码失败。
type FailureInfo struct {
	Handle uint64 `json:"handle"`
	Error  string `json:"error"`
}

// Diagnostic 单条诊断项（稳定排序后输出）。
type Diagnostic struct {
	Code       string   `json:"code"`
	Severity   string   `json:"severity"` // error | warning
	Space      string   `json:"space,omitempty"`
	Handle     uint64   `json:"handle,omitempty"`
	EntityType string   `json:"entity_type,omitempty"`
	Layer      string   `json:"layer,omitempty"`
	Block      string   `json:"block,omitempty"`
	Fields     []string `json:"fields,omitempty"`
	Count      int      `json:"count,omitempty"`
	Message    string   `json:"message"`
}

// InspectDocument 对已解析文档执行只读健康检查。文档为 nil 或不含任何
// 实体（空文档/全部对象解码失败）时返回清晰错误，调用方不应把该情形
// 当作诊断报告消费。
func InspectDocument(doc *Document) (*HealthReport, error) {
	if doc == nil {
		return nil, fmt.Errorf("cad: 文档为空：无法执行健康检查（文档为 nil）")
	}
	in := newInspector(doc)
	in.collect()
	if in.totalEntities == 0 {
		if n := doc.Skipped(); n > 0 {
			return nil, fmt.Errorf("cad: 文档为空：未解析到任何实体（%d 个对象解码被跳过）", n)
		}
		return nil, fmt.Errorf("cad: 文档为空：未解析到任何模型空间/图纸空间/块内实体")
	}
	return in.build(), nil
}

// JSON 序列化报告为缩进 JSON（HTML 字符不转义，与实体对照导出同口径）。
func (r *HealthReport) JSON() ([]byte, error) {
	buf := &bytes.Buffer{}
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// inspector 单次检查的聚合状态（不跨文档复用）。
type inspector struct {
	doc *Document

	model  []any            // 模型空间实体（doc.ModelSpace）
	paper  []any            // 图纸空间实体（PspaceSpace + ByHandle 的 mode==1）
	blocks map[uint64][]any // 块内实体（doc.Blocks，句柄升序遍历）

	seen map[any]struct{} // 指针身份去重（三空间划分不重不漏）

	totalEntities int
	handleTally   map[uint64]int // 非零句柄 → 实体出现次数

	layerUsage map[uint64]int // 图层句柄 → 引用实体数

	modelStats SpaceStats
	paperStats SpaceStats
	blockStats BlockSpaceStats

	blockOrder  []uint64          // 块句柄稳定序
	blockName   map[uint64]string // 块句柄 → 显示名
	blockLayout map[uint64]bool   // 块句柄 → 是否布局块
	blockCount  map[uint64]int    // 块句柄 → 实体数

	diags []Diagnostic
}

func newInspector(doc *Document) *inspector {
	return &inspector{
		doc:         doc,
		blocks:      doc.Blocks,
		seen:        map[any]struct{}{},
		handleTally: map[uint64]int{},
		layerUsage:  map[uint64]int{},
		modelStats:  newSpaceStats(),
		paperStats:  newSpaceStats(),
		blockStats:  BlockSpaceStats{SpaceStats: newSpaceStats(), ByBlock: map[string]int{}},
		blockName:   map[uint64]string{},
		blockLayout: map[uint64]bool{},
		blockCount:  map[uint64]int{},
	}
}

func newSpaceStats() SpaceStats {
	return SpaceStats{ByType: map[string]int{}, ByLayer: map[string]int{}}
}

// collect 将文档实体划分到三个空间并完成句柄/图层计数。
func (in *inspector) collect() {
	for _, e := range in.doc.ModelSpace {
		in.addEntity(e, "model_space", 0)
	}
	// pre-R13 图纸空间实体单独存档；R13+ 图纸空间实体（mode==1）
	// classify 时不进任何列表，仅能从 ByHandle 取得。
	for _, e := range in.doc.PspaceSpace {
		in.addEntity(e, "paper_space", 0)
	}
	for _, e := range in.doc.ByHandle {
		b := entity.EntityBase(e)
		if b != nil && b.Mode == 1 {
			in.addEntity(e, "paper_space", 0)
		}
	}
	// 块定义集合：Blocks 实际内容句柄 ∪ BLOCK_HEADER 元数据句柄。
	blockSet := map[uint64]struct{}{}
	for h := range in.doc.Blocks {
		blockSet[h] = struct{}{}
	}
	for h, g := range in.doc.InternalObjs {
		if g != nil && g.Name == "BLOCK_HEADER" {
			blockSet[h] = struct{}{}
		}
	}
	in.blockOrder = make([]uint64, 0, len(blockSet))
	for h := range blockSet {
		in.blockOrder = append(in.blockOrder, h)
	}
	sort.Slice(in.blockOrder, func(i, j int) bool { return in.blockOrder[i] < in.blockOrder[j] })
	for _, h := range in.blockOrder {
		list := in.doc.Blocks[h]
		name, layout := in.resolveBlock(h)
		in.blockName[h] = name
		in.blockLayout[h] = layout
		in.blockCount[h] = len(list)
		for _, e := range list {
			in.addEntity(e, "blocks", h)
		}
	}
}

// addEntity 把单个实体计入对应空间（指针已见则跳过，保证 pre-R13 的
// PspaceSpace 与 ByHandle 双重登记不重复计数）。
func (in *inspector) addEntity(e any, space string, blockHandle uint64) {
	if e == nil {
		return
	}
	if _, dup := in.seen[e]; dup {
		return
	}
	in.seen[e] = struct{}{}
	b := entity.EntityBase(e)
	if b == nil {
		return // 非图元值不计入实体统计
	}
	in.totalEntities++
	if b.Handle != 0 {
		in.handleTally[b.Handle]++
	}
	in.layerUsage[b.Layer]++
	typeName := in.entityTypeName(e, b)
	layerName, _, _ := in.resolveLayer(b.Layer)
	var st *SpaceStats
	switch space {
	case "model_space":
		st = &in.modelStats
	case "paper_space":
		st = &in.paperStats
	default:
		st = &in.blockStats.SpaceStats
		in.blockStats.ByBlock[in.blockName[blockHandle]]++
	}
	st.Total++
	st.ByType[typeName]++
	st.ByLayer[layerName]++
	// 同步保存去重后的分桶列表（addEntity 已按指针身份去重，三个
	// 列表构成实体全集的划分，供几何检查逐空间遍历）。
	switch space {
	case "model_space":
		in.model = append(in.model, e)
	case "paper_space":
		in.paper = append(in.paper, e)
	}
}

// resolveLayer 图层句柄 → （显示名, 是否真名, 图层表记录是否存在）。
// 句柄 0 恒为内建 "0" 层；记录缺失或记录名字段为空时退回数字编号
// 合成名 LAYER_<HEX>，named=false。
func (in *inspector) resolveLayer(h uint64) (string, bool, bool) {
	if h == 0 {
		return "0", true, true
	}
	lc, ok := in.doc.LayerColors[h]
	if !ok {
		return dxfLayerName(h), false, false
	}
	if lc.Name != "" {
		return lc.Name, true, true
	}
	return dxfLayerName(h), false, true
}

// resolveBlock 块句柄 → （显示名, 是否模型/图纸空间布局块）。
func (in *inspector) resolveBlock(h uint64) (string, bool) {
	name := ""
	if g := in.doc.InternalObjs[h]; g != nil && g.Name == "BLOCK_HEADER" {
		if s, ok := g.Field("name").(string); ok {
			name = s
		}
	}
	layout := name != "" && (isModelSpaceBlockName(name) || isPaperSpaceBlockName(name))
	if name == "" {
		name = dxfBlockName(h)
	}
	return name, layout
}

// entityTypeName 实体类型名：解码路径已回填 b.TypeName（DWG/pre-R13）；
// DXF/外部构造实体未回填时按具体 Go 类型兜底，仍未知再退回反射类型名。
func (in *inspector) entityTypeName(e any, b *entity.BaseEntity) string {
	if b.TypeName != "" {
		return b.TypeName
	}
	switch t := e.(type) {
	case *entity.EntLine:
		return "LINE"
	case *entity.EntCircle:
		return "CIRCLE"
	case *entity.EntArc:
		return "ARC"
	case *entity.EntPoint:
		return "POINT"
	case *entity.EntEllipse:
		return "ELLIPSE"
	case *entity.EntLwPolyline:
		return "LWPOLYLINE"
	case *entity.EntText:
		return "TEXT"
	case *entity.EntMText:
		return "MTEXT"
	case *entity.EntInsert:
		return "INSERT"
	case *entity.EntAttrib:
		return "ATTRIB"
	case *entity.EntBlockLike:
		return "BLOCK"
	case *entity.EntSpline:
		return "SPLINE"
	case *entity.EntRay:
		if t.Xline {
			return "XLINE"
		}
		return "RAY"
	case *entity.EntSolid:
		if t.Trace {
			return "TRACE"
		}
		return "SOLID"
	case *entity.EntFace3d:
		return "3DFACE"
	case *entity.EntLeader:
		return "LEADER"
	case *entity.EntMLine:
		return "MLINE"
	case *entity.EntVertex2d:
		return "VERTEX_2D"
	case *entity.EntVertex3d:
		return "VERTEX_3D"
	case *entity.EntVertexPface:
		return "VERTEX_PFACE"
	case *entity.EntVertexPfaceFace:
		return "VERTEX_PFACE_FACE"
	case *entity.EntUnknownEnt:
		return "UNKNOWN_ENT"
	case *entity.EntHelix:
		return "HELIX"
	case *entity.EntPolyline2d:
		return "POLYLINE_2D"
	case *entity.EntPolyline3d:
		return "POLYLINE_3D"
	case *entity.EntPolylinePface:
		return "POLYLINE_PFACE"
	case *entity.EntPolylineMesh:
		return "POLYLINE_MESH"
	case *entity.EntTolerance:
		return "TOLERANCE"
	case *entity.EntViewport:
		return "VIEWPORT"
	case *entity.EntShape:
		return "SHAPE"
	case *entity.EntWipeout:
		return "WIPEOUT"
	case *entity.EntAcis:
		if t.Kind != "" {
			return t.Kind
		}
		return "BODY"
	case *entity.EntImage:
		return "IMAGE"
	case *entity.EntOle2Frame:
		return "OLE2FRAME"
	case *entity.EntOleFrame:
		return "OLEFRAME"
	case *entity.EntProxyEntity:
		return "PROXY_ENTITY"
	case *entity.EntUnderlay:
		return "UNDERLAY"
	case *entity.EntDimension:
		return "DIMENSION"
	case *entity.EntHatch:
		return "HATCH"
	case *entity.EntLight:
		return "LIGHT"
	case *entity.EntMLeader:
		return "MULTILEADER"
	case *entity.EntMpolygon:
		return "MPOLYGON"
	}
	rt := reflect.TypeOf(e)
	if rt != nil && rt.Kind() == reflect.Ptr {
		return strings.TrimPrefix(rt.Elem().Name(), "Ent")
	}
	return "UNKNOWN"
}

// build 汇总统计与诊断并组装最终报告。
func (in *inspector) build() *HealthReport {
	in.checkGeometry()
	in.checkLayers()
	in.checkBlocks()
	in.checkDuplicateHandles()
	in.checkSkipped()
	sortDiagnostics(in.diags)

	r := &HealthReport{
		SchemaVersion: HealthSchemaVersion,
		Generator:     "go-cad cadinspect",
		DwgVersion:    in.doc.Version(),
		Spaces: HealthSpaces{
			ModelSpace: in.modelStats,
			PaperSpace: in.paperStats,
			Blocks:     in.blockStats,
		},
		Layers:      in.layerReports(),
		Blocks:      in.blockReports(),
		Skipped:     in.skippedStat(),
		Diagnostics: in.diags,
	}
	if r.Diagnostics == nil {
		r.Diagnostics = []Diagnostic{}
	}
	if r.Skipped.Failures == nil {
		r.Skipped.Failures = []FailureInfo{}
	}
	emptyBlocks := 0
	for _, b := range r.Blocks {
		if !b.Layout && b.Entities == 0 {
			emptyBlocks++
		}
	}
	dupHandles := 0
	for _, d := range in.diags {
		if d.Code == diagDuplicateHandle {
			dupHandles++
		}
	}
	r.Summary = HealthSummary{
		Entities:         in.totalEntities,
		ModelSpace:       in.modelStats.Total,
		PaperSpace:       in.paperStats.Total,
		InBlocks:         in.blockStats.Total,
		Blocks:           len(r.Blocks),
		EmptyBlocks:      emptyBlocks,
		Layers:           len(r.Layers),
		Skipped:          in.doc.Skipped(),
		FailureHandles:   len(in.doc.DebugFailures()),
		DuplicateHandles: dupHandles,
		Diagnostics:      len(r.Diagnostics),
	}
	if len(r.Diagnostics) > 0 {
		r.Status = healthStatusDiagnostics
	} else {
		r.Status = healthStatusOK
	}
	return r
}

// layerReports 图层清单：图层表记录 ∪ 实体实际引用句柄，按句柄升序。
func (in *inspector) layerReports() []LayerHealth {
	set := map[uint64]struct{}{}
	for h := range in.doc.LayerColors {
		set[h] = struct{}{}
	}
	for h := range in.layerUsage {
		set[h] = struct{}{}
	}
	hs := make([]uint64, 0, len(set))
	for h := range set {
		hs = append(hs, h)
	}
	sort.Slice(hs, func(i, j int) bool { return hs[i] < hs[j] })
	out := make([]LayerHealth, 0, len(hs))
	for _, h := range hs {
		name, named, _ := in.resolveLayer(h)
		out = append(out, LayerHealth{
			Handle:   h,
			Name:     name,
			Named:    named,
			Entities: in.layerUsage[h],
		})
	}
	return out
}

// blockReports 块定义清单（句柄升序）。
func (in *inspector) blockReports() []BlockHealth {
	out := make([]BlockHealth, 0, len(in.blockOrder))
	for _, h := range in.blockOrder {
		out = append(out, BlockHealth{
			Handle:   h,
			Name:     in.blockName[h],
			Layout:   in.blockLayout[h],
			Entities: in.blockCount[h],
		})
	}
	return out
}

// skippedStat 跳过统计：句柄→原因按键升序列出可取得的失败。
func (in *inspector) skippedStat() SkippedStat {
	fails := in.doc.DebugFailures()
	hs := make([]uint64, 0, len(fails))
	for h := range fails {
		hs = append(hs, h)
	}
	sort.Slice(hs, func(i, j int) bool { return hs[i] < hs[j] })
	out := SkippedStat{Count: in.doc.Skipped(), Failures: make([]FailureInfo, 0, len(hs))}
	for _, h := range hs {
		out.Failures = append(out.Failures, FailureInfo{Handle: h, Error: fails[h]})
	}
	return out
}

// checkGeometry 遍历三个空间全部实体，反射检查可达 float64 字段的
// NaN/±Inf（非有限几何）。字段路径按结构定义顺序产生，天然确定；
// walked 按指针身份兜底，防止跨来源登记的同一实体被重复诊断。
func (in *inspector) checkGeometry() {
	walked := map[any]struct{}{}
	in.walkSpace(walked, "model_space", "", in.model)
	in.walkSpace(walked, "paper_space", "", in.paper)
	for _, h := range in.blockOrder {
		in.walkSpace(walked, "blocks", in.blockName[h], in.blocks[h])
	}
}

func (in *inspector) walkSpace(walked map[any]struct{}, space, blockName string, list []any) {
	for _, e := range list {
		if _, ok := walked[e]; ok {
			continue
		}
		walked[e] = struct{}{}
		b := entity.EntityBase(e)
		if b == nil {
			continue
		}
		bad := nonFiniteFields(e)
		if len(bad) == 0 {
			continue
		}
		layerName, _, _ := in.resolveLayer(b.Layer)
		d := Diagnostic{
			Code:       diagNonFiniteGeometry,
			Severity:   severityError,
			Space:      space,
			Handle:     b.Handle,
			EntityType: in.entityTypeName(e, b),
			Layer:      layerName,
			Fields:     bad,
			Message:    fmt.Sprintf("实体含非有限几何坐标（NaN/Inf）：%s", strings.Join(bad, ", ")),
		}
		if space == "blocks" {
			d.Block = blockName
		}
		in.diags = append(in.diags, d)
	}
}

// nonFiniteFields 反射收集实体内所有非有限 float64 字段的稳定路径。
// 仅遍历导出字段：几何坐标/比例/角度等数值均为导出字段；EED/原始位串
// 等非几何数据位于未导出字段或 string/[]byte，不会误入。
func nonFiniteFields(ent any) []string {
	var out []string
	visited := map[uintptr]bool{}
	var walk func(v reflect.Value, prefix string)
	walk = func(v reflect.Value, prefix string) {
		switch v.Kind() {
		case reflect.Ptr:
			if v.IsNil() {
				return
			}
			ptr := v.Pointer()
			if ptr != 0 {
				if visited[ptr] {
					return
				}
				visited[ptr] = true
			}
			walk(v.Elem(), prefix)
		case reflect.Interface:
			if v.IsNil() {
				return
			}
			walk(v.Elem(), prefix)
		case reflect.Struct:
			t := v.Type()
			for i := 0; i < v.NumField(); i++ {
				f := t.Field(i)
				if !f.IsExported() {
					continue
				}
				name := f.Name
				next := name
				if prefix != "" {
					next = prefix + "." + name
				}
				walk(v.Field(i), next)
			}
		case reflect.Slice, reflect.Array:
			if v.Type().Elem().Kind() == reflect.Uint8 {
				return // []byte 原始数据，无数值几何
			}
			for i := 0; i < v.Len(); i++ {
				walk(v.Index(i), fmt.Sprintf("%s[%d]", prefix, i))
			}
		case reflect.Map:
			if v.Type().Key().Kind() != reflect.String {
				return
			}
			keys := make([]string, 0, v.Len())
			for _, k := range v.MapKeys() {
				keys = append(keys, k.String())
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(v.MapIndex(reflect.ValueOf(k)), prefix+"."+k)
			}
		case reflect.Float64:
			f := v.Float()
			if math.IsNaN(f) {
				out = append(out, prefix+"=NaN")
			} else if math.IsInf(f, 1) {
				out = append(out, prefix+"=+Inf")
			} else if math.IsInf(f, -1) {
				out = append(out, prefix+"=-Inf")
			}
		}
	}
	rv := reflect.ValueOf(ent)
	walk(rv, "")
	return out
}

// checkLayers 缺失图层名诊断（按图层句柄聚合）：图层表无记录
// （record_missing）或记录缺名（name_empty），图层只能以数字编号标识。
func (in *inspector) checkLayers() {
	handles := make([]uint64, 0, len(in.layerUsage))
	for h := range in.layerUsage {
		if h == 0 {
			continue // 内建 0 层永远有效
		}
		handles = append(handles, h)
	}
	sort.Slice(handles, func(i, j int) bool { return handles[i] < handles[j] })
	for _, h := range handles {
		name, named, exists := in.resolveLayer(h)
		if named {
			continue
		}
		var reason, msg string
		if !exists {
			reason = "record_missing"
			msg = fmt.Sprintf("图层句柄 0x%X 在图层表中无记录（图层名缺失，仅能以数字编号 %s 标识），%d 个实体引用该图层",
				h, name, in.layerUsage[h])
		} else {
			reason = "name_empty"
			msg = fmt.Sprintf("图层句柄 0x%X 的图层记录缺少名称（仅能以数字编号 %s 标识），%d 个实体引用该图层",
				h, name, in.layerUsage[h])
		}
		in.diags = append(in.diags, Diagnostic{
			Code:     diagMissingLayerName,
			Severity: severityWarning,
			Handle:   h,
			Layer:    name,
			Count:    in.layerUsage[h],
			Message:  msg + "（" + reason + "）",
		})
	}
}

// checkBlocks 空块诊断：非布局块定义且实体数为 0。
func (in *inspector) checkBlocks() {
	for _, h := range in.blockOrder {
		name := in.blockName[h]
		if in.blockLayout[h] || in.blockCount[h] > 0 {
			continue
		}
		in.diags = append(in.diags, Diagnostic{
			Code:     diagEmptyBlock,
			Severity: severityWarning,
			Handle:   h,
			Block:    name,
			Message:  fmt.Sprintf("块定义 %q（句柄 0x%X）不包含任何图元", name, h),
		})
	}
}

// checkDuplicateHandles 重复句柄诊断：同一非零句柄被多个实体占用。
func (in *inspector) checkDuplicateHandles() {
	handles := make([]uint64, 0, len(in.handleTally))
	for h, n := range in.handleTally {
		if n >= 2 {
			handles = append(handles, h)
		}
	}
	sort.Slice(handles, func(i, j int) bool { return handles[i] < handles[j] })
	for _, h := range handles {
		n := in.handleTally[h]
		in.diags = append(in.diags, Diagnostic{
			Code:     diagDuplicateHandle,
			Severity: severityError,
			Handle:   h,
			Count:    n,
			Message:  fmt.Sprintf("句柄 0x%X 被 %d 个实体重复使用", h, n),
		})
	}
}

// checkSkipped 跳过记录汇总诊断 + 句柄可得的逐条失败诊断。
func (in *inspector) checkSkipped() {
	if n := in.doc.Skipped(); n > 0 {
		in.diags = append(in.diags, Diagnostic{
			Code:     diagSkippedRecords,
			Severity: severityWarning,
			Count:    n,
			Message:  fmt.Sprintf("解析阶段有 %d 个对象解码失败被跳过，其中 %d 个句柄可定位", n, len(in.doc.DebugFailures())),
		})
	}
	for _, f := range in.skippedStat().Failures {
		in.diags = append(in.diags, Diagnostic{
			Code:     diagDecodeFailure,
			Severity: severityWarning,
			Handle:   f.Handle,
			Message:  fmt.Sprintf("句柄 0x%X 解码失败：%s", f.Handle, f.Error),
		})
	}
}

// sortDiagnostics 诊断项稳定排序：严重级别（error 先）→ 码 → 空间 →
// 块 → 图层 → 句柄 → 实体类型 → 字段序列 → 消息。
func sortDiagnostics(ds []Diagnostic) {
	sevRank := func(s string) int {
		if s == severityError {
			return 0
		}
		return 1
	}
	less := func(a, b Diagnostic) bool {
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		if a.Handle != b.Handle {
			return a.Handle < b.Handle
		}
		ka := []string{a.Space, a.Block, a.Layer, a.EntityType, strings.Join(a.Fields, ","), a.Message}
		kb := []string{b.Space, b.Block, b.Layer, b.EntityType, strings.Join(b.Fields, ","), b.Message}
		for i := range ka {
			if ka[i] != kb[i] {
				return ka[i] < kb[i]
			}
		}
		return a.Count < b.Count
	}
	sort.SliceStable(ds, func(i, j int) bool {
		ri, rj := sevRank(ds[i].Severity), sevRank(ds[j].Severity)
		if ri != rj {
			return ri < rj
		}
		return less(ds[i], ds[j])
	})
}
