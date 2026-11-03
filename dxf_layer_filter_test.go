// dxf_layer_filter_test.go 验证 WriteDXFWithOptions 的图层子集拆图语义：
// 模型空间、pre-R13 图纸空间、嵌套块、空块、包含/排除组合、未知图层一次性
// 校验、无剩余实体、写出后句柄全局唯一，以及默认路径与 WriteDXF 逐字节一致。
package cad

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/unitedrhino/go-cad/internal/container"
	"github.com/unitedrhino/go-cad/internal/drawing"
	"github.com/unitedrhino/go-cad/internal/entity"
	"github.com/unitedrhino/go-cad/internal/object"
)

// 测试固定句柄布局（图层/块头/实体互不撞车）。
const (
	lhA uint64 = 0x40 // 图层 A
	lhB uint64 = 0x41 // 图层 B
	lhC uint64 = 0x42 // 图层 C
	lhD uint64 = 0x43 // 空图层 D（LAYER 表有登记、无实体）

	blkOUT   uint64 = 0x50 // 普通块：A/B 图层内容 + 嵌套 INSERT→IN
	blkIN    uint64 = 0x60 // 嵌套块：A/B 图层内容
	blkEMPTY uint64 = 0x70 // 空块
	blkUNREF uint64 = 0x80 // 不可达块（含 A 图层内容）
	blkXBLK  uint64 = 0x90 // 仅含 C 图层内容的块
	blkDECOY uint64 = 0xA0 // 最大块启发式吸收块（4 个 B 图层实体）
)

// lineEnt 构造指定图层的 LINE 实体（mode 2=模型空间直属）。
func lineEnt(h, layer uint64, mode uint8, owner uint64, x float64) *entity.EntLine {
	return &entity.EntLine{
		BaseEntity: entity.BaseEntity{Handle: h, Layer: layer, Mode: mode, Owner: owner},
		Start:      entity.Point3{X: x, Y: 0},
		End:        entity.Point3{X: x + 1, Y: 1},
	}
}

// insertEnt 构造指定图层的 INSERT（引用 blockHeader）。
func insertEnt(h, layer, blockHeader uint64, mode uint8, owner uint64) *entity.EntInsert {
	return &entity.EntInsert{
		BaseEntity:  entity.BaseEntity{Handle: h, Layer: layer, Mode: mode, Owner: owner},
		Position:    entity.Point3{X: 1, Y: 1},
		Scale:       entity.Point3{X: 1, Y: 1, Z: 1},
		BlockHeader: blockHeader,
	}
}

// registerBlock 在 InternalObjs 登记块头真名（与 DWG/DXF 来源同口径）。
func registerBlock(doc *Document, h uint64, name string) {
	doc.InternalObjs[h] = &object.ObjGeneric{
		Name:   "BLOCK_HEADER",
		Handle: h,
		Fields: []object.ObjField{{Key: "name", Val: name}},
	}
}

// buildFilterFixture 构造覆盖块/图层各场景的文档：
//   - 模型空间：A/B/0 层 LINE + INSERT→OUT(A 层)/EMPTY(B 层)/XBLK(A 层)
//   - OUT：A 层 LINE、B 层 LINE、A 层 INSERT→IN
//   - IN：A 层 LINE、B 层 LINE
//   - EMPTY：空块；UNREF：不可达但含 A 层内容；XBLK：仅 C 层内容
//   - DECOY：实体数最多（4 个 B 层 LINE），吸收“最大块并入模型空间”启发式，
//     保证 OUT/IN 等普通块在 BLOCKS 段正常输出
func buildFilterFixture() *Document {
	doc := &Document{
		Ver:          container.VerR2000,
		Blocks:       map[uint64][]any{},
		LayerColors:  map[uint64]drawing.LayerColor{},
		InternalObjs: map[uint64]*object.ObjGeneric{},
	}
	doc.LayerColors[lhA] = drawing.LayerColor{Name: "A", Index: 1}
	doc.LayerColors[lhB] = drawing.LayerColor{Name: "B", Index: 5}
	doc.LayerColors[lhC] = drawing.LayerColor{Name: "C", Index: 3}
	doc.LayerColors[lhD] = drawing.LayerColor{Name: "D", Index: 2}

	doc.ModelSpace = []any{
		lineEnt(0x101, lhA, 2, 0, 0),
		lineEnt(0x102, lhB, 2, 0, 10),
		lineEnt(0x103, 0, 2, 0, 20),
		insertEnt(0x110, lhA, blkOUT, 2, 0),
		insertEnt(0x111, lhB, blkEMPTY, 2, 0),
		insertEnt(0x112, lhA, blkXBLK, 2, 0),
	}
	doc.Blocks[blkOUT] = []any{
		lineEnt(0x201, lhA, 0, blkOUT, 100),
		lineEnt(0x202, lhB, 0, blkOUT, 110),
		insertEnt(0x203, lhA, blkIN, 0, blkOUT),
	}
	doc.Blocks[blkIN] = []any{
		lineEnt(0x301, lhA, 0, blkIN, 200),
		lineEnt(0x302, lhB, 0, blkIN, 210),
	}
	doc.Blocks[blkEMPTY] = nil
	doc.Blocks[blkUNREF] = []any{lineEnt(0x401, lhA, 0, blkUNREF, 300)}
	doc.Blocks[blkXBLK] = []any{lineEnt(0x501, lhC, 0, blkXBLK, 400)}
	doc.Blocks[blkDECOY] = []any{
		lineEnt(0x601, lhB, 0, blkDECOY, 500),
		lineEnt(0x602, lhB, 0, blkDECOY, 510),
		lineEnt(0x603, lhB, 0, blkDECOY, 520),
		lineEnt(0x604, lhB, 0, blkDECOY, 530),
	}
	for _, h := range []struct {
		h    uint64
		name string
	}{
		{blkOUT, "OUT"}, {blkIN, "IN"}, {blkEMPTY, "EMPTY"},
		{blkUNREF, "UNREF"}, {blkXBLK, "XBLK"}, {blkDECOY, "DECOY"},
	} {
		registerBlock(doc, h.h, h.name)
	}
	return doc
}

// ---- 输出结构解析 ----

// dxfRecOut 解析后的单个 0 组码记录（含所属段与同名段序号）。
type dxfRecOut struct {
	sec     string
	secInst int // 同名 SECTION 的出现序号（0 起；pre-R13 图纸空间为第二个 ENTITIES）
	typ     string
	codes   []int
	vals    []string
}

func (r dxfRecOut) get(code int) (string, bool) {
	for i, c := range r.codes {
		if c == code {
			return strings.TrimSpace(r.vals[i]), true
		}
	}
	return "", false
}

// parseDXFRecords 把 DXF 输出解析为带段归属的记录序列。
func parseDXFRecords(t *testing.T, data []byte) []dxfRecOut {
	t.Helper()
	codes, vals := parseDXFGroups(t, data)
	var recs []dxfRecOut
	sec := ""
	secCount := map[string]int{}
	for i := 0; i < len(codes); {
		if codes[i] != 0 {
			i++
			continue
		}
		typ := vals[i]
		if typ == "SECTION" {
			for j := i + 1; j < len(codes) && codes[j] != 0; j++ {
				if codes[j] == 2 {
					sec = vals[j]
				}
			}
			secCount[sec]++
			i++
			continue
		}
		if typ == "ENDSEC" {
			sec = ""
			i++
			continue
		}
		r := dxfRecOut{sec: sec, secInst: secCount[sec] - 1, typ: typ}
		j := i + 1
		for ; j < len(codes) && codes[j] != 0; j++ {
			r.codes = append(r.codes, codes[j])
			r.vals = append(r.vals, vals[j])
		}
		recs = append(recs, r)
		i = j
	}
	return recs
}

// layerTableNames 收集 TABLES 段 LAYER 表的全部图层名（按输出顺序）。
func layerTableNames(recs []dxfRecOut) []string {
	var out []string
	for _, r := range recs {
		if r.sec == "TABLES" && r.typ == "LAYER" {
			if n, ok := r.get(2); ok {
				out = append(out, n)
			}
		}
	}
	return out
}

// blockDefNames 收集 BLOCKS 段用户块定义名（布局块 *Model_Space/
// *Paper_Space 恒存在，不计入）。
func blockDefNames(recs []dxfRecOut) []string {
	var out []string
	for _, r := range recs {
		if r.sec == "BLOCKS" && r.typ == "BLOCK" {
			if n, ok := r.get(2); ok {
				if strings.HasPrefix(n, "*") || strings.HasPrefix(n, "$") {
					continue
				}
				out = append(out, n)
			}
		}
	}
	return out
}

// entityLayers 收集指定段内具体实体（LINE/INSERT/...）的图层名序列；
// blockName 非空时仅统计该块定义（BLOCK…ENDBLK 之间）的内容。
func entityLayers(recs []dxfRecOut, section, blockName string) []string {
	var out []string
	inBlock := false
	for _, r := range recs {
		if r.sec != section {
			continue
		}
		if section == "BLOCKS" {
			if r.typ == "BLOCK" {
				n, _ := r.get(2)
				inBlock = n == blockName
				continue
			}
			if r.typ == "ENDBLK" {
				inBlock = false
				continue
			}
			if blockName != "" && !inBlock {
				continue
			}
		}
		switch r.typ {
		case "LINE", "CIRCLE", "ARC", "POINT", "ELLIPSE", "LWPOLYLINE", "TEXT",
			"MTEXT", "INSERT", "SOLID", "TRACE", "3DFACE", "RAY", "XLINE",
			"SPLINE", "HATCH", "DIMENSION", "LEADER", "MLINE", "POLYLINE",
			"VERTEX", "SEQEND", "SHAPE", "TOLERANCE", "IMAGE", "VIEWPORT":
			if n, ok := r.get(8); ok {
				out = append(out, r.typ+"@"+n)
			}
		}
	}
	return out
}

// insertRefs 收集指定段内 INSERT 的块引用名序列。
func insertRefs(recs []dxfRecOut, section, blockName string) []string {
	var out []string
	inBlock := false
	for _, r := range recs {
		if r.sec != section {
			continue
		}
		if section == "BLOCKS" {
			if r.typ == "BLOCK" {
				n, _ := r.get(2)
				inBlock = n == blockName
				continue
			}
			if r.typ == "ENDBLK" {
				inBlock = false
				continue
			}
			if blockName != "" && !inBlock {
				continue
			}
		}
		if r.typ == "INSERT" {
			if n, ok := r.get(2); ok {
				out = append(out, n)
			}
		}
	}
	return out
}

// assertHandlesUnique 全文件（含 TABLES/BLOCKS/ENTITIES 各段）组码 5
// 句柄必须全局唯一。零 LAYER 记录按本库写出口径句柄为 0，属唯一豁免。
func assertHandlesUnique(t *testing.T, recs []dxfRecOut) {
	t.Helper()
	seen := map[uint64]string{}
	for _, r := range recs {
		v, ok := r.get(5)
		if !ok {
			continue
		}
		h, err := strconv.ParseUint(v, 16, 64)
		if err != nil {
			t.Errorf("记录 %s 句柄非法 %q", r.typ, v)
			continue
		}
		if h == 0 {
			if r.sec == "TABLES" && r.typ == "LAYER" {
				if n, _ := r.get(2); n == "0" {
					continue
				}
			}
			t.Errorf("记录 %s/%s 句柄为 0", r.sec, r.typ)
			continue
		}
		if prev, dup := seen[h]; dup {
			t.Errorf("句柄 %X 重复（%s 与 %s）", h, prev, r.sec+"/"+r.typ)
		}
		seen[h] = r.sec + "/" + r.typ
	}
}

// writeFiltered 执行子集写出并解析结果。
func writeFiltered(t *testing.T, doc *Document, opts *DXFWriteOptions) ([]byte, []dxfRecOut) {
	t.Helper()
	var buf bytes.Buffer
	if err := WriteDXFWithOptions(doc, &buf, opts); err != nil {
		t.Fatalf("WriteDXFWithOptions 失败: %v", err)
	}
	data := buf.Bytes()
	return data, parseDXFRecords(t, data)
}

// containsStr 线性包含断言辅助。
func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// ---- 用例 ----

// TestDXFLayerFilterIncludeOnly 白名单只含 A：模型空间只留 A 与 0 层实体；
// 悬空 INSERT（→空块 EMPTY、→内容全被滤掉的 XBLK）被修剪；OUT/IN 块定义
// 保留但块内 B 层实体不得因子 INSERT 选中而被带入；UNREF 不可达不写出；
// LAYER 表只剩 0 与 A。
func TestDXFLayerFilterIncludeOnly(t *testing.T) {
	doc := buildFilterFixture()
	_, recs := writeFiltered(t, doc, &DXFWriteOptions{IncludeLayers: []string{"A"}})

	ms := entityLayers(recs, "ENTITIES", "")
	if !containsStr(ms, "LINE@A") || !containsStr(ms, "LINE@0") {
		t.Errorf("模型空间应保留 A/0 层 LINE，got %v", ms)
	}
	if containsStr(ms, "LINE@B") {
		t.Errorf("模型空间不应含 B 层 LINE，got %v", ms)
	}
	refs := insertRefs(recs, "ENTITIES", "")
	if !containsStr(refs, "OUT") {
		t.Errorf("应保留 A 层 INSERT→OUT，got %v", refs)
	}
	for _, bad := range []string{"EMPTY", "XBLK"} {
		if containsStr(refs, bad) {
			t.Errorf("INSERT→%s 指向无选中内容的块，应被修剪，got %v", bad, refs)
		}
	}

	blocks := blockDefNames(recs)
	for _, want := range []string{"OUT", "IN"} {
		if !containsStr(blocks, want) {
			t.Errorf("BLOCKS 段应保留可达块 %s，got %v", want, blocks)
		}
	}
	for _, bad := range []string{"EMPTY", "UNREF", "XBLK", "DECOY"} {
		if containsStr(blocks, bad) {
			t.Errorf("BLOCKS 段不应含 %s（空/不可达/滤空/启发式吸收），got %v", bad, blocks)
		}
	}

	// 块内实体按展开前自身图层判定：父 INSERT 在 A 层，OUT/IN 内的
	// B 层 LINE 仍须排除。
	outEnts := entityLayers(recs, "BLOCKS", "OUT")
	if !containsStr(outEnts, "LINE@A") {
		t.Errorf("OUT 块应保留 A 层 LINE，got %v", outEnts)
	}
	if containsStr(outEnts, "LINE@B") {
		t.Errorf("OUT 块不得带入 B 层 LINE，got %v", outEnts)
	}
	if !containsStr(outEnts, "INSERT@A") {
		t.Errorf("OUT 块应保留 A 层嵌套 INSERT→IN，got %v", outEnts)
	}
	inEnts := entityLayers(recs, "BLOCKS", "IN")
	if !containsStr(inEnts, "LINE@A") || containsStr(inEnts, "LINE@B") {
		t.Errorf("IN 块应仅保留 A 层 LINE，got %v", inEnts)
	}

	layers := layerTableNames(recs)
	if len(layers) != 2 || layers[0] != "0" || !containsStr(layers, "A") {
		t.Errorf("LAYER 表应恰为 [0 A]，got %v", layers)
	}
	assertHandlesUnique(t, recs)

	// 写出结果必须是可被本库读回的合法 DXF。
	if _, err := ParseDXF(dataOf(t, doc, &DXFWriteOptions{IncludeLayers: []string{"A"}})); err != nil {
		t.Errorf("过滤后 DXF 无法 ParseDXF 读回: %v", err)
	}
}

// dataOf 便捷重跑一次写出供读回校验使用。
func dataOf(t *testing.T, doc *Document, opts *DXFWriteOptions) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := WriteDXFWithOptions(doc, &buf, opts); err != nil {
		t.Fatalf("WriteDXFWithOptions 失败: %v", err)
	}
	return buf.Bytes()
}

// TestDXFLayerFilterExclude 排除 B：B 层实体从模型空间与块内一并消失；
// XBLK（C 层内容）仍可达且含选中内容，INSERT 与块定义均保留；最大块
// DECOY 的 B 层内容经模型空间启发式并入后同样被滤除。
func TestDXFLayerFilterExclude(t *testing.T) {
	doc := buildFilterFixture()
	_, recs := writeFiltered(t, doc, &DXFWriteOptions{ExcludeLayers: []string{"B"}})

	ms := entityLayers(recs, "ENTITIES", "")
	if containsStr(ms, "LINE@B") {
		t.Errorf("排除 B 后模型空间不应含 B 层 LINE（含最大块并入内容），got %v", ms)
	}
	if !containsStr(ms, "LINE@A") {
		t.Errorf("模型空间应保留 A 层 LINE，got %v", ms)
	}
	refs := insertRefs(recs, "ENTITIES", "")
	if !containsStr(refs, "XBLK") {
		t.Errorf("XBLK 的 C 层内容未被排除，INSERT→XBLK 应保留，got %v", refs)
	}
	if containsStr(refs, "EMPTY") {
		t.Errorf("空块 EMPTY 的引用应被修剪，got %v", refs)
	}
	blocks := blockDefNames(recs)
	if !containsStr(blocks, "XBLK") || !containsStr(blocks, "OUT") || !containsStr(blocks, "IN") {
		t.Errorf("应保留 OUT/IN/XBLK 块定义，got %v", blocks)
	}
	if containsStr(blocks, "DECOY") || containsStr(blocks, "UNREF") || containsStr(blocks, "EMPTY") {
		t.Errorf("不应输出 DECOY/UNREF/EMPTY，got %v", blocks)
	}
	if containsStr(entityLayers(recs, "BLOCKS", "OUT"), "LINE@B") ||
		containsStr(entityLayers(recs, "BLOCKS", "IN"), "LINE@B") {
		t.Errorf("OUT/IN 块内 B 层实体应被排除")
	}
	layers := layerTableNames(recs)
	if containsStr(layers, "B") {
		t.Errorf("LAYER 表不应保留无实体的 B 层，got %v", layers)
	}
	for _, want := range []string{"0", "A", "C"} {
		if !containsStr(layers, want) {
			t.Errorf("LAYER 表缺 %s，got %v", want, layers)
		}
	}
	assertHandlesUnique(t, recs)
}

// TestDXFLayerFilterIncludeExcludeCombo 包含与排除同时命中同一图层时排除
// 优先：include A + exclude A 后只剩恒保留的 0 层实体，所有块定义消失。
func TestDXFLayerFilterIncludeExcludeCombo(t *testing.T) {
	doc := buildFilterFixture()
	_, recs := writeFiltered(t, doc, &DXFWriteOptions{
		IncludeLayers: []string{"A"},
		ExcludeLayers: []string{"A"},
	})
	ms := entityLayers(recs, "ENTITIES", "")
	if !containsStr(ms, "LINE@0") {
		t.Errorf("零层实体应恒保留，got %v", ms)
	}
	for _, bad := range []string{"LINE@A", "LINE@B", "LINE@C", "INSERT@A", "INSERT@B"} {
		if containsStr(ms, bad) {
			t.Errorf("排除优先后不应含 %s，got %v", bad, ms)
		}
	}
	if len(blockDefNames(recs)) != 0 {
		t.Errorf("不应保留任何块定义，got %v", blockDefNames(recs))
	}
	layers := layerTableNames(recs)
	if len(layers) != 1 || layers[0] != "0" {
		t.Errorf("LAYER 表应仅余 0 层，got %v", layers)
	}
	assertHandlesUnique(t, recs)

	// 零层豁免：显式排除 0 也不得移除零层实体
	_, recs0 := writeFiltered(t, doc, &DXFWriteOptions{ExcludeLayers: []string{"0"}})
	if !containsStr(entityLayers(recs0, "ENTITIES", ""), "LINE@0") {
		t.Errorf("零层恒保留，排除 0 不应生效，got %v", entityLayers(recs0, "ENTITIES", ""))
	}
}

// TestDXFLayerFilterUnknownNames 未知图层名一次性汇总校验：包含与排除列表
// 中的未知名字去重排序后一次报错，且写出器不产生任何字节。
func TestDXFLayerFilterUnknownNames(t *testing.T) {
	doc := buildFilterFixture()
	var buf bytes.Buffer
	err := WriteDXFWithOptions(doc, &buf, &DXFWriteOptions{
		IncludeLayers: []string{"A", "NOPE1", "NOPE1"},
		ExcludeLayers: []string{"ZZZ2", "B"},
	})
	if err == nil {
		t.Fatal("未知图层应返回错误")
	}
	msg := err.Error()
	if !strings.Contains(msg, "NOPE1") || !strings.Contains(msg, "ZZZ2") {
		t.Errorf("错误应汇总全部未知图层名，got %q", msg)
	}
	if strings.Contains(msg, " A") || strings.Contains(msg, "A,") {
		t.Errorf("已知图层名 A 不应报错，got %q", msg)
	}
	if !strings.HasSuffix(msg, "NOPE1, ZZZ2") {
		t.Errorf("未知图层名应去重并按序输出，got %q", msg)
	}
	if buf.Len() != 0 {
		t.Errorf("校验失败前不得写出任何字节，已写 %d 字节", buf.Len())
	}

	// 已知但无实体的图层不是未知图层：include D（表中有登记、无实体）合法
	var okBuf bytes.Buffer
	if err := WriteDXFWithOptions(doc, &okBuf, &DXFWriteOptions{
		IncludeLayers: []string{"D"},
	}); err != nil {
		t.Errorf("无实体的已知图层 D 不应报未知错误: %v", err)
	}
}

// TestDXFLayerFilterNoRemainingEntities 只选无实体图层 D：输出无任何实体，
// 但文件结构完整（HEADER/TABLES/BLOCKS/ENTITIES/EOF），LAYER 表仅 0 层，
// BLOCKS 仅 *Model_Space 空块对。注意零层恒保留，故选用无零层实体的文档。
func TestDXFLayerFilterNoRemainingEntities(t *testing.T) {
	doc := &Document{
		Ver:          container.VerR2000,
		Blocks:       map[uint64][]any{},
		LayerColors:  map[uint64]drawing.LayerColor{lhA: {Name: "A", Index: 1}, lhD: {Name: "D", Index: 2}},
		InternalObjs: map[uint64]*object.ObjGeneric{},
	}
	doc.ModelSpace = []any{
		lineEnt(0x101, lhA, 2, 0, 0),
		lineEnt(0x102, lhA, 2, 0, 10),
	}
	data, recs := writeFiltered(t, doc, &DXFWriteOptions{IncludeLayers: []string{"D"}})

	ents := entityLayers(recs, "ENTITIES", "")
	if len(ents) != 0 {
		t.Errorf("不应有任何实体，got %v", ents)
	}
	if len(blockDefNames(recs)) != 0 {
		t.Errorf("不应有块定义，got %v", blockDefNames(recs))
	}
	layers := layerTableNames(recs)
	if len(layers) != 1 || layers[0] != "0" {
		t.Errorf("LAYER 表应仅 0 层，got %v", layers)
	}
	// 段完整性：恰一个空 ENTITIES 段 + EOF
	out := string(data)
	if strings.Count(out, "ENTITIES\r\n") != 1 {
		t.Errorf("无实体时应恰有一个 ENTITIES 段")
	}
	if !strings.HasSuffix(out, "  0\r\nEOF\r\n") {
		t.Errorf("输出应以 EOF 结束")
	}
	assertHandlesUnique(t, recs)
	if _, err := ParseDXF(data); err != nil {
		t.Errorf("空实体 DXF 应可被 ParseDXF 读回: %v", err)
	}
}

// TestDXFLayerFilterPreR13PaperSpace pre-R13 图纸空间实体（67=1）同样按
// 自身图层过滤，并在独立的第二个 ENTITIES 段输出；模型/图纸空间互不污染。
func TestDXFLayerFilterPreR13PaperSpace(t *testing.T) {
	doc := &Document{
		Ver:          container.VerR11,
		Blocks:       map[uint64][]any{},
		LayerColors:  map[uint64]drawing.LayerColor{},
		InternalObjs: map[uint64]*object.ObjGeneric{},
	}
	doc.LayerColors[lhA] = drawing.LayerColor{Name: "A", Index: 1}
	doc.LayerColors[lhB] = drawing.LayerColor{Name: "B", Index: 5}
	doc.ModelSpace = []any{lineEnt(0x101, lhA, 2, 0, 0)}
	doc.PspaceSpace = []any{
		lineEnt(0x701, lhA, 1, 0, 60),
		lineEnt(0x702, lhB, 1, 0, 70),
	}
	_, recs := writeFiltered(t, doc, &DXFWriteOptions{IncludeLayers: []string{"A"}})

	// 两个 ENTITIES 段：模型段（序号 0）+ pre-R13 图纸段（序号 1）
	var model, paper []string
	for _, r := range recs {
		if r.sec != "ENTITIES" || r.typ != "LINE" {
			continue
		}
		layer, _ := r.get(8)
		_, has67 := r.get(67)
		switch r.secInst {
		case 0:
			if has67 {
				t.Errorf("模型空间实体不应带 67=1")
			}
			model = append(model, layer)
		case 1:
			if v, ok := r.get(67); !ok || v != "1" {
				t.Errorf("图纸空间实体应带 67=1")
			}
			paper = append(paper, layer)
		}
	}
	if len(model) != 1 || model[0] != "A" {
		t.Errorf("模型空间应仅余 A 层 LINE，got %v", model)
	}
	if len(paper) != 1 || paper[0] != "A" {
		t.Errorf("图纸空间应仅余 A 层实体（67=1），got %v", paper)
	}
	assertHandlesUnique(t, recs)
}

// TestDXFLayerFilterNestedInsertExcluded 嵌套 INSERT 自身位于排除图层时，
// 其引用的下游块不可达：外层块保留（仍有 A 层内容），下游块定义不写出。
func TestDXFLayerFilterNestedInsertExcluded(t *testing.T) {
	doc := &Document{
		Ver:          container.VerR2000,
		Blocks:       map[uint64][]any{},
		LayerColors:  map[uint64]drawing.LayerColor{lhA: {Name: "A", Index: 1}, lhB: {Name: "B", Index: 5}},
		InternalObjs: map[uint64]*object.ObjGeneric{},
	}
	const outer, inner, decoy uint64 = 0x50, 0x60, 0xA0
	doc.ModelSpace = []any{insertEnt(0x110, lhA, outer, 2, 0)}
	doc.Blocks[outer] = []any{
		lineEnt(0x201, lhA, 0, outer, 1),
		insertEnt(0x202, lhB, inner, 0, outer), // 嵌套引用在 B 层 → 被滤除
	}
	doc.Blocks[inner] = []any{lineEnt(0x301, lhA, 0, inner, 2)}
	doc.Blocks[decoy] = []any{ // 严格最大，吸收最大块启发式，避免等 tie 不确定性
		lineEnt(0x601, lhB, 0, decoy, 9),
		lineEnt(0x602, lhB, 0, decoy, 9),
		lineEnt(0x603, lhB, 0, decoy, 9),
	}
	registerBlock(doc, outer, "OUTER")
	registerBlock(doc, inner, "INNER")
	registerBlock(doc, decoy, "DECOY")

	_, recs := writeFiltered(t, doc, &DXFWriteOptions{IncludeLayers: []string{"A"}})
	blocks := blockDefNames(recs)
	if !containsStr(blocks, "OUTER") {
		t.Errorf("OUTER 含 A 层内容应保留，got %v", blocks)
	}
	if containsStr(blocks, "INNER") {
		t.Errorf("到达 INNER 的唯一 INSERT 在 B 层，INNER 应不可达，got %v", blocks)
	}
	outEnts := entityLayers(recs, "BLOCKS", "OUTER")
	if !containsStr(outEnts, "LINE@A") {
		t.Errorf("OUTER 应保留 A 层 LINE，got %v", outEnts)
	}
	if containsStr(outEnts, "INSERT@B") {
		t.Errorf("OUTER 内 B 层嵌套 INSERT 应被滤除，got %v", outEnts)
	}
	assertHandlesUnique(t, recs)
}

// TestDXFLayerFilterEmptyBlockUnreferenced 空块与不可达块在全量不过滤但指定
// 排除无关图层时同样不写出定义（无选中内容 / 不可达两条规则独立生效）。
func TestDXFLayerFilterEmptyBlockUnreferenced(t *testing.T) {
	doc := buildFilterFixture()
	// 排除一个文档中不存在任何实体的图层不影响选择，但未知图层会报错——
	// 这里用排除 B 触发 EMPTY（原本由 B 层 INSERT 引用）失去引用内容场景，
	// 并直接断言 EMPTY/UNREF 始终不出现。
	_, recs := writeFiltered(t, doc, &DXFWriteOptions{ExcludeLayers: []string{"B"}})
	blocks := blockDefNames(recs)
	if containsStr(blocks, "EMPTY") {
		t.Errorf("空块 EMPTY 不应写出定义，got %v", blocks)
	}
	if containsStr(blocks, "UNREF") {
		t.Errorf("不可达块 UNREF 不应写出定义，got %v", blocks)
	}
}

// TestDXFLayerFilterDefaultByteIdentical 不传选项（nil）或传空选项时，
// 输出必须与 WriteDXF 逐字节一致。使用单图层/单块的确定性文档，规避
// 全量路径既有的 map 遍历顺序非确定性。
func TestDXFLayerFilterDefaultByteIdentical(t *testing.T) {
	doc := &Document{
		Ver:          container.VerR2000,
		Blocks:       map[uint64][]any{},
		LayerColors:  map[uint64]drawing.LayerColor{lhA: {Name: "A", Index: 1}},
		InternalObjs: map[uint64]*object.ObjGeneric{},
	}
	const kb uint64 = 0x50
	doc.ModelSpace = []any{
		lineEnt(0x101, lhA, 2, 0, 0),
		lineEnt(0x102, 0, 2, 0, 20),
		insertEnt(0x110, lhA, kb, 2, 0),
	}
	doc.Blocks[kb] = []any{lineEnt(0x201, lhA, 0, kb, 100)}
	registerBlock(doc, kb, "KB")

	var base bytes.Buffer
	if err := WriteDXF(doc, &base); err != nil {
		t.Fatalf("WriteDXF 失败: %v", err)
	}
	for _, opts := range []*DXFWriteOptions{
		nil,
		{},
		{IncludeLayers: []string{}},
		{ExcludeLayers: []string{}},
		{IncludeLayers: []string{}, ExcludeLayers: []string{}},
	} {
		var buf bytes.Buffer
		if err := WriteDXFWithOptions(doc, &buf, opts); err != nil {
			t.Fatalf("opts=%v 写出失败: %v", opts, err)
		}
		if !bytes.Equal(buf.Bytes(), base.Bytes()) {
			t.Errorf("opts=%v 输出与 WriteDXF 非逐字节一致", opts)
		}
	}
}

// TestDXFLayerFilterNilDoc nil 文档返回错误且不 panic。
func TestDXFLayerFilterNilDoc(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteDXFWithOptions(nil, &buf, &DXFWriteOptions{IncludeLayers: []string{"A"}}); err == nil {
		t.Fatal("nil 文档应返回错误")
	}
}

// TestDXFLayerFilterPolylineVertexLayers POLYLINE 聚合体按头实体图层选中，
// 其 VERTEX 子实体随聚合体输出（顶点脱离 POLYLINE 无独立意义）；顶点引用
// 的图层必须进入 LAYER 表以保证闭合，且顶点不产生独立实体/伪块定义。
func TestDXFLayerFilterPolylineVertexLayers(t *testing.T) {
	doc := &Document{
		Ver:          container.VerR2000,
		Blocks:       map[uint64][]any{},
		LayerColors:  map[uint64]drawing.LayerColor{lhA: {Name: "A", Index: 1}, lhB: {Name: "B", Index: 5}},
		InternalObjs: map[uint64]*object.ObjGeneric{},
	}
	poly := &entity.EntPolyline2d{
		BaseEntity:   entity.BaseEntity{Handle: 0x101, Layer: lhA, Mode: 2},
		OwnedHandles: []uint64{0x102},
	}
	vert := &entity.EntVertex2d{
		BaseEntity: entity.BaseEntity{Handle: 0x102, Layer: lhB, Mode: 0, Owner: 0x101},
		Position:   entity.Point3{X: 1, Y: 1},
	}
	doc.ModelSpace = []any{poly}
	doc.Blocks[0x101] = []any{vert} // 解析归类：顶点按 owner=POLYLINE 句柄入伪块

	_, recs := writeFiltered(t, doc, &DXFWriteOptions{IncludeLayers: []string{"A"}})

	ents := entityLayers(recs, "ENTITIES", "")
	if !containsStr(ents, "POLYLINE@A") {
		t.Errorf("POLYLINE 头在 A 层应整体保留，got %v", ents)
	}
	if !containsStr(ents, "VERTEX@B") {
		t.Errorf("VERTEX 应随 POLYLINE 聚合输出（保留自身 B 层引用），got %v", ents)
	}
	// 伪块键不得输出为块定义
	for _, bn := range blockDefNames(recs) {
		if strings.Contains(bn, "B101") {
			t.Errorf("POLYLINE 伪块不应输出定义，got %s", bn)
		}
	}
	// 顶点引用 B 层 → LAYER 表须含 B（实体图层闭合）
	layers := layerTableNames(recs)
	if !containsStr(layers, "A") || !containsStr(layers, "B") {
		t.Errorf("LAYER 表应含聚合体及其顶点引用的图层，got %v", layers)
	}
	assertHandlesUnique(t, recs)
	if _, err := ParseDXF(dataOf(t, doc, &DXFWriteOptions{IncludeLayers: []string{"A"}})); err != nil {
		t.Errorf("聚合体子集 DXF 应可读回: %v", err)
	}
}

// TestDXFLayerFilterCorpusInvariants 真实 DWG 语料上的子集不变量：
// 仅包含零层时，输出结构合法、句柄全局唯一、无悬空图层引用、块定义均非空、
// 可被 ParseDXF 读回，且实体数不多于全量输出；排除一个真实图层后输出中
// 任何段落都不得再出现该图层的实体（含块内）。
func TestDXFLayerFilterCorpusInvariants(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "*.dwg"))
	if err != nil || len(files) == 0 {
		t.Skip("无测试样本")
	}
	checked := 0
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		doc, err := Parse(data)
		if err != nil {
			continue
		}
		name := filepath.Base(f)

		// 1) 仅零层子集
		var sub bytes.Buffer
		if err := WriteDXFWithOptions(doc, &sub, &DXFWriteOptions{IncludeLayers: []string{"0"}}); err != nil {
			t.Errorf("%s: 零层子集写出失败: %v", name, err)
			continue
		}
		subData := sub.Bytes()
		if strings.Count(string(subData), "\n") != strings.Count(string(subData), "\r\n") {
			t.Errorf("%s: 输出存在裸 LF", name)
		}
		recs := parseDXFRecords(t, subData)
		assertHandlesUnique(t, recs)

		// 图层闭合：每个实体（含块内）的 8 组码图层必须在 LAYER 表中
		layerSet := map[string]bool{}
		for _, n := range layerTableNames(recs) {
			layerSet[n] = true
		}
		if !layerSet["0"] {
			t.Errorf("%s: LAYER 表丢失零层", name)
		}
		for _, r := range recs {
			if r.sec != "ENTITIES" && r.sec != "BLOCKS" {
				continue
			}
			if r.typ == "BLOCK" || r.typ == "ENDBLK" {
				continue
			}
			if v, ok := r.get(8); ok && !layerSet[v] {
				t.Errorf("%s: 实体 %s 引用了不在 LAYER 表的图层 %q", name, r.typ, v)
			}
		}
		// 每个用户块定义至少含一个内容实体
		cur := ""
		contentCount := map[string]int{}
		for _, r := range recs {
			if r.sec != "BLOCKS" {
				continue
			}
			if r.typ == "BLOCK" {
				cur, _ = r.get(2)
				continue
			}
			if r.typ == "ENDBLK" {
				cur = ""
				continue
			}
			if cur != "" && !strings.HasPrefix(cur, "*") && !strings.HasPrefix(cur, "$") {
				contentCount[cur]++
			}
		}
		for bn, n := range contentCount {
			if n == 0 {
				t.Errorf("%s: 块定义 %s 为空，不应写出", name, bn)
			}
		}
		if _, err := ParseDXF(subData); err != nil {
			t.Errorf("%s: 子集 DXF 无法读回: %v", name, err)
		}

		// 2) 排除一个文档真实图层：输出任何段落均不得残留该图层实体
		var exName string
		for _, lc := range doc.LayerColors {
			if n := lc.Name; n != "" && n != "0" {
				exName = n
				break
			}
		}
		if exName != "" {
			var ex bytes.Buffer
			if err := WriteDXFWithOptions(doc, &ex, &DXFWriteOptions{ExcludeLayers: []string{exName}}); err != nil {
				t.Errorf("%s: 排除 %s 写出失败: %v", name, exName, err)
			} else {
				for _, r := range parseDXFRecords(t, ex.Bytes()) {
					if (r.sec == "ENTITIES" || r.sec == "BLOCKS") && r.typ != "BLOCK" && r.typ != "ENDBLK" {
						if v, ok := r.get(8); ok && v == exName {
							t.Errorf("%s: 排除图层 %s 后仍残留 %s 实体", name, exName, r.typ)
						}
					}
				}
			}
		}
		checked++
	}
	if checked == 0 {
		t.Skip("无样本通过解析")
	}
	t.Logf("语料子集不变量样本数=%d", checked)
}
