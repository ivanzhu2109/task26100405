package lessonpack

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// limitReader 限制每次 Read 的字节数，模拟任意分段边界的流式输入。
type limitReader struct {
	r io.Reader
	n int
}

func (l *limitReader) Read(p []byte) (int, error) {
	if len(p) > l.n {
		p = p[:l.n]
	}
	return l.r.Read(p)
}

// patternReader 生成确定性的伪随机字节流，不占内存。
type patternReader struct {
	remaining int64
	x         byte
}

func (p *patternReader) Read(b []byte) (int, error) {
	if p.remaining == 0 {
		return 0, io.EOF
	}
	n := int64(len(b))
	if n > p.remaining {
		n = p.remaining
	}
	for i := int64(0); i < n; i++ {
		p.x = p.x*166 + 101
		b[i] = p.x
	}
	p.remaining -= n
	return int(n), nil
}

// peakHeap 后台采样 Go 堆占用，返回峰值字节数。
type peakHeap struct {
	max  atomic.Int64
	done chan struct{}
}

func startPeakHeap() *peakHeap {
	p := &peakHeap{done: make(chan struct{})}
	go func() {
		for {
			select {
			case <-p.done:
				return
			default:
			}
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			if v := int64(m.HeapAlloc); v > p.max.Load() {
				p.max.Store(v)
			}
			time.Sleep(time.Millisecond)
		}
	}()
	return p
}

func (p *peakHeap) stop() int64 { close(p.done); return p.max.Load() }

const (
	miB          = 1 << 20
	giB          = 1 << 30
	heapLimitMiB = 32
)

// TestBigPackage 单课件 70MiB 音频、整包 ≥2GiB 时，
// 导入与导出的峰值 Go 堆占用均不得超过 32MiB。
func TestBigPackage(t *testing.T) {
	if testing.Short() {
		t.Skip("big package test skipped in -short mode")
	}
	dir := t.TempDir()
	pkgPath := filepath.Join(dir, "big.pkg")

	// 课件的音频大小：首条 70MiB，其余 50MiB，总量使整包 ≥ 2GiB
	sizes := []int64{70 * miB}
	for len(sizes) < 31 {
		sizes = append(sizes, 50*miB)
	}
	ids := make([]string, len(sizes))

	// 流式生成包文件：音频先落临时文件算摘要，再流式 Base64 写出
	pkgFile, err := os.Create(pkgPath)
	if err != nil {
		t.Fatal(err)
	}
	bw := bufio.NewWriterSize(pkgFile, 64*1024)
	bw.WriteString(`{"lessons":[`)
	for i, sz := range sizes {
		ids[i] = fmt.Sprintf("L%02d", i)
		tmp, err := os.CreateTemp(dir, "gen-*")
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.New()
		if _, err := io.Copy(io.MultiWriter(tmp, h), &patternReader{remaining: sz, x: byte(i)}); err != nil {
			t.Fatal(err)
		}
		sha := hex.EncodeToString(h.Sum(nil))
		tmp.Close()
		if i > 0 {
			bw.WriteByte(',')
		}
		fmt.Fprintf(bw, `{"id":"%s","title":"lesson %d","cards":[%s,%s,{"type":"audio","sha256":"%s","data":"`,
			ids[i], i, `{"type":"quiz","q":1}`, `{"type":"text","body":"x"}`, sha)
		func() {
			f, err := os.Open(tmp.Name())
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			enc := base64.NewEncoder(base64.StdEncoding, bw)
			if _, err := io.Copy(enc, f); err != nil {
				t.Fatal(err)
			}
			enc.Close()
		}()
		bw.WriteString(`"}]}`)
		os.Remove(tmp.Name())
	}
	bw.WriteString(`]}`)
	if err := bw.Flush(); err != nil {
		t.Fatal(err)
	}
	pkgFile.Close()

	st, err := os.Stat(pkgPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("package size: %.2f GiB (%d lessons)", float64(st.Size())/giB, len(sizes))
	if st.Size() < 2*giB {
		t.Fatalf("package too small: %d < 2GiB", st.Size())
	}

	// 导入：峰值堆占用不得超过 32MiB
	lib, err := Open(filepath.Join(dir, "lib"))
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(pkgPath)
	if err != nil {
		t.Fatal(err)
	}
	peak := startPeakHeap()
	res, err := lib.Import(&limitReader{r: f, n: 4099})
	importPeak := peak.stop()
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Failures) != 0 || len(res.Imported) != len(ids) {
		t.Fatalf("import result: %d imported, %+v", len(res.Imported), res.Failures)
	}
	t.Logf("import peak heap: %.2f MiB", float64(importPeak)/miB)
	if importPeak > heapLimitMiB*miB {
		t.Fatalf("import peak heap %.2f MiB exceeds %d MiB", float64(importPeak)/miB, heapLimitMiB)
	}

	// 导出：峰值堆占用不得超过 32MiB
	outPath := filepath.Join(dir, "out.pkg")
	out, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	peak = startPeakHeap()
	err = lib.Export(out, ids)
	exportPeak := peak.stop()
	out.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("export peak heap: %.2f MiB", float64(exportPeak)/miB)
	if exportPeak > heapLimitMiB*miB {
		t.Fatalf("export peak heap %.2f MiB exceeds %d MiB", float64(exportPeak)/miB, heapLimitMiB)
	}

	// 导出包可重新导入空库，且 70MiB 音频字节一致
	ost, err := os.Stat(outPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("exported size: %.2f GiB", float64(ost.Size())/giB)
	lib2, err := Open(filepath.Join(dir, "lib2"))
	if err != nil {
		t.Fatal(err)
	}
	of, err := os.Open(outPath)
	if err != nil {
		t.Fatal(err)
	}
	res2, err := lib2.Import(&limitReader{r: of, n: 8191})
	of.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.Failures) != 0 || len(res2.Imported) != len(ids) {
		t.Fatalf("reimport result: %d imported, %+v", len(res2.Imported), res2.Failures)
	}
	// 校验首条课件 70MiB 音频的摘要与独立计算一致
	h := sha256.New()
	if _, err := io.Copy(h, &patternReader{remaining: sizes[0], x: 0}); err != nil {
		t.Fatal(err)
	}
	got, err := os.Open(lib2.audioPath(hex.EncodeToString(h.Sum(nil))))
	if err != nil {
		t.Fatal(err)
	}
	defer got.Close()
	h2 := sha256.New()
	if _, err := io.Copy(h2, got); err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(h2.Sum(nil)) != hex.EncodeToString(h.Sum(nil)) {
		t.Fatal("70MiB audio bytes changed after big roundtrip")
	}
}
