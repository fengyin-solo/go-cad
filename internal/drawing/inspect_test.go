// inspect_test.go 验证只读文档健康检查（InspectDocument）：
// 健康文档统计口径（模型/图纸/块三空间按类型与图层）、非有限几何、
// 跳过记录与失败句柄、缺失图层名（数字编号图层）、空块、重复句柄、
// 诊断稳定排序，以及 nil/空文档清晰报错不 panic；并证明检查过程不改变
// 文档（DXF/对照导出字节前后一致）。渲染不变性在 drawing_test 外部
// 测试包覆盖（避免与 render 包循环导入）。
package drawing

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/entity"
	"github.com/unitedrhino/go-cad/internal/object"
	"github.com/unitedrhino/go-cad/internal/objrec"
)

// inspectLine 构造一条 LINE 实体。
func inspectLine(handle, layer uint64, mode uint8, x1, y1, x2, y2 float64) *entity.EntLine {
	return &entity.EntLine{
		BaseEntity: entity.BaseEntity{Handle: handle, Layer: layer, Mode: mode, Owner: 0, TypeName: "LINE"},
		Start:      entity.Point3{X: x1, Y: y1},
		End:        entity.Point3{X: x2, Y: y2},
	}
}

// inspectBlockHeader 登记一个 BLOCK_HEADER 内部对象（块名元数据）。
func inspectBlockHeader(doc *Document, handle uint64, name string) {
	doc.InternalObjs[handle] = &object.ObjGeneric{
		Name:   "BLOCK_HEADER",
		Handle: handle,
		Fields: []object.ObjField{{Key: "name", Val: name}},
	}
}

// newInspectTestDoc 构造结构完整的健康文档骨架（含图层表与映射）。
func newInspectTestDoc() *Document {
	return &Document{
		Ver:          container.VerR2018,
		ModelSpace:   []any{},
		Blocks:       map[uint64][]any{},
		Attribs:      map[uint64]*entity.EntAttrib{},
		LayerColors:  map[uint64]LayerColor{10: {Index: 1, Name: "WALL"}},
		InternalObjs: map[uint64]*object.ObjGeneric{},
		ByHandle:     map[uint64]any{},
	}
}

// TestInspectHealthy 健康文档：三空间实体统计、图层/块清单、布局块
// 不算空块、图纸空间双来源（PspaceSpace 与 ByHandle mode==1）合并且
// 不重复计数、Type 名反射兜底。
func TestInspectHealthy(t *testing.T) {
	doc := newInspectTestDoc()
	// 模型空间：LINE 在 WALL 层 + CIRCLE 在 0 层
	doc.ModelSpace = append(doc.ModelSpace,
		inspectLine(1, 10, 2, 0, 0, 1, 1),
		&entity.EntCircle{
			BaseEntity: entity.BaseEntity{Handle: 2, Layer: 0, Mode: 2}, // 无 TypeName，走反射兜底
			Center:     entity.Point3{X: 5, Y: 5}, Radius: 2,
		})
	// 普通块 MYBLOCK：一条 TEXT
	inspectBlockHeader(doc, 50, "MYBLOCK")
	doc.Blocks[50] = []any{
		&entity.EntText{
			BaseEntity: entity.BaseEntity{Handle: 3, Layer: 10, Mode: 0, Owner: 50, TypeName: "TEXT"},
			Text:       "T", Insertion: entity.Point3{X: 1, Y: 1}, Height: 2,
		},
	}
	// 布局块（无内容也不得报空块）
	inspectBlockHeader(doc, 51, "*Model_Space")
	// 图纸空间来源一：pre-R13 风格 PspaceSpace（同时登记 ByHandle）
	ps1 := inspectLine(4, 10, 1, 0, 0, 2, 2)
	doc.PspaceSpace = append(doc.PspaceSpace, ps1)
	doc.ByHandle[4] = ps1
	// 图纸空间来源二：R13+ classify 后仅存于 ByHandle 的 mode==1 实体
	doc.ByHandle[5] = &entity.EntCircle{
		BaseEntity: entity.BaseEntity{Handle: 5, Layer: 0, Mode: 1, TypeName: "CIRCLE"},
		Center:     entity.Point3{X: 1, Y: 1}, Radius: 1,
	}
	// 模型空间实体也在 ByHandle（模拟 classify），不得重复计入图纸空间
	doc.ByHandle[1] = doc.ModelSpace[0]
	doc.ByHandle[2] = doc.ModelSpace[1]

	r, err := InspectDocument(doc)
	if err != nil {
		t.Fatalf("InspectDocument: %v", err)
	}
	if r.Status != "ok" || len(r.Diagnostics) != 0 {
		t.Fatalf("健康文档 status=%s diags=%+v", r.Status, r.Diagnostics)
	}
	if r.SchemaVersion != 1 || r.Generator == "" || r.DwgVersion == "" {
		t.Fatalf("报告元数据异常: %+v", r)
	}
	if got := r.Summary; got.Entities != 5 || got.ModelSpace != 2 || got.PaperSpace != 2 ||
		got.InBlocks != 1 || got.Blocks != 2 || got.EmptyBlocks != 0 || got.Layers != 2 {
		t.Fatalf("汇总计数异常: %+v", got)
	}
	if r.Spaces.ModelSpace.ByType["LINE"] != 1 || r.Spaces.ModelSpace.ByType["CIRCLE"] != 1 {
		t.Fatalf("模型空间按类型统计异常: %+v", r.Spaces.ModelSpace.ByType)
	}
	if r.Spaces.ModelSpace.ByLayer["WALL"] != 1 || r.Spaces.ModelSpace.ByLayer["0"] != 1 {
		t.Fatalf("模型空间按图层统计异常: %+v", r.Spaces.ModelSpace.ByLayer)
	}
	if r.Spaces.PaperSpace.Total != 2 || r.Spaces.PaperSpace.ByType["LINE"] != 1 ||
		r.Spaces.PaperSpace.ByType["CIRCLE"] != 1 {
		t.Fatalf("图纸空间统计异常: %+v", r.Spaces.PaperSpace)
	}
	if r.Spaces.Blocks.Total != 1 || r.Spaces.Blocks.ByBlock["MYBLOCK"] != 1 ||
		r.Spaces.Blocks.ByType["TEXT"] != 1 {
		t.Fatalf("块内统计异常: %+v", r.Spaces.Blocks)
	}
	// 图层清单按句柄升序：0 在 WALL(10) 前；0 层=模型 CIRCLE+图纸 CIRCLE，
	// WALL=模型 LINE+块内 TEXT+图纸 LINE
	if len(r.Layers) != 2 || r.Layers[0].Handle != 0 || r.Layers[0].Name != "0" ||
		r.Layers[0].Entities != 2 || r.Layers[1].Name != "WALL" || r.Layers[1].Entities != 3 {
		t.Fatalf("图层清单异常: %+v", r.Layers)
	}
	// 块清单按句柄升序，布局块标记正确
	if len(r.Blocks) != 2 || r.Blocks[0].Handle != 50 || !r.Blocks[1].Layout {
		t.Fatalf("块清单异常: %+v", r.Blocks)
	}
	// JSON 可序列化且诊断为空数组（非 null）
	buf, err := r.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	s := string(buf)
	if !strings.Contains(s, `"diagnostics": []`) {
		t.Fatalf("空诊断应输出 [], got: %s", s)
	}
}

// TestInspectNonFiniteGeometry 非有限坐标：NaN/+Inf/-Inf 逐字段定位，
// 模型空间与块内实体分别带空间/块名，严重级别为 error。
func TestInspectNonFiniteGeometry(t *testing.T) {
	doc := newInspectTestDoc()
	bad := inspectLine(1, 10, 2, 0, 0, 1, 1)
	bad.Start.X = math.NaN()
	bad.End.Y = math.Inf(1)
	doc.ModelSpace = append(doc.ModelSpace, bad)
	inspectBlockHeader(doc, 50, "MYBLOCK")
	badCircle := &entity.EntCircle{
		BaseEntity: entity.BaseEntity{Handle: 6, Layer: 0, Mode: 0, Owner: 50, TypeName: "CIRCLE"},
		Center:     entity.Point3{X: 1}, Radius: math.Inf(-1),
	}
	doc.Blocks[50] = []any{badCircle}

	r, err := InspectDocument(doc)
	if err != nil {
		t.Fatalf("InspectDocument: %v", err)
	}
	if r.Status != "diagnostics" {
		t.Fatalf("期望 diagnostics, got %s", r.Status)
	}
	var geom []Diagnostic
	for _, d := range r.Diagnostics {
		if d.Code == diagNonFiniteGeometry {
			geom = append(geom, d)
		}
	}
	if len(geom) != 2 {
		t.Fatalf("非有限几何诊断数 = %d, 期望 2: %+v", len(geom), r.Diagnostics)
	}
	// 稳定序：句柄 1（模型空间）在句柄 6（块内）之前
	d := geom[0]
	if d.Handle != 1 || d.Space != "model_space" || d.Severity != severityError || d.EntityType != "LINE" {
		t.Fatalf("首条诊断异常: %+v", d)
	}
	if len(d.Fields) != 2 || d.Fields[0] != "Start.X=NaN" || d.Fields[1] != "End.Y=+Inf" {
		t.Fatalf("非有限字段路径异常: %+v", d.Fields)
	}
	d2 := geom[1]
	if d2.Handle != 6 || d2.Space != "blocks" || d2.Block != "MYBLOCK" {
		t.Fatalf("块内诊断异常: %+v", d2)
	}
	if len(d2.Fields) != 1 || d2.Fields[0] != "Radius=-Inf" {
		t.Fatalf("块内非有限字段异常: %+v", d2.Fields)
	}
}

// TestInspectSkipped 跳过记录：总数汇总 + 可取得的失败句柄按句柄升序，
// 诊断包含汇总项与逐条 decode_failure。
func TestInspectSkipped(t *testing.T) {
	doc := newInspectTestDoc()
	doc.ModelSpace = append(doc.ModelSpace, inspectLine(1, 0, 2, 0, 0, 1, 1))
	doc.skipped = 3 // 3 条跳过，其中仅 2 条拿得到句柄
	doc.FailBy(objrec.ObjectRef{Handle: 300, Offset: 11}, fmt.Errorf("LINE: 位流越界"))
	doc.FailBy(objrec.ObjectRef{Handle: 200, Offset: 22}, fmt.Errorf("CIRCLE: 候选评分不足"))

	r, err := InspectDocument(doc)
	if err != nil {
		t.Fatalf("InspectDocument: %v", err)
	}
	if r.Skipped.Count != 3 || len(r.Skipped.Failures) != 2 {
		t.Fatalf("跳过统计异常: %+v", r.Skipped)
	}
	if r.Skipped.Failures[0].Handle != 200 || r.Skipped.Failures[1].Handle != 300 {
		t.Fatalf("失败句柄应按升序: %+v", r.Skipped.Failures)
	}
	if !strings.Contains(r.Skipped.Failures[0].Error, "CIRCLE") {
		t.Fatalf("失败原因丢失: %+v", r.Skipped.Failures[0])
	}
	if r.Summary.Skipped != 3 || r.Summary.FailureHandles != 2 {
		t.Fatalf("汇总计数异常: %+v", r.Summary)
	}
	codes := map[string]int{}
	for _, d := range r.Diagnostics {
		codes[d.Code]++
	}
	if codes[diagSkippedRecords] != 1 || codes[diagDecodeFailure] != 2 {
		t.Fatalf("跳过相关诊断异常: %+v", r.Diagnostics)
	}
	var summary Diagnostic
	for _, d := range r.Diagnostics {
		if d.Code == diagSkippedRecords {
			summary = d
		}
	}
	if summary.Count != 3 || !strings.Contains(summary.Message, "3") {
		t.Fatalf("跳过汇总诊断异常: %+v", summary)
	}
	// 失败句柄出现在诊断项中
	handles := map[uint64]bool{}
	for _, d := range r.Diagnostics {
		if d.Code == diagDecodeFailure {
			handles[d.Handle] = true
		}
	}
	if !handles[200] || !handles[300] {
		t.Fatalf("decode_failure 句柄缺失: %+v", handles)
	}
}

// TestInspectMissingLayerName 缺失图层名：图层表无记录（record_missing）
// 与记录缺名（name_empty，仅能以数字编号 LAYER_<HEX> 标识）分别诊断；
// 内建 0 层不诊断。
func TestInspectMissingLayerName(t *testing.T) {
	doc := newInspectTestDoc()
	doc.ModelSpace = append(doc.ModelSpace,
		inspectLine(1, 77, 2, 0, 0, 1, 1), // 图层表无 77 记录
		inspectLine(2, 77, 2, 0, 0, 1, 1),
	)
	// 88 记录存在但名字段为空（仅数字编号可用）
	doc.LayerColors[88] = LayerColor{Index: 7}
	doc.ModelSpace = append(doc.ModelSpace, &entity.EntCircle{
		BaseEntity: entity.BaseEntity{Handle: 3, Layer: 88, Mode: 2, TypeName: "CIRCLE"},
		Radius:     1,
	})
	doc.ModelSpace = append(doc.ModelSpace, inspectLine(4, 0, 2, 0, 0, 1, 1)) // 0 层健康

	r, err := InspectDocument(doc)
	if err != nil {
		t.Fatalf("InspectDocument: %v", err)
	}
	byHandle := map[uint64]Diagnostic{}
	for _, d := range r.Diagnostics {
		if d.Code != diagMissingLayerName {
			t.Fatalf("意外诊断: %+v", d)
		}
		byHandle[d.Handle] = d
	}
	miss, ok1 := byHandle[77]
	unnamed, ok2 := byHandle[88]
	if !ok1 || !ok2 {
		t.Fatalf("缺少图层诊断: %+v", r.Diagnostics)
	}
	if miss.Layer != "LAYER_4D" || miss.Count != 2 ||
		!strings.Contains(miss.Message, "数字编号") || !strings.Contains(miss.Message, "record_missing") {
		t.Fatalf("无记录图层诊断异常: %+v", miss)
	}
	if unnamed.Layer != "LAYER_58" || unnamed.Count != 1 ||
		!strings.Contains(unnamed.Message, "数字编号") || !strings.Contains(unnamed.Message, "name_empty") {
		t.Fatalf("缺名图层诊断异常: %+v", unnamed)
	}
	// 统计按合成名分桶
	if r.Spaces.ModelSpace.ByLayer["LAYER_4D"] != 2 || r.Spaces.ModelSpace.ByLayer["LAYER_58"] != 1 ||
		r.Spaces.ModelSpace.ByLayer["0"] != 1 {
		t.Fatalf("按图层统计异常: %+v", r.Spaces.ModelSpace.ByLayer)
	}
	// 图层清单具名标志
	for _, l := range r.Layers {
		if l.Handle == 77 && l.Named {
			t.Fatalf("句柄 77 应 named=false")
		}
		if l.Handle == 88 && l.Named {
			t.Fatalf("句柄 88 应 named=false")
		}
	}
}

// TestInspectEmptyBlock 空块：非布局空块定义报诊断（含无 BLOCK_HEADER
// 元数据的零实体分桶），有内容块与布局块不报。
func TestInspectEmptyBlock(t *testing.T) {
	doc := newInspectTestDoc()
	doc.ModelSpace = append(doc.ModelSpace, inspectLine(1, 0, 2, 0, 0, 1, 1))
	inspectBlockHeader(doc, 50, "USED")
	doc.Blocks[50] = []any{inspectLine(2, 0, 0, 0, 0, 1, 1)}
	inspectBlockHeader(doc, 60, "EMPTYBLK") // 无任何图元
	inspectBlockHeader(doc, 61, "*Paper_Space")
	inspectBlockHeader(doc, 62, "$Model_Space")
	doc.Blocks[70] = []any{} // 无块头元数据的零实体分桶

	r, err := InspectDocument(doc)
	if err != nil {
		t.Fatalf("InspectDocument: %v", err)
	}
	var empties []Diagnostic
	for _, d := range r.Diagnostics {
		if d.Code != diagEmptyBlock {
			t.Fatalf("意外诊断: %+v", d)
		}
		empties = append(empties, d)
	}
	if len(empties) != 2 || empties[0].Handle != 60 || empties[1].Handle != 70 {
		t.Fatalf("空块诊断异常（期望 60/70）: %+v", empties)
	}
	if empties[0].Block != "EMPTYBLK" || !strings.Contains(empties[0].Message, "EMPTYBLK") {
		t.Fatalf("空块消息异常: %+v", empties[0])
	}
	if empties[1].Block != "*B46" { // 70 == 0x46
		t.Fatalf("合成块名异常: %+v", empties[1])
	}
	if r.Summary.EmptyBlocks != 2 || r.Summary.Blocks != 5 {
		t.Fatalf("汇总异常: %+v", r.Summary)
	}
}

// TestInspectDuplicateHandle 重复句柄：同一非零句柄被多个实体占用
// （跨模型空间与块），按句柄升序报告出现次数；句柄 0 不参与。
func TestInspectDuplicateHandle(t *testing.T) {
	doc := newInspectTestDoc()
	doc.ModelSpace = append(doc.ModelSpace,
		inspectLine(1, 0, 2, 0, 0, 1, 1),
		inspectLine(1, 0, 2, 0, 0, 2, 2),
		&entity.EntCircle{BaseEntity: entity.BaseEntity{Handle: 0, Layer: 0, Mode: 2, TypeName: "CIRCLE"}, Radius: 1},
	)
	inspectBlockHeader(doc, 50, "MYBLOCK")
	doc.Blocks[50] = []any{inspectLine(1, 0, 0, 0, 0, 1, 1)}
	doc.ModelSpace = append(doc.ModelSpace, inspectLine(9, 0, 2, 0, 0, 1, 1))

	r, err := InspectDocument(doc)
	if err != nil {
		t.Fatalf("InspectDocument: %v", err)
	}
	var dups []Diagnostic
	for _, d := range r.Diagnostics {
		if d.Code != diagDuplicateHandle {
			t.Fatalf("意外诊断: %+v", d)
		}
		dups = append(dups, d)
	}
	if len(dups) != 1 {
		t.Fatalf("重复句柄诊断数 = %d, 期望 1: %+v", len(dups), r.Diagnostics)
	}
	if dups[0].Handle != 1 || dups[0].Count != 3 || dups[0].Severity != severityError {
		t.Fatalf("重复句柄诊断异常: %+v", dups[0])
	}
	if r.Summary.DuplicateHandles != 1 {
		t.Fatalf("汇总重复句柄数异常: %+v", r.Summary)
	}
}

// TestInspectDiagnosticsSorted 诊断稳定排序：error 先于 warning；
// 同级按码/句柄排序；重复检查输出字节一致。
func TestInspectDiagnosticsSorted(t *testing.T) {
	build := func() *Document {
		doc := newInspectTestDoc()
		bad := inspectLine(2, 0, 2, 0, 0, 1, 1)
		bad.Start.X = math.NaN()
		doc.ModelSpace = append(doc.ModelSpace, bad)
		doc.ModelSpace = append(doc.ModelSpace, inspectLine(2, 0, 2, 0, 0, 1, 1)) // 与上条同句柄
		inspectBlockHeader(doc, 60, "EMPTYBLK")
		doc.ModelSpace = append(doc.ModelSpace, inspectLine(3, 77, 2, 0, 0, 1, 1)) // 缺图层
		return doc
	}
	r1, err := InspectDocument(build())
	if err != nil {
		t.Fatalf("InspectDocument: %v", err)
	}
	r2, err := InspectDocument(build())
	if err != nil {
		t.Fatalf("InspectDocument(2): %v", err)
	}
	b1, _ := r1.JSON()
	b2, _ := r2.JSON()
	if !bytes.Equal(b1, b2) {
		t.Fatalf("两次检查输出不一致:\n%s\n----\n%s", b1, b2)
	}
	if len(r1.Diagnostics) < 3 {
		t.Fatalf("诊断数异常: %d", len(r1.Diagnostics))
	}
	// error 级（non_finite/duplicate）必须全部排在 warning 级之前
	var firstWarning int
	for i, d := range r1.Diagnostics {
		if d.Severity == severityWarning {
			firstWarning = i
			break
		}
	}
	for i, d := range r1.Diagnostics {
		if d.Severity == severityError && i > firstWarning {
			t.Fatalf("error 级诊断排在 warning 之后: %+v", r1.Diagnostics)
		}
	}
	// error 组内按码排序：duplicate_handle < non_finite_geometry
	if r1.Diagnostics[0].Code != diagDuplicateHandle {
		t.Fatalf("首条诊断应为 duplicate_handle: %+v", r1.Diagnostics[0])
	}
	// 诊断码序整体非降序（同级内）
	codes := make([]string, len(r1.Diagnostics))
	sevs := make([]string, len(r1.Diagnostics))
	for i, d := range r1.Diagnostics {
		codes[i] = d.Code
		sevs[i] = d.Severity
	}
	if !sort.SliceIsSorted(codes, func(i, j int) bool {
		if sevs[i] != sevs[j] {
			return sevs[i] == severityError // 仅校验同级内有序
		}
		return codes[i] < codes[j]
	}) {
		t.Fatalf("同级诊断码未排序: %v", codes)
	}
}

// TestInspectEmptyAndNil nil/空文档/全跳过文档均返回清晰错误，不 panic。
func TestInspectEmptyAndNil(t *testing.T) {
	if _, err := InspectDocument(nil); err == nil || !strings.Contains(err.Error(), "空") {
		t.Fatalf("nil 文档应报空错误, got %v", err)
	}
	// 只有图层表、没有任何实体
	doc := newInspectTestDoc()
	if _, err := InspectDocument(doc); err == nil || !strings.Contains(err.Error(), "空") {
		t.Fatalf("空文档应报空错误, got %v", err)
	}
	// 全部对象跳过：错误中带跳过数
	doc2 := newInspectTestDoc()
	doc2.skipped = 5
	_, err := InspectDocument(doc2)
	if err == nil || !strings.Contains(err.Error(), "5") {
		t.Fatalf("全跳过文档错误应含跳过数, got %v", err)
	}
}

// TestInspectGarbageValuesNoPanic 切片中的 nil/非图元值安全跳过。
func TestInspectGarbageValuesNoPanic(t *testing.T) {
	doc := newInspectTestDoc()
	doc.ModelSpace = []any{nil, 42, "not-an-entity", inspectLine(1, 0, 2, 0, 0, 1, 1)}
	doc.Blocks[50] = []any{nil, struct{ X float64 }{X: math.NaN()}}
	r, err := InspectDocument(doc)
	if err != nil {
		t.Fatalf("InspectDocument: %v", err)
	}
	if r.Summary.Entities != 1 || r.Summary.ModelSpace != 1 || r.Summary.InBlocks != 0 {
		t.Fatalf("垃圾值应被跳过: %+v", r.Summary)
	}
}

// TestInspectReadOnly 检查不得修改文档：DXF 写出与实体对照导出在检查
// 前后逐字节一致，实体坐标不被触碰。
func TestInspectReadOnly(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "line_2013.dwg"))
	if err != nil {
		t.Skip("testdata 不可用")
	}
	doc, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	dumpBefore, err := DumpEntitiesDoc(doc)
	if err != nil {
		t.Fatalf("DumpEntitiesDoc before: %v", err)
	}
	dxfBefore := &bytes.Buffer{}
	if err := WriteDXF(doc, dxfBefore); err != nil {
		t.Fatalf("WriteDXF before: %v", err)
	}
	lineBefore := doc.ModelSpace[0]
	r1, err := InspectDocument(doc)
	if err != nil {
		t.Fatalf("InspectDocument: %v", err)
	}
	r2, err := InspectDocument(doc)
	if err != nil {
		t.Fatalf("InspectDocument(2): %v", err)
	}
	b1, _ := r1.JSON()
	b2, _ := r2.JSON()
	if !bytes.Equal(b1, b2) {
		t.Fatalf("重复检查输出不一致")
	}
	dumpAfter, _ := DumpEntitiesDoc(doc)
	dxfAfter := &bytes.Buffer{}
	if err := WriteDXF(doc, dxfAfter); err != nil {
		t.Fatalf("WriteDXF after: %v", err)
	}
	if dumpBefore != dumpAfter {
		t.Fatalf("检查改变了实体对照导出")
	}
	if !bytes.Equal(dxfBefore.Bytes(), dxfAfter.Bytes()) {
		t.Fatalf("检查改变了 DXF 写出结果")
	}
	if l, ok := lineBefore.(*entity.EntLine); ok {
		if got := doc.ModelSpace[0].(*entity.EntLine); got.Start != l.Start || got.End != l.End {
			t.Fatalf("检查触碰了实体坐标")
		}
	}
	if doc.Skipped() != 0 {
		t.Fatalf("skipped 被修改: %d", doc.Skipped())
	}
}

// TestInspectRealCorpus 全量 testdata DWG：检查不报错、不产生非有限
// 几何诊断（解析后的几何必须有限）、JSON 合法且不含 NaN 字面量。
func TestInspectRealCorpus(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "testdata", "*.dwg"))
	if err != nil || len(files) == 0 {
		t.Skip("testdata 不可用")
	}
	sort.Strings(files)
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		doc, perr := Parse(data)
		if perr != nil {
			continue
		}
		r, ierr := InspectDocument(doc)
		if ierr != nil {
			t.Errorf("%s: InspectDocument: %v", filepath.Base(f), ierr)
			continue
		}
		for _, d := range r.Diagnostics {
			if d.Code == diagNonFiniteGeometry {
				t.Errorf("%s: 真实文件出现非有限几何: %+v", filepath.Base(f), d)
			}
		}
		buf, jerr := r.JSON()
		if jerr != nil {
			t.Errorf("%s: JSON 编码失败: %v", filepath.Base(f), jerr)
			continue
		}
		var generic any
		if uerr := json.Unmarshal(buf, &generic); uerr != nil {
			t.Errorf("%s: 输出不是合法 JSON: %v", filepath.Base(f), uerr)
		}
	}
}
