package lessonpack

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// chunkReader 以任意小分段提供字节流，模拟不可回退输入的任意分段边界。
type chunkReader struct {
	data []byte
	n    int // 每段最大字节数
}

func (c *chunkReader) Read(p []byte) (int, error) {
	if len(c.data) == 0 {
		return 0, io.EOF
	}
	n := c.n
	if n > len(c.data) {
		n = len(c.data)
	}
	copy(p, c.data[:n])
	c.data = c.data[n:]
	return n, nil
}

// makeAudio 生成确定性的伪随机音频字节。
func makeAudio(seed byte, n int) []byte {
	b := make([]byte, n)
	x := seed
	for i := range b {
		x = x*166 + 101
		b[i] = x
	}
	return b
}

// escapeB64 把 base64 字符串中部分字符改写为 \uXXXX JSON 转义形式。
func escapeB64(s string) string {
	var buf bytes.Buffer
	for i := 0; i < len(s); i++ {
		c := s[i]
		if i%7 == 3 && ((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '+' || c == '/' || c == '=') {
			fmt.Fprintf(&buf, `\u%04x`, c)
		} else {
			buf.WriteByte(c)
		}
	}
	return buf.String()
}

// audioField 以乱序、转义的方式输出一张音频卡片的 JSON。
func audioCardJSON(sha, b64 string, escaped bool) string {
	data := b64
	if escaped {
		data = escapeB64(b64)
	}
	// 字段乱序：data 在 sha256 之前，type 在最后
	return fmt.Sprintf(`{"data":"%s","sha256":"%s","type":"audio","note":{"sampleRate":48000,"tags":["a","b"]}}`, data, sha)
}

func textCardJSON() string {
	// 含 \u 转义、数字与嵌套结构的 text 卡片
	return `{"type":"text","body":"\u8bfe\u4ef6\n\"引号\"","meta":{"order":1e2,"ratio":0.5,"flags":[true,null]}}`
}

func quizCardJSON() string {
	return `{"type":"quiz","question":"1+1=?","choices":["1","2"],"answer":1}`
}

// buildPackage 构造一个字段乱序、含转义的自包含包，返回包字节与课件 id/音频。
func buildPackage() (pkg []byte, ids []string, audios map[string][]byte) {
	audios = map[string][]byte{}
	a1 := makeAudio(1, 1000)
	a2 := makeAudio(2, 2049)
	a3 := a1 // 与 a1 共享内容，验证去重
	audios["a1"] = a1
	audios["a2"] = a2
	sha := func(b []byte) string {
		h := sha256.Sum256(b)
		return hex.EncodeToString(h[:])
	}
	b64 := base64.StdEncoding.EncodeToString

	var buf bytes.Buffer
	buf.WriteString(`{"lessons":[`)
	// 课件1：cards 在 id 之前；音频卡片字段乱序且 data 含 \u 转义
	fmt.Fprintf(&buf, `{"cards":[%s,%s,%s],"id":"les\u0073on-1","title":"第一课"}`, textCardJSON(), audioCardJSON(sha(a1), b64(a1), true), quizCardJSON())
	buf.WriteByte(',')
	// 课件2：title 在最后；第二张音频卡片与课件1共享音频内容
	fmt.Fprintf(&buf, `{"id":"lesson-2","cards":[%s,%s,%s,%s],"title":"第二课"}`, quizCardJSON(), audioCardJSON(sha(a2), b64(a2), false), audioCardJSON(sha(a3), b64(a3), false), textCardJSON())
	buf.WriteString(`]}`)
	ids = []string{"lesson-1", "lesson-2"}
	return buf.Bytes(), ids, audios
}

func TestRoundTrip(t *testing.T) {
	pkg, ids, audios := buildPackage()

	lib1, err := Open(filepath.Join(t.TempDir(), "lib1"))
	if err != nil {
		t.Fatal(err)
	}
	// 以 3 字节分段输入，与 \uXXXX 转义（6 字节）交织
	res, err := lib1.Import(&chunkReader{data: pkg, n: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Failures) != 0 {
		t.Fatalf("unexpected failures: %+v", res.Failures)
	}
	if !reflect.DeepEqual(res.Imported, ids) {
		t.Fatalf("imported ids = %v, want %v", res.Imported, ids)
	}

	// 库内音频去重：a1 与 a3 内容相同，只保留一份
	entries, err := os.ReadDir(lib1.audioDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("audio files = %d, want 2 (dedup)", len(entries))
	}
	// 音频摘要与独立计算结果一致
	for name, want := range audios {
		_ = name
		h := sha256.Sum256(want)
		path := lib1.audioPath(hex.EncodeToString(h[:]))
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("audio %x: %v", h, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("audio bytes mismatch for sha %x", h)
		}
	}

	// 导出：顺序与给定 id 序列一致（逆序验证）
	rev := []string{ids[1], ids[0]}
	var exported bytes.Buffer
	if err := lib1.Export(&exported, rev); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Lessons []map[string]any `json:"lessons"`
	}
	dec := json.NewDecoder(bytes.NewReader(exported.Bytes()))
	dec.UseNumber()
	if err := dec.Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Lessons) != 2 || decoded.Lessons[0]["id"] != "lesson-2" || decoded.Lessons[1]["id"] != "lesson-1" {
		t.Fatalf("export order wrong: %v, %v", decoded.Lessons[0]["id"], decoded.Lessons[1]["id"])
	}

	// 重新导入空库：卡片顺序、其他字段 JSON 值、音频字节均一致
	lib2, err := Open(filepath.Join(t.TempDir(), "lib2"))
	if err != nil {
		t.Fatal(err)
	}
	res2, err := lib2.Import(&chunkReader{data: exported.Bytes(), n: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.Failures) != 0 {
		t.Fatalf("reimport failures: %+v", res2.Failures)
	}
	var reexported bytes.Buffer
	if err := lib2.Export(&reexported, rev); err != nil {
		t.Fatal(err)
	}
	var again struct {
		Lessons []map[string]any `json:"lessons"`
	}
	dec2 := json.NewDecoder(bytes.NewReader(reexported.Bytes()))
	dec2.UseNumber()
	if err := dec2.Decode(&again); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded.Lessons, again.Lessons) {
		t.Fatalf("roundtrip mismatch:\n%v\nvs\n%v", decoded.Lessons, again.Lessons)
	}

	// 音频字节与原课件一致
	for _, want := range audios {
		h := sha256.Sum256(want)
		got, err := os.ReadFile(lib2.audioPath(hex.EncodeToString(h[:])))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatal("audio bytes changed after roundtrip")
		}
	}

	// 其他字段的 JSON 值一致（抽查 text 卡片的嵌套值）
	cards := decoded.Lessons[1]["cards"].([]any)
	textCard := cards[0].(map[string]any)
	meta := textCard["meta"].(map[string]any)
	if meta["order"] != json.Number("1e2") {
		t.Fatalf("number literal not preserved: %v", meta["order"])
	}
	if textCard["body"] != "课件\n\"引号\"" {
		t.Fatalf("unicode escape not decoded: %q", textCard["body"])
	}
}
