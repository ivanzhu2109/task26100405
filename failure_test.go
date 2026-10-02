package lessonpack

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func b64of(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func quizCard() string { return `{"type":"quiz","question":"q","answer":0}` }
func textCard() string { return `{"type":"text","body":"x"}` }

func audioCard(sha, b64 string) string {
	return fmt.Sprintf(`{"type":"audio","sha256":"%s","data":"%s"}`, sha, b64)
}

func lessonJSON(id string, cards ...string) string {
	return fmt.Sprintf(`{"id":"%s","title":"%s","cards":[%s]}`, id, id, strings.Join(cards, ","))
}

// TestPartialFailure 一个课件前几段音频正确而最后一段损坏：
// 前后有效课件成功，共享音频不误删，失败课件不留独占音频。
func TestPartialFailure(t *testing.T) {
	audioX := makeAudio(10, 5000)  // 被成功课件与失败课件共享
	audioA := makeAudio(11, 3000)  // 仅成功课件 A
	audioB1 := makeAudio(12, 3000) // 仅失败课件 B（独占，不得留下）
	audioC := makeAudio(13, 3000)  // 仅成功课件 C

	// 课件 B：前两段音频正确（含共享 X 与独占 B1），最后一段 Base64 非法
	badB64 := "QUJD!@#=" // 含非法字符
	lessonA := lessonJSON("A", quizCard(), audioCard(sha256Hex(audioX), b64of(audioX)), audioCard(sha256Hex(audioA), b64of(audioA)))
	lessonB := lessonJSON("B", textCard(), audioCard(sha256Hex(audioX), b64of(audioX)), audioCard(sha256Hex(audioB1), b64of(audioB1)), quizCard(), audioCard(sha256Hex(audioB1), badB64))
	lessonC := lessonJSON("C", quizCard(), audioCard(sha256Hex(audioX), b64of(audioX)), audioCard(sha256Hex(audioC), b64of(audioC)))

	// 课件 D：Base64 合法但摘要不符
	audioD := makeAudio(14, 1000)
	wrongSha := sha256Hex(makeAudio(15, 1000))
	lessonD := lessonJSON("D", quizCard(), textCard(), audioCard(wrongSha, b64of(audioD)))

	pkg := `{"lessons":[` + strings.Join([]string{lessonA, lessonB, lessonC, lessonD}, ",") + `]}`

	lib, err := Open(filepath.Join(t.TempDir(), "lib"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := lib.Import(&chunkReader{data: []byte(pkg), n: 7})
	if err != nil {
		t.Fatal(err)
	}

	// 成功记录：A、C 成功，B、D 失败
	if got := fmt.Sprint(res.Imported); got != "[A C]" {
		t.Fatalf("imported = %s, want [A C]", got)
	}
	if len(res.Failures) != 2 {
		t.Fatalf("failures = %+v, want 2", res.Failures)
	}
	fb, fd := res.Failures[0], res.Failures[1]
	if fb.LessonIndex != 1 || fb.LessonID != "B" || fb.CardIndex != 4 || fb.Reason != FailureInvalidBase64 {
		t.Fatalf("failure B = %+v, want lesson 1 card 4 invalid-base64", fb)
	}
	if fd.LessonIndex != 3 || fd.LessonID != "D" || fd.CardIndex != 2 || fd.Reason != FailureHashMismatch {
		t.Fatalf("failure D = %+v, want lesson 3 card 2 hash-mismatch", fd)
	}

	// 共享音频 X 与成功课件的音频保留；失败课件独占音频 B1、D 不留
	mustExist := [][]byte{audioX, audioA, audioC}
	for _, a := range mustExist {
		if _, err := os.Stat(lib.audioPath(sha256Hex(a))); err != nil {
			t.Fatalf("shared/succeeded audio missing: %v", err)
		}
	}
	mustAbsent := [][]byte{audioB1, audioD}
	for _, a := range mustAbsent {
		if _, err := os.Stat(lib.audioPath(sha256Hex(a))); !os.IsNotExist(err) {
			t.Fatalf("failed lesson's exclusive audio should not exist: %s", sha256Hex(a))
		}
	}
	// 失败课件元数据不得留在库中
	if _, err := os.Stat(lib.metaPath("B")); !os.IsNotExist(err) {
		t.Fatal("meta of failed lesson B should not exist")
	}
	if _, err := os.Stat(lib.metaPath("D")); !os.IsNotExist(err) {
		t.Fatal("meta of failed lesson D should not exist")
	}
	// 临时目录已清理
	left, err := os.ReadDir(lib.tmpDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("tmp dir not cleaned: %d entries", len(left))
	}
}

// TestStrictBase64 验证严格 Base64：内嵌空白、缺少补位、非零末尾位均被拒绝。
func TestStrictBase64(t *testing.T) {
	cases := []struct {
		name string
		data string // JSON 字符串内容（已含 JSON 转义写法）
	}{
		{"inner-whitespace", "QUJD\\nREVG"},      // JSON 转义 \n 解出换行
		{"inner-tab-unicode", "QUJD\\u0009REVG"}, // \u0009 解出制表符
		{"missing-padding", "QUI"},               // 长度不是 4 的倍数
		{"missing-padding2", "QQ"},               // 缺少 == 补位
		{"nonzero-trailing-bits", "QUJ="},        // 末尾未使用位非零
		{"nonzero-trailing-bits2", "QH=="},       // 单字节情形末尾位非零
		{"bad-char", "QUJ$"},
		{"padding-in-middle", "QU=DRUVG"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lesson := lessonJSON("L", quizCard(), textCard(), fmt.Sprintf(`{"type":"audio","sha256":"%s","data":"%s"}`, sha256Hex(nil), tc.data))
			pkg := `{"lessons":[` + lesson + `]}`
			lib, err := Open(filepath.Join(t.TempDir(), "lib"))
			if err != nil {
				t.Fatal(err)
			}
			res, err := lib.Import(strings.NewReader(pkg))
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Failures) != 1 || res.Failures[0].Reason != FailureInvalidBase64 {
				t.Fatalf("want one invalid-base64 failure, got %+v", res.Failures)
			}
			if len(res.Imported) != 0 {
				t.Fatalf("lesson should be rejected, imported=%v", res.Imported)
			}
		})
	}
}

// TestEmptyAudio 空音频（空 data）合法，摘要为空字节串的 SHA-256。
func TestEmptyAudio(t *testing.T) {
	empty := sha256Hex(nil)
	lesson := lessonJSON("L", quizCard(), textCard(), audioCard(empty, ""))
	pkg := `{"lessons":[` + lesson + `]}`
	lib, err := Open(filepath.Join(t.TempDir(), "lib"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := lib.Import(strings.NewReader(pkg))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Failures) != 0 || len(res.Imported) != 1 {
		t.Fatalf("empty audio should import: %+v", res)
	}
	b, err := os.ReadFile(lib.audioPath(empty))
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 0 {
		t.Fatalf("empty audio file has %d bytes", len(b))
	}
}
