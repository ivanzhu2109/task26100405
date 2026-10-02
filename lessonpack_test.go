package lessonpack

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// chunkReader 以随机小分段包装上层 reader，模拟不可回退流的任意分段边界。
type chunkReader struct {
	r   io.Reader
	rng *rand.Rand
	max int
}

func (c *chunkReader) Read(p []byte) (int, error) {
	n := 1 + c.rng.Intn(c.max)
	if n > len(p) {
		n = len(p)
	}
	return c.r.Read(p[:n])
}

// chunkWriter 以随机小分段写出。
type chunkWriter struct {
	w   io.Writer
	rng *rand.Rand
	max int
}

func (c *chunkWriter) Write(p []byte) (int, error) {
	total := 0
	for len(p) > 0 {
		n := 1 + c.rng.Intn(c.max)
		if n > len(p) {
			n = len(p)
		}
		if _, err := c.w.Write(p[:n]); err != nil {
			return total, err
		}
		total += n
		p = p[n:]
	}
	return total, nil
}

// jstring 以随机转义写法输出 JSON 字符串。
func jstring(sb *strings.Builder, s string, rng *rand.Rand) {
	sb.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			sb.WriteString(`\"`)
		case r == '\\':
			sb.WriteString(`\\`)
		case r == '/':
			if rng.Intn(2) == 0 {
				sb.WriteString(`\/`)
			} else {
				sb.WriteByte('/')
			}
		case r < 0x20:
			fmt.Fprintf(sb, `\u%04x`, r)
		case rng.Intn(4) == 0 && r < 0x80:
			fmt.Fprintf(sb, `\u%04x`, r)
		default:
			sb.WriteRune(r)
		}
	}
	sb.WriteByte('"')
}

// ws 输出随机空白。
func ws(sb *strings.Builder, rng *rand.Rand) {
	for i := rng.Intn(3); i > 0; i-- {
		sb.WriteByte(" \t\n\r"[rng.Intn(4)])
	}
}

// tCard 描述一张测试卡片。
type tCard struct {
	typ     string
	sha     string      // audio：声明摘要
	data    []byte      // audio：原始字节
	dataRaw string      // audio：非空时作为 data 字段的原始 JSON（用于注入坏数据）
	fields  [][2]string // 其他字段：key + 原始 JSON
}

// tLesson 描述一条测试课件。
type tLesson struct {
	id     string
	fields [][2]string // 顶层其他字段（含 title）
	cards  []tCard
}

// buildPackage 以乱序字段、随机空白与随机转义生成包 JSON。
func buildPackage(lessons []tLesson, rng *rand.Rand) string {
	var sb strings.Builder
	ws(&sb, rng)
	sb.WriteByte('{')
	ws(&sb, rng)
	jstring(&sb, "lessons", rng)
	ws(&sb, rng)
	sb.WriteByte(':')
	ws(&sb, rng)
	sb.WriteByte('[')
	for i, l := range lessons {
		if i > 0 {
			sb.WriteByte(',')
		}
		ws(&sb, rng)
		buildLesson(&sb, l, rng)
	}
	sb.WriteByte(']')
	ws(&sb, rng)
	sb.WriteByte('}')
	ws(&sb, rng)
	return sb.String()
}

// buildLesson 以乱序字段写出一条课件。
func buildLesson(sb *strings.Builder, l tLesson, rng *rand.Rand) {
	type fld struct{ k, v string }
	var fields []fld
	fields = append(fields, fld{"id", quote(l.id)})
	for _, f := range l.fields {
		fields = append(fields, fld{f[0], f[1]})
	}
	var cb strings.Builder
	buildCards(&cb, l.cards, rng)
	fields = append(fields, fld{"cards", cb.String()})
	rng.Shuffle(len(fields), func(i, j int) { fields[i], fields[j] = fields[j], fields[i] })
	sb.WriteByte('{')
	for i, f := range fields {
		if i > 0 {
			sb.WriteByte(',')
		}
		ws(sb, rng)
		jstring(sb, f.k, rng)
		ws(sb, rng)
		sb.WriteByte(':')
		ws(sb, rng)
		sb.WriteString(f.v)
	}
	ws(sb, rng)
	sb.WriteByte('}')
}

func buildCards(sb *strings.Builder, cards []tCard, rng *rand.Rand) {
	sb.WriteByte('[')
	for i, c := range cards {
		if i > 0 {
			sb.WriteByte(',')
		}
		ws(sb, rng)
		buildCard(sb, c, rng)
	}
	sb.WriteByte(']')
}

// buildCard 以乱序字段写出一张卡片；Base64 字符也随机转义。
func buildCard(sb *strings.Builder, c tCard, rng *rand.Rand) {
	type fld struct{ k, v string }
	var fields []fld
	fields = append(fields, fld{"type", quote(c.typ)})
	if c.typ == "audio" {
		dataVal := c.dataRaw
		if dataVal == "" {
			b64 := base64.StdEncoding.EncodeToString(c.data)
			var db strings.Builder
			jstring(&db, b64, rng)
			dataVal = db.String()
		}
		fields = append(fields, fld{"sha256", quote(c.sha)}, fld{"data", dataVal})
	}
	for _, f := range c.fields {
		fields = append(fields, fld{f[0], f[1]})
	}
	rng.Shuffle(len(fields), func(i, j int) { fields[i], fields[j] = fields[j], fields[i] })
	sb.WriteByte('{')
	for i, f := range fields {
		if i > 0 {
			sb.WriteByte(',')
		}
		ws(sb, rng)
		jstring(sb, f.k, rng)
		ws(sb, rng)
		sb.WriteByte(':')
		ws(sb, rng)
		sb.WriteString(f.v)
	}
	ws(sb, rng)
	sb.WriteByte('}')
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// makeAudio 生成确定性的伪随机音频字节。
func makeAudio(rng *rand.Rand, n int) []byte {
	b := make([]byte, n)
	rng.Read(b)
	return b
}

// sampleLessons 构造一组含共享音频、乱序字段与多样 JSON 值的课件。
func sampleLessons(rng *rand.Rand) []tLesson {
	shared := makeAudio(rng, 3000)
	exclusive := makeAudio(rng, 1500)
	return []tLesson{
		{
			id: "lesson-1",
			fields: [][2]string{
				{"title", `"初级 培训"`},
				{"level", `1`},
			},
			cards: []tCard{
				{typ: "text", fields: [][2]string{
					{"body", `"第一课的嵌套值"`},
					{"meta", `{"tags":["a","b"],"weight":1.5,"ok":true,"nil":null}`},
				}},
				{typ: "quiz", fields: [][2]string{
					{"question", `"1+1=?"`},
					{"options", `["1","2","3"]`},
					{"answer", `2`},
				}},
				{typ: "audio", sha: sha256Hex(shared), data: shared},
			},
		},
		{
			id: "lesson-2",
			fields: [][2]string{
				{"title", `"第二课"`},
			},
			cards: []tCard{
				{typ: "audio", sha: sha256Hex(shared), data: shared}, // 与 lesson-1 共享
				{typ: "quiz", fields: [][2]string{{"q", `"x"`}}},
				{typ: "text", fields: [][2]string{{"body", `"y"`}}},
				{typ: "audio", sha: sha256Hex(exclusive), data: exclusive},
			},
		},
	}
}

// importChunked 以随机小分段导入。
func importChunked(t *testing.T, s *Store, pkg string, seed int64) []LessonResult {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	results, err := s.Import(&chunkReader{r: strings.NewReader(pkg), rng: rng, max: 13})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	return results
}

// exportChunked 以随机小分段导出。
func exportChunked(t *testing.T, s *Store, ids []string, seed int64) string {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	var buf bytes.Buffer
	if err := s.Export(&chunkWriter{w: &buf, rng: rng, max: 11}, ids); err != nil {
		t.Fatalf("Export: %v", err)
	}
	return buf.String()
}

// TestRoundTrip 覆盖字段乱序、JSON 转义与输入分段交织的完整往返。
func TestRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	lessons := sampleLessons(rng)
	pkg := buildPackage(lessons, rng)

	dirA := t.TempDir()
	storeA, err := Open(dirA)
	if err != nil {
		t.Fatal(err)
	}
	results := importChunked(t, storeA, pkg, 7)
	if len(results) != len(lessons) {
		t.Fatalf("got %d results, want %d", len(results), len(lessons))
	}
	for i, r := range results {
		if !r.OK {
			t.Fatalf("lesson %d (%s) failed: reason=%d card=%d", i, r.ID, r.Reason, r.CardIndex)
		}
		if r.ID != lessons[i].id {
			t.Fatalf("result %d ID = %q, want %q", i, r.ID, lessons[i].id)
		}
	}

	// 独立计算音频摘要，与库内按摘要寻址的文件逐一核对。
	distinct := map[string][]byte{}
	for _, l := range lessons {
		for _, c := range l.cards {
			if c.typ == "audio" {
				distinct[sha256Hex(c.data)] = c.data
			}
		}
	}
	entries, err := os.ReadDir(filepath.Join(dirA, "audio"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(distinct) {
		t.Fatalf("audio dir has %d files, want %d (dedup)", len(entries), len(distinct))
	}
	for _, e := range entries {
		content, err := os.ReadFile(filepath.Join(dirA, "audio", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if got := sha256Hex(content); got != e.Name() {
			t.Fatalf("audio file %s content sha256 = %s", e.Name(), got)
		}
		if want, ok := distinct[e.Name()]; !ok || !bytes.Equal(content, want) {
			t.Fatalf("audio file %s content mismatch", e.Name())
		}
	}

	// 导出（乱序 ID 序列），再导入空库，再导出，两次导出应逐字节一致。
	ids := []string{"lesson-2", "lesson-1"}
	outA := exportChunked(t, storeA, ids, 9)
	dirB := t.TempDir()
	storeB, err := Open(dirB)
	if err != nil {
		t.Fatal(err)
	}
	results = importChunked(t, storeB, outA, 11)
	for i, r := range results {
		if !r.OK {
			t.Fatalf("reimport lesson %d failed: %+v", i, r)
		}
	}
	if results[0].ID != "lesson-2" || results[1].ID != "lesson-1" {
		t.Fatalf("export order not preserved: %+v", results)
	}
	outB := exportChunked(t, storeB, ids, 13)
	if outA != outB {
		t.Fatal("re-export differs from first export")
	}

	// 语义核对：卡片顺序、其他字段 JSON 值与音频字节均与原始课件一致。
	var got struct {
		Lessons []map[string]any `json:"lessons"`
	}
	if err := json.Unmarshal([]byte(outA), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Lessons) != 2 {
		t.Fatalf("exported %d lessons, want 2", len(got.Lessons))
	}
	byID := map[string]tLesson{}
	for _, l := range lessons {
		byID[l.id] = l
	}
	for i, id := range ids {
		want := expectedLesson(t, byID[id])
		if !reflect.DeepEqual(got.Lessons[i], want) {
			t.Fatalf("lesson %q mismatch:\n got: %v\nwant: %v", id, got.Lessons[i], want)
		}
	}
}

// expectedLesson 构造课件的期望 JSON 值（encoding/json 归一化形式）。
func expectedLesson(t *testing.T, l tLesson) map[string]any {
	t.Helper()
	obj := map[string]any{"id": l.id}
	for _, f := range l.fields {
		obj[f[0]] = jsonValue(t, f[1])
	}
	var cards []any
	for _, c := range l.cards {
		card := map[string]any{"type": c.typ}
		if c.typ == "audio" {
			card["sha256"] = c.sha
			card["data"] = base64.StdEncoding.EncodeToString(c.data)
		}
		for _, f := range c.fields {
			card[f[0]] = jsonValue(t, f[1])
		}
		cards = append(cards, card)
	}
	obj["cards"] = cards
	return obj
}

func jsonValue(t *testing.T, raw string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatal(err)
	}
	return v
}
