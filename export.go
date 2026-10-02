package lessonpack

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// Export 按 ids 给定顺序把课件写为自包含 JSON 包。
// 音频从库内文件流式重新编码为 Base64，不在内存中累积。
func (s *Store) Export(w io.Writer, ids []string) error {
	bw := bufio.NewWriterSize(w, 64<<10)
	bw.WriteString(`{"lessons":[`)
	for i, id := range ids {
		if i > 0 {
			bw.WriteByte(',')
		}
		if err := s.exportLesson(bw, id); err != nil {
			return err
		}
	}
	bw.WriteString(`]}`)
	return bw.Flush()
}

// exportLesson 写出一条课件；音频卡片内联完整 Base64 数据。
func (s *Store) exportLesson(w *bufio.Writer, id string) error {
	data, err := os.ReadFile(s.lessonPath(id))
	if err != nil {
		return fmt.Errorf("lessonpack: lesson %q: %w", id, err)
	}
	var meta lessonMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return err
	}
	w.WriteByte('{')
	w.WriteString(`"id":`)
	writeJSONString(w, meta.ID)
	// 其余顶层字段（含 title）按原始 JSON 值写出。
	for _, f := range meta.Fields {
		w.WriteByte(',')
		writeJSONString(w, f.Key)
		w.WriteByte(':')
		w.Write(f.Raw)
	}
	w.WriteString(`,"cards":[`)
	for i := range meta.Cards {
		if i > 0 {
			w.WriteByte(',')
		}
		if err := s.exportCard(w, &meta.Cards[i]); err != nil {
			return err
		}
	}
	w.WriteString(`]}`)
	return nil
}

// exportCard 写出一张卡片。
func (s *Store) exportCard(w *bufio.Writer, c *cardMeta) error {
	w.WriteString(`{"type":`)
	writeJSONString(w, c.Type)
	if c.Type == "audio" {
		w.WriteString(`,"sha256":`)
		writeJSONString(w, c.SHA256)
		w.WriteString(`,"data":"`)
		f, err := os.Open(s.audioPath(c.SHA256))
		if err != nil {
			return fmt.Errorf("lessonpack: audio %s: %w", c.SHA256, err)
		}
		// 直接写到底层 bufio，音频字节不在堆上累积。
		err = encodeBase64(w, f)
		cerr := f.Close()
		if err != nil {
			return err
		}
		if cerr != nil {
			return cerr
		}
		w.WriteByte('"')
	}
	for _, f := range c.Fields {
		w.WriteByte(',')
		writeJSONString(w, f.Key)
		w.WriteByte(':')
		w.Write(f.Raw)
	}
	w.WriteByte('}')
	return nil
}

// writeJSONString 以 JSON 字符串形式写出 s。
func writeJSONString(w *bufio.Writer, s string) {
	b, _ := json.Marshal(s)
	w.Write(b)
}
