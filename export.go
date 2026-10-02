package lessonpack

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// Export 按给定 id 序列导出同格式自包含包，顺序与 ids 一致。
// 每张音频卡片都携带完整 Base64 内容，流式编码写出，不占额外堆内存。
func (l *Library) Export(w io.Writer, ids []string) error {
	bw := bufio.NewWriterSize(w, 32*1024)
	if _, err := bw.WriteString(`{"lessons":[`); err != nil {
		return err
	}
	for i, id := range ids {
		if i > 0 {
			if err := bw.WriteByte(','); err != nil {
				return err
			}
		}
		m, err := l.readMeta(id)
		if err != nil {
			return fmt.Errorf("export lesson %q: %w", id, err)
		}
		if err := l.writeLesson(bw, m); err != nil {
			return fmt.Errorf("export lesson %q: %w", id, err)
		}
	}
	if _, err := bw.WriteString(`]}`); err != nil {
		return err
	}
	return bw.Flush()
}

func (l *Library) writeLesson(w *bufio.Writer, m *lessonMeta) error {
	idJSON, _ := json.Marshal(m.id)
	titleJSON, _ := json.Marshal(m.title)
	if _, err := fmt.Fprintf(w, `{"id":%s,"title":%s,"cards":[`, idJSON, titleJSON); err != nil {
		return err
	}
	for i, c := range m.cards {
		if i > 0 {
			if err := w.WriteByte(','); err != nil {
				return err
			}
		}
		if err := l.writeCard(w, &m.cards[i]); err != nil {
			return err
		}
		_ = c
	}
	if _, err := w.WriteString(`]}`); err != nil {
		return err
	}
	return nil
}

func (l *Library) writeCard(w *bufio.Writer, c *cardMeta) error {
	typeJSON, _ := json.Marshal(c.typ)
	if _, err := fmt.Fprintf(w, `{"type":%s`, typeJSON); err != nil {
		return err
	}
	if c.typ == "audio" {
		shaJSON, _ := json.Marshal(c.sha)
		if _, err := fmt.Fprintf(w, `,"sha256":%s,"data":"`, shaJSON); err != nil {
			return err
		}
		if err := l.streamAudioBase64(w, c.sha); err != nil {
			return err
		}
		if err := w.WriteByte('"'); err != nil {
			return err
		}
	}
	for k, v := range c.extra {
		kJSON, _ := json.Marshal(k)
		vJSON, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, `,%s:%s`, kJSON, vJSON); err != nil {
			return err
		}
	}
	return w.WriteByte('}')
}

// streamAudioBase64 把库内音频文件流式编码为 Base64 写出。
func (l *Library) streamAudioBase64(w io.Writer, sha256Hex string) error {
	f, err := os.Open(l.audioPath(sha256Hex))
	if err != nil {
		return err
	}
	defer f.Close()
	enc := base64.NewEncoder(base64.StdEncoding, w)
	if _, err := io.Copy(enc, f); err != nil {
		return err
	}
	return enc.Close()
}
