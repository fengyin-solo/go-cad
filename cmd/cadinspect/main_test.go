// main_test.go 验证 cadinspect CLI 合同：stdout 只输出一份完整 JSON；
// 健康文档退出码 0；存在诊断退出独立状态码 2 且 JSON 仍完整写出；
// 损坏输入/空文档/参数错误退出码 1 且 stdout 无半截输出。覆盖健康
// DXF、非有限坐标、缺失图层名、空块、重复句柄与真实 DWG。
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// writeInspectInput 写输入文件并返回路径。
func writeInspectInput(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写输入文件: %v", err)
	}
	return path
}

// runInspect 执行 run 并返回 stdout 与退出码（0=成功 2=诊断 1=错误）。
func runInspect(t *testing.T, args ...string) (string, int) {
	t.Helper()
	var buf bytes.Buffer
	err := run(args, &buf)
	if err == nil {
		return buf.String(), 0
	}
	var ee *exitError
	if errors.As(err, &ee) {
		return buf.String(), ee.code
	}
	return buf.String(), 1
}

// healthyInspectDXF 最小健康 ASCII DXF：一个具名图层 + 一条 LINE。
const healthyInspectDXF = `0
SECTION
2
HEADER
9
$ACADVER
1
AC1032
0
ENDSEC
0
SECTION
2
TABLES
0
TABLE
2
LAYER
5
2
70
1
0
LAYER
5
10
2
GOOD
70
0
62
7
0
ENDTAB
0
ENDSEC
0
SECTION
2
ENTITIES
0
LINE
5
1A
8
GOOD
10
0.0
20
0.0
30
0.0
11
10.0
21
10.0
31
0.0
0
ENDSEC
0
EOF
`

// badInspectDXF 问题 DXF：未定义图层 GHOST、NaN 坐标、两条同句柄
// 1A 的 LINE（重复句柄）、一个无内容的用户块 EMPTYBLK。
const badInspectDXF = `0
SECTION
2
HEADER
9
$ACADVER
1
AC1032
0
ENDSEC
0
SECTION
2
TABLES
0
TABLE
2
LAYER
5
2
70
1
0
LAYER
5
10
2
GOOD
70
0
62
7
0
ENDTAB
0
ENDSEC
0
SECTION
2
BLOCKS
0
BLOCK
5
2F
8
0
2
EMPTYBLK
10
0.0
20
0.0
30
0.0
0
ENDBLK
5
30
8
0
0
ENDSEC
0
SECTION
2
ENTITIES
0
LINE
5
1A
8
GHOST
10
nan
20
0.0
30
0.0
11
10.0
21
10.0
31
0.0
0
LINE
5
1A
8
GOOD
10
1.0
20
1.0
30
0.0
11
2.0
21
2.0
31
0.0
0
ENDSEC
0
EOF
`

// emptyInspectDXF 只有 HEADER 的空文档（无任何实体/块）。
const emptyInspectDXF = `0
SECTION
2
HEADER
9
$ACADVER
1
AC1032
0
ENDSEC
0
EOF
`

// parseReport 解析 stdout JSON 为通用 map（断言合法 JSON）。
func parseReport(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("stdout 不是合法 JSON: %v\n%s", err, raw)
	}
	return m
}

// TestCLIHealthyDXF 健康 DXF：退出码 0，status=ok，统计正确。
func TestCLIHealthyDXF(t *testing.T) {
	dir := t.TempDir()
	input := writeInspectInput(t, dir, "ok.dxf", healthyInspectDXF)
	out, code := runInspect(t, input)
	if code != 0 {
		t.Fatalf("健康文档退出码 = %d, 期望 0", code)
	}
	m := parseReport(t, out)
	if m["status"] != "ok" {
		t.Fatalf("status = %v, 期望 ok", m["status"])
	}
	if m["source"] != "ok.dxf" || m["dwg_version"] == "" {
		t.Fatalf("元数据异常: source=%v dwg_version=%v", m["source"], m["dwg_version"])
	}
	summary := m["summary"].(map[string]any)
	if summary["model_space"].(float64) != 1 || summary["diagnostics"].(float64) != 0 {
		t.Fatalf("汇总异常: %v", summary)
	}
	diags := m["diagnostics"].([]any)
	if len(diags) != 0 {
		t.Fatalf("健康文档不应有诊断: %v", diags)
	}
}

// TestCLIDiagnosticsExitCode 问题 DXF：退出码 2，完整 JSON 仍写出，
// 四类诊断齐全且稳定排序。
func TestCLIDiagnosticsExitCode(t *testing.T) {
	dir := t.TempDir()
	input := writeInspectInput(t, dir, "bad.dxf", badInspectDXF)
	out, code := runInspect(t, input)
	if code != exitCodeDiagnostics {
		t.Fatalf("问题文档退出码 = %d, 期望 %d", code, exitCodeDiagnostics)
	}
	if out == "" {
		t.Fatalf("诊断场景仍须把完整 JSON 写到 stdout")
	}
	m := parseReport(t, out)
	if m["status"] != "diagnostics" {
		t.Fatalf("status = %v, 期望 diagnostics", m["status"])
	}
	diags := m["diagnostics"].([]any)
	codes := map[string]bool{}
	for _, d := range diags {
		codes[d.(map[string]any)["code"].(string)] = true
	}
	for _, want := range []string{"non_finite_geometry", "missing_layer_name", "empty_block", "duplicate_handle"} {
		if !codes[want] {
			t.Fatalf("缺少诊断码 %s，实际 %v", want, codes)
		}
	}
	// 非有限几何诊断须带字段路径与句柄
	for _, d := range diags {
		dm := d.(map[string]any)
		if dm["code"] == "non_finite_geometry" {
			fields := dm["fields"].([]any)
			if len(fields) == 0 || fields[0] != "Start.X=NaN" {
				t.Fatalf("非有限字段异常: %v", fields)
			}
		}
		if dm["code"] == "duplicate_handle" && dm["count"].(float64) != 2 {
			t.Fatalf("重复句柄计数异常: %v", dm)
		}
		if dm["code"] == "empty_block" && dm["block"] != "EMPTYBLK" {
			t.Fatalf("空块名异常: %v", dm["block"])
		}
	}
	// 跳过区为合同固定结构
	skipped := m["skipped"].(map[string]any)
	if skipped["count"].(float64) != 0 {
		t.Fatalf("跳过数异常: %v", skipped)
	}
}

// TestCLIStableOutput 同一问题文档两次输出字节一致。
func TestCLIStableOutput(t *testing.T) {
	dir := t.TempDir()
	input := writeInspectInput(t, dir, "bad.dxf", badInspectDXF)
	out1, code1 := runInspect(t, input)
	out2, code2 := runInspect(t, input)
	if code1 != exitCodeDiagnostics || code2 != exitCodeDiagnostics {
		t.Fatalf("退出码异常: %d/%d", code1, code2)
	}
	if out1 != out2 {
		t.Fatalf("两次输出不稳定一致")
	}
}

// TestCLICorruptInput 损坏输入：退出码 1，stdout 无半截 JSON。
func TestCLICorruptInput(t *testing.T) {
	dir := t.TempDir()
	input := writeInspectInput(t, dir, "garbage.dwg", "this is definitely not a DWG file")
	out, code := runInspect(t, input)
	if code != 1 {
		t.Fatalf("损坏输入退出码 = %d, 期望 1", code)
	}
	if out != "" {
		t.Fatalf("解析失败不应向 stdout 写半截输出, got: %s", out)
	}
}

// TestCLIEmptyDocument 空文档：退出码 1，错误清晰（含“空”）。
func TestCLIEmptyDocument(t *testing.T) {
	dir := t.TempDir()
	input := writeInspectInput(t, dir, "empty.dxf", emptyInspectDXF)
	var buf bytes.Buffer
	err := run([]string{input}, &buf)
	if err == nil {
		t.Fatalf("空文档应返回错误")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("空")) {
		t.Fatalf("空文档错误不清晰: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("空文档不应产出报告")
	}
}

// TestCLIUsage 参数错误退出码 1；帮助成功。
func TestCLIUsage(t *testing.T) {
	if _, code := runInspect(t); code != 1 {
		t.Fatalf("无参数退出码 = %d, 期望 1", code)
	}
	dir := t.TempDir()
	extra := writeInspectInput(t, dir, "a.dwg", "x")
	if _, code := runInspect(t, extra, filepath.Join(dir, "b.dwg")); code != 1 {
		t.Fatalf("多余参数退出码 = %d, 期望 1", code)
	}
	var buf bytes.Buffer
	if err := run([]string{"-h"}, &buf); err != nil {
		t.Fatalf("-h 应成功: %v", err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("cadinspect")) {
		t.Fatalf("帮助文本异常: %s", buf.String())
	}
}

// TestCLIRealDWG 真实 DWG：合法 JSON，status=ok，三空间统计可消费。
func TestCLIRealDWG(t *testing.T) {
	input := filepath.Join("..", "..", "testdata", "line_2013.dwg")
	out, code := runInspect(t, input)
	if code != 0 {
		t.Fatalf("真实 DWG 退出码 = %d, 期望 0, out=%s", code, out)
	}
	m := parseReport(t, out)
	if m["status"] != "ok" {
		t.Fatalf("真实 DWG status = %v", m["status"])
	}
	spaces := m["spaces"].(map[string]any)
	ms := spaces["model_space"].(map[string]any)
	if ms["total"].(float64) < 1 {
		t.Fatalf("模型空间统计异常: %v", ms)
	}
	ps := spaces["paper_space"].(map[string]any)
	if ps["total"].(float64) < 1 {
		t.Fatalf("图纸空间统计异常: %v", ps)
	}
	if m["summary"].(map[string]any)["entities"].(float64) < 1 {
		t.Fatalf("实体汇总异常")
	}
}

// TestCLIMissingFile 不存在的文件：退出码 1。
func TestCLIMissingFile(t *testing.T) {
	if _, code := runInspect(t, filepath.Join(t.TempDir(), "nope.dwg")); code != 1 {
		t.Fatalf("缺失文件退出码应为 1")
	}
}
