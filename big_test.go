package lessonpack

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"runtime"
	"testing"
	"time"
)

// xorshiftReader 生成确定性伪随机字节流，不在内存中持有数据。
type xorshiftReader struct {
	state uint64
	left  int64
	oct   [8]byte
	pos   int
}

func newXorshiftReader(seed uint64, n int64) *xorshiftReader {
	return &xorshiftReader{state: seed, left: n}
}

func (x *xorshiftReader) Read(p []byte) (int, error) {
	if x.left == 0 {
		return 0, io.EOF
	}
	n := int64(len(p))
	if n > x.left {
		n = x.left
	}
	for i := int64(0); i < n; i++ {
		if x.pos == 0 {
			x.state ^= x.state << 13
			x.state ^= x.state >> 7
			x.state ^= x.state << 17
			binary.LittleEndian.PutUint64(x.oct[:], x.state)
		}
		p[i] = x.oct[x.pos]
		x.pos = (x.pos + 1) % 8
	}
	x.left -= n
	return int(n), nil
}

// audioSpec 描述一段待生成的音频。
type audioSpec struct {
	seed uint64
	size int64
	sha  string // 独立计算的摘要
}

// lessonSpec 描述一条待生成的课件。
type lessonSpec struct {
	id     string
	audios []audioSpec
}

// hashOf 独立计算音频字节流的 sha256。
func hashOf(seed uint64, size int64) string {
	h := sha256.New()
	io.Copy(h, newXorshiftReader(seed, size))
	return hex.EncodeToString(h.Sum(nil))
}

// packageReader 流式生成大包 JSON，自身只持有很小状态。
type packageReader struct {
	lessons []lessonSpec
	phase   int // 0=包起始 1=课件头 2=text/quiz卡 3=音频卡头 4=音频卡尾 5=包收尾 6=结束
	li      int
	ci      int
	cur     io.Reader // 当前音频字节流
	buf     []byte
}

func newPackageReader(lessons []lessonSpec) *packageReader {
	return &packageReader{lessons: lessons}
}

func (p *packageReader) Read(b []byte) (int, error) {
	for len(p.buf) == 0 {
		if !p.advance() {
			return 0, io.EOF
		}
	}
	n := copy(b, p.buf)
	p.buf = p.buf[n:]
	return n, nil
}

// advance 推进生成状态机，填充下一段缓冲。
func (p *packageReader) advance() bool {
	if p.cur != nil {
		// 正在输出音频 Base64：编码下一块。
		raw := make([]byte, 48<<10)
		n, err := p.cur.Read(raw)
		if n > 0 {
			enc := make([]byte, base64.StdEncoding.EncodedLen(n))
			base64.StdEncoding.Encode(enc, raw[:n])
			p.buf = enc
		}
		if err == io.EOF {
			p.cur = nil
		}
		return n > 0 || p.advance()
	}
	switch p.phase {
	case 0:
		p.phase = 1
		p.buf = []byte(`{"lessons":[`)
	case 1: // 课件头
		if p.li >= len(p.lessons) {
			p.phase = 5
			return p.advance()
		}
		l := &p.lessons[p.li]
		prefix := ""
		if p.li > 0 {
			prefix = ","
		}
		p.buf = []byte(fmt.Sprintf(`%s{"id":%q,"title":"big","cards":[`, prefix, l.id))
		p.phase = 2
	case 2: // text 与 quiz 卡
		p.buf = []byte(`{"type":"text","body":"x"},{"type":"quiz","q":"?"}`)
		p.phase = 3
	case 3: // 音频卡头（含 data 开引号）
		l := &p.lessons[p.li]
		if p.ci >= len(l.audios) {
			p.buf = []byte(`]}`)
			p.phase = 1
			p.li++
			p.ci = 0
			return true
		}
		a := l.audios[p.ci]
		p.buf = []byte(fmt.Sprintf(`,{"type":"audio","sha256":%q,"data":"`, a.sha))
		p.cur = newXorshiftReader(a.seed, a.size)
		p.phase = 4
	case 4: // 音频卡尾
		p.buf = []byte(`"}`)
		p.phase = 3
		p.ci++
	case 5: // 包收尾
		p.buf = []byte(`]}`)
		p.phase = 6
	default:
		return false
	}
	return true
}

// measurePeakHeap 在 fn 运行期间采样 HeapAlloc，返回相对起点的峰值增量。
func measurePeakHeap(fn func()) uint64 {
	runtime.GC()
	runtime.GC()
	var start runtime.MemStats
	runtime.ReadMemStats(&start)
	var peak uint64
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			default:
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				if m.HeapAlloc > peak {
					peak = m.HeapAlloc
				}
				time.Sleep(time.Millisecond)
			}
		}
	}()
	fn()
	close(done)
	if peak < start.HeapAlloc {
		return 0
	}
	return peak - start.HeapAlloc
}

const heapLimit = 32 << 20 // 32 MiB

// TestBigPackMemory 大包导入导出：单课件 70 MiB 音频，整包默认约 250 MiB；
// 设 LESSONPACK_BIG=1 时约 2 GiB。峰值 Go 堆增量不得超过 32 MiB。
func TestBigPackMemory(t *testing.T) {
	audioSize := int64(10 << 20) // 每段音频 10 MiB（上限）
	nLessons := 3                // 默认：3 × 7 × 10 MiB = 210 MiB 音频
	if os.Getenv("LESSONPACK_BIG") == "1" {
		nLessons = 29 // 约 2 GiB 音频，Base64 后约 2.7 GiB 包
	}
	var lessons []lessonSpec
	var ids []string
	var totalAudio int64
	for i := 0; i < nLessons; i++ {
		id := fmt.Sprintf("big-%03d", i)
		ids = append(ids, id)
		var audios []audioSpec
		for j := 0; j < 7; j++ { // 7 × 10 MiB = 70 MiB / 课件
			seed := uint64(i*100 + j + 1)
			audios = append(audios, audioSpec{
				seed: seed,
				size: audioSize,
				sha:  hashOf(seed, audioSize), // 独立计算摘要
			})
			totalAudio += audioSize
		}
		lessons = append(lessons, lessonSpec{id: id, audios: audios})
	}
	t.Logf("package: %d lessons, %d MiB audio total", nLessons, totalAudio>>20)

	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	// 导入：流式生成包，测量峰值堆。
	var results []LessonResult
	var importErr error
	peakImport := measurePeakHeap(func() {
		results, importErr = store.Import(newPackageReader(lessons))
	})
	if importErr != nil {
		t.Fatal(importErr)
	}
	for i, r := range results {
		if !r.OK {
			t.Fatalf("lesson %d failed: %+v", i, r)
		}
	}
	t.Logf("import peak heap delta: %.1f MiB", float64(peakImport)/float64(1<<20))
	if peakImport > heapLimit {
		t.Fatalf("import peak heap delta %.1f MiB exceeds 32 MiB", float64(peakImport)/float64(1<<20))
	}

	// 抽查首条课件每段音频：库内文件摘要与独立计算一致。
	for j, a := range lessons[0].audios {
		content, err := os.ReadFile(store.audioPath(a.sha))
		if err != nil {
			t.Fatalf("audio card %d: %v", j, err)
		}
		h := sha256.Sum256(content)
		if hex.EncodeToString(h[:]) != a.sha {
			t.Fatalf("audio card %d: stored content sha mismatch", j)
		}
	}

	// 导出：写向 discard，测量峰值堆。
	var exportErr error
	peakExport := measurePeakHeap(func() {
		exportErr = store.Export(io.Discard, ids)
	})
	if exportErr != nil {
		t.Fatal(exportErr)
	}
	t.Logf("export peak heap delta: %.1f MiB", float64(peakExport)/float64(1<<20))
	if peakExport > heapLimit {
		t.Fatalf("export peak heap delta %.1f MiB exceeds 32 MiB", float64(peakExport)/float64(1<<20))
	}

	// 临时目录已清理。
	if _, err := os.Stat(store.tmpDir()); !os.IsNotExist(err) {
		t.Fatal("tmp dir not cleaned")
	}
}
