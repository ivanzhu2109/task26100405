package lessonpack

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Library 是本地课件库：元数据与按 SHA-256 寻址的音频均存于 root 目录下。
//
//	root/meta/<hex(id)>.json  课件元数据（音频卡片不含 data）
//	root/audio/<sha256hex>    去重后的音频字节
//	root/tmp/                 导入过程中的临时文件，返回前清理
type Library struct {
	root string
}

// Open 打开（必要时创建）位于 root 的课件库。
func Open(root string) (*Library, error) {
	l := &Library{root: root}
	for _, d := range []string{l.metaDir(), l.audioDir(), l.tmpDir()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	return l, nil
}

func (l *Library) metaDir() string  { return filepath.Join(l.root, "meta") }
func (l *Library) audioDir() string { return filepath.Join(l.root, "audio") }
func (l *Library) tmpDir() string   { return filepath.Join(l.root, "tmp") }

func (l *Library) metaPath(id string) string {
	return filepath.Join(l.metaDir(), hex.EncodeToString([]byte(id))+".json")
}

func (l *Library) audioPath(sha256Hex string) string {
	return filepath.Join(l.audioDir(), sha256Hex)
}

// commitAudio 把校验通过的临时音频文件按摘要寻址移入正式库；
// 相同内容已存在时只保留一份。
func (l *Library) commitAudio(tmpPath, sha256Hex string) error {
	dst := l.audioPath(sha256Hex)
	if _, err := os.Stat(dst); err == nil {
		return os.Remove(tmpPath)
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		return fmt.Errorf("commit audio %s: %w", sha256Hex, err)
	}
	return nil
}

// writeMeta 持久化一条课件的元数据。
func (l *Library) writeMeta(m *lessonMeta) error {
	obj := map[string]any{
		"id":    m.id,
		"title": m.title,
		"cards": m.cardsJSON(),
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	return os.WriteFile(l.metaPath(m.id), b, 0o644)
}

// readMeta 读取一条课件的元数据。
func (l *Library) readMeta(id string) (*lessonMeta, error) {
	b, err := os.ReadFile(l.metaPath(id))
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		return nil, err
	}
	return metaFromJSON(obj)
}

var _ = io.EOF
