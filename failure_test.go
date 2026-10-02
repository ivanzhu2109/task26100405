package lessonpack

import (
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFailureIsolation 覆盖：坏课件前几段音频正确、最后一段损坏时，
// 前后有效课件成功，共享音频保留，失败课件的独占音频与元数据不留不留库。
func TestFailureIsolation(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	shared := makeAudio(rng, 2000)   // L1、L2、L4 共享
	badOnly1 := makeAudio(rng, 1000) // L2 独占（损坏课件的前段正确音频）
	badOnly2 := makeAudio(rng, 1000) // L3 独占
	fresh := makeAudio(rng, 1200)    // L4 独占（成功）
	wrongSHA := strings.Repeat("0", 64)

	lessons := []tLesson{
		{
			id:     "L1-ok-first",
			fields: [][2]string{{"title", `"前"`}},
			cards: []tCard{
				{typ: "text", fields: [][2]string{{"body", `"t"`}}},
				{typ: "quiz", fields: [][2]string{{"q", `"?"`}}},
				{typ: "audio", sha: sha256Hex(shared), data: shared},
			},
		},
		{
			id:     "L2-bad-base64",
			fields: [][2]string{{"title", `"坏base64"`}},
			cards: []tCard{
				{typ: "audio", sha: sha256Hex(shared), data: shared},     // 共享，正确
				{typ: "audio", sha: sha256Hex(badOnly1), data: badOnly1}, // 独占，正确
				{typ: "quiz", fields: [][2]string{{"q", `"?"`}}},
				// 最后一段损坏：内嵌空白，非法 Base64
				{typ: "audio", sha: sha256Hex(shared), dataRaw: `"QUJD R0U="`},
			},
		},
		{
			id:     "L3-bad-sha",
			fields: [][2]string{{"title", `"坏摘要"`}},
			cards: []tCard{
				{typ: "quiz", fields: [][2]string{{"q", `"?"`}}},
				{typ: "audio", sha: sha256Hex(badOnly2), data: badOnly2}, // 独占，正确
				// 最后一段：Base64 合法但摘要不符
				{typ: "audio", sha: wrongSHA, data: shared},
			},
		},
		{
			id:     "L4-ok-last",
			fields: [][2]string{{"title", `"后"`}},
			cards: []tCard{
				{typ: "audio", sha: sha256Hex(shared), data: shared},
				{typ: "quiz", fields: [][2]string{{"q", `"?"`}}},
				{typ: "audio", sha: sha256Hex(fresh), data: fresh},
			},
		},
	}
	pkg := buildPackage(lessons, rng)

	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	results := importChunked(t, store, pkg, 3)
	if len(results) != 4 {
		t.Fatalf("got %d results, want 4", len(results))
	}

	// 逐条核对结果记录。
	want := []LessonResult{
		{ID: "L1-ok-first", OK: true},
		{ID: "L2-bad-base64", OK: false, Reason: ReasonInvalidBase64, CardIndex: 3},
		{ID: "L3-bad-sha", OK: false, Reason: ReasonSHA256Mismatch, CardIndex: 2},
		{ID: "L4-ok-last", OK: true},
	}
	for i, w := range want {
		if results[i] != w {
			t.Fatalf("result %d = %+v, want %+v", i, results[i], w)
		}
	}

	// 正式库音频：共享与成功课件独占的保留，失败课件独占的清除。
	audioDir := filepath.Join(dir, "audio")
	mustExist := []string{sha256Hex(shared), sha256Hex(fresh)}
	mustAbsent := []string{sha256Hex(badOnly1), sha256Hex(badOnly2)}
	for _, name := range mustExist {
		if _, err := os.Stat(filepath.Join(audioDir, name)); err != nil {
			t.Fatalf("shared/valid audio %s missing: %v", name, err)
		}
	}
	for _, name := range mustAbsent {
		if _, err := os.Stat(filepath.Join(audioDir, name)); !os.IsNotExist(err) {
			t.Fatalf("exclusive audio of failed lesson %s should not exist", name)
		}
	}
	entries, err := os.ReadDir(audioDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(mustExist) {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("audio dir = %v, want exactly %d files", names, len(mustExist))
	}

	// 失败课件不得留下元数据；成功课件的必须在。
	for _, id := range []string{"L1-ok-first", "L4-ok-last"} {
		if _, err := os.Stat(store.lessonPath(id)); err != nil {
			t.Fatalf("lesson %s metadata missing: %v", id, err)
		}
	}
	for _, id := range []string{"L2-bad-base64", "L3-bad-sha"} {
		if _, err := os.Stat(store.lessonPath(id)); !os.IsNotExist(err) {
			t.Fatalf("failed lesson %s metadata should not exist", id)
		}
	}

	// 临时目录须在返回前清理。
	if _, err := os.Stat(store.tmpDir()); !os.IsNotExist(err) {
		t.Fatalf("tmp dir should be cleaned, stat err = %v", err)
	}

	// 成功课件可正常导出，共享音频内容完整。
	out := exportChunked(t, store, []string{"L1-ok-first", "L4-ok-last"}, 5)
	if !strings.Contains(out, `"L1-ok-first"`) || !strings.Contains(out, `"L4-ok-last"`) {
		t.Fatal("export missing successful lessons")
	}
}
