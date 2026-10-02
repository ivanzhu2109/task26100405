package lessonpack

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
)

// rawField 是按键保留的任意 JSON 值（原始字节）。
type rawField struct {
	Key string          `json:"k"`
	Raw json.RawMessage `json:"v"`
}

// cardMeta 是卡片在库中的元数据；音频卡片不存 data，只存 sha256。
type cardMeta struct {
	Type   string     `json:"type"`
	SHA256 string     `json:"sha256,omitempty"`
	Fields []rawField `json:"fields,omitempty"`
}

// lessonMeta 是课件在库中的元数据。
type lessonMeta struct {
	ID     string     `json:"id"`
	Fields []rawField `json:"fields,omitempty"`
	Cards  []cardMeta `json:"cards"`
}

const maxAudioBytes = 10 << 20 // 每段音频解码后不超过 10 MiB

func (s *Store) lessonsDir() string { return filepath.Join(s.dir, "lessons") }
func (s *Store) audioDir() string   { return filepath.Join(s.dir, "audio") }
func (s *Store) tmpDir() string     { return filepath.Join(s.dir, "tmp") }

func (s *Store) lessonPath(id string) string {
	return filepath.Join(s.lessonsDir(), url.PathEscape(id)+".json")
}

func (s *Store) audioPath(sha256Hex string) string {
	return filepath.Join(s.audioDir(), sha256Hex)
}

// Import 从 r 流式读取自包含 JSON 包，逐条导入课件。
func (s *Store) Import(r io.Reader) ([]LessonResult, error) {
	for _, d := range []string{s.lessonsDir(), s.audioDir(), s.tmpDir()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	defer os.Remove(s.tmpDir()) // 仅当为空时成功；临时文件须已清理

	lx := newLexer(r)
	if err := lx.expect('{'); err != nil {
		return nil, err
	}
	var results []LessonResult
	for {
		c, err := lx.nextNonWS()
		if err != nil {
			return nil, errUnexpectedEOF
		}
		if c == '}' {
			lx.pos++
			break
		}
		key, err := lx.readSmallString(256)
		if err != nil {
			return nil, err
		}
		if err := lx.expect(':'); err != nil {
			return nil, err
		}
		if key != "lessons" {
			// 顶层唯一字段为 lessons；防御性跳过其他字段。
			if err := lx.skipValue(); err != nil {
				return nil, err
			}
		} else {
			if err := lx.expect('['); err != nil {
				return nil, err
			}
			for {
				c, err := lx.nextNonWS()
				if err != nil {
					return nil, errUnexpectedEOF
				}
				if c == ']' {
					lx.pos++
					break
				}
				res, err := s.importLesson(lx)
				if err != nil {
					return nil, err
				}
				results = append(results, res)
				c, err = lx.nextNonWS()
				if err != nil {
					return nil, errUnexpectedEOF
				}
				if c == ',' {
					lx.pos++
					continue
				}
				if c == ']' {
					lx.pos++
					break
				}
				return nil, fmt.Errorf("lessonpack: expected ',' or ']' in lessons array")
			}
		}
		c, err = lx.nextNonWS()
		if err != nil {
			return nil, errUnexpectedEOF
		}
		if c == ',' {
			lx.pos++
			continue
		}
		if c == '}' {
			lx.pos++
			break
		}
		return nil, fmt.Errorf("lessonpack: expected ',' or '}' in top-level object")
	}
	if err := lx.skipWS(); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("lessonpack: trailing data after top-level object")
		}
		return nil, err
	}
	return results, nil
}

// importLesson 解析并导入一条课件；音频损坏时只拒绝该课件。
func (s *Store) importLesson(lx *lexer) (LessonResult, error) {
	p := &lessonParser{store: s}
	defer p.cleanup() // commit 后临时文件已改名或删除，此处兜底
	err := p.parse(lx)
	if err != nil {
		return LessonResult{}, err
	}
	res := LessonResult{ID: p.meta.ID}
	if p.failed {
		res.Reason = p.reason
		res.CardIndex = p.failCard
		return res, nil
	}
	// 校验每段音频摘要。
	for i := range p.meta.Cards {
		c := &p.meta.Cards[i]
		if c.Type != "audio" {
			continue
		}
		if p.computed[i] != c.SHA256 {
			res.Reason = ReasonSHA256Mismatch
			res.CardIndex = i
			return res, nil
		}
	}
	if err := p.commit(); err != nil {
		return LessonResult{}, err
	}
	res.OK = true
	return res, nil
}

// lessonParser 保存单条课件的解析状态。
type lessonParser struct {
	store    *Store
	meta     lessonMeta
	staged   []string // 本课件的临时音频文件
	computed []string // 每张卡片解码出的摘要（非音频卡为空）
	failed   bool
	reason   FailureReason
	failCard int
}

func (p *lessonParser) fail(reason FailureReason, card int) {
	if !p.failed {
		p.failed = true
		p.reason = reason
		p.failCard = card
	}
}

// cleanup 删除本课件的全部临时音频文件。
func (p *lessonParser) cleanup() {
	for _, f := range p.staged {
		os.Remove(f)
	}
	p.staged = nil
}

// parse 消费一个课件对象；失败后仍跳过剩余部分以继续后续课件。
func (p *lessonParser) parse(lx *lexer) error {
	if err := lx.expect('{'); err != nil {
		return err
	}
	for {
		c, err := lx.nextNonWS()
		if err != nil {
			return errUnexpectedEOF
		}
		if c == '}' {
			lx.pos++
			return nil
		}
		key, err := lx.readSmallString(256)
		if err != nil {
			return err
		}
		if err := lx.expect(':'); err != nil {
			return err
		}
		switch {
		case key == "id":
			// 即使课件已失败也要读出 id，用于失败记录定位。
			id, err := lx.readSmallString(1024)
			if err != nil {
				return err
			}
			p.meta.ID = id
		case p.failed:
			if err := lx.skipValue(); err != nil {
				return err
			}
		case key == "cards":
			if err := p.parseCards(lx); err != nil {
				return err
			}
		default:
			var buf bytes.Buffer
			if err := lx.copyValue(&buf); err != nil {
				return err
			}
			p.meta.Fields = append(p.meta.Fields, rawField{Key: key, Raw: buf.Bytes()})
		}
		c, err = lx.nextNonWS()
		if err != nil {
			return errUnexpectedEOF
		}
		if c == ',' {
			lx.pos++
			continue
		}
		if c == '}' {
			lx.pos++
			return nil
		}
		return fmt.Errorf("lessonpack: expected ',' or '}' in lesson object")
	}
}

// parseCards 消费 cards 数组。
func (p *lessonParser) parseCards(lx *lexer) error {
	if err := lx.expect('['); err != nil {
		return err
	}
	idx := 0
	for {
		c, err := lx.nextNonWS()
		if err != nil {
			return errUnexpectedEOF
		}
		if c == ']' {
			lx.pos++
			return nil
		}
		if err := p.parseCard(lx, idx); err != nil {
			return err
		}
		idx++
		c, err = lx.nextNonWS()
		if err != nil {
			return errUnexpectedEOF
		}
		if c == ',' {
			lx.pos++
			continue
		}
		if c == ']' {
			lx.pos++
			return nil
		}
		return fmt.Errorf("lessonpack: expected ',' or ']' in cards array")
	}
}

// parseCard 消费一张卡片对象；data 字段流式解码到临时文件。
func (p *lessonParser) parseCard(lx *lexer, idx int) error {
	if err := lx.expect('{'); err != nil {
		return err
	}
	var card cardMeta
	var computed string
	for {
		c, err := lx.nextNonWS()
		if err != nil {
			return errUnexpectedEOF
		}
		if c == '}' {
			lx.pos++
			break
		}
		key, err := lx.readSmallString(256)
		if err != nil {
			return err
		}
		if err := lx.expect(':'); err != nil {
			return err
		}
		switch {
		case p.failed:
			if err := lx.skipValue(); err != nil {
				return err
			}
		case key == "type":
			t, err := lx.readSmallString(64)
			if err != nil {
				return err
			}
			card.Type = t
		case key == "sha256":
			v, err := lx.readSmallString(128)
			if err != nil {
				return err
			}
			card.SHA256 = v
		case key == "data":
			sum, err := p.stageAudio(lx)
			if err != nil {
				p.fail(ReasonInvalidBase64, idx)
			} else {
				computed = sum
			}
		default:
			var buf bytes.Buffer
			if err := lx.copyValue(&buf); err != nil {
				return err
			}
			card.Fields = append(card.Fields, rawField{Key: key, Raw: buf.Bytes()})
		}
		c, err = lx.nextNonWS()
		if err != nil {
			return errUnexpectedEOF
		}
		if c == ',' {
			lx.pos++
			continue
		}
		if c == '}' {
			lx.pos++
			break
		}
		return fmt.Errorf("lessonpack: expected ',' or '}' in card object")
	}
	p.meta.Cards = append(p.meta.Cards, card)
	p.computed = append(p.computed, computed)
	return nil
}

// stageAudio 把 data 字符串流式解码到临时文件，返回解码字节的 sha256 十六进制。
func (p *lessonParser) stageAudio(lx *lexer) (string, error) {
	tmp, err := os.CreateTemp(p.store.tmpDir(), "audio-*")
	if err != nil {
		return "", err
	}
	path := tmp.Name()
	h := sha256.New()
	w := io.MultiWriter(tmp, h)
	bw := bufio.NewWriterSize(&limitWriter{w: w, limit: maxAudioBytes}, 64<<10)
	dec := newB64Decoder(bw)
	// 解码出错后继续吞掉字符串剩余部分，保证词法位置停在闭引号之后。
	dw := &discardAfterErr{w: dec}
	if lexErr := lx.readString(dw); lexErr != nil {
		tmp.Close()
		os.Remove(path)
		return "", lexErr
	}
	err = dw.err
	if err == nil {
		err = dec.close()
	}
	if ferr := bw.Flush(); err == nil {
		err = ferr
	}
	cerr := tmp.Close()
	if err != nil {
		os.Remove(path)
		return "", errInvalidBase64
	}
	if cerr != nil {
		os.Remove(path)
		return "", cerr
	}
	p.staged = append(p.staged, path)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// discardAfterErr 记录首个写入错误并丢弃后续数据，让上层继续消费输入。
type discardAfterErr struct {
	w   io.Writer
	err error
}

func (d *discardAfterErr) Write(p []byte) (int, error) {
	if d.err == nil {
		if _, err := d.w.Write(p); err != nil {
			d.err = err
		}
	}
	return len(p), nil
}

// limitWriter 在写入超过 limit 字节时报错。
type limitWriter struct {
	w     io.Writer
	limit int64
	n     int64
}

func (l *limitWriter) Write(p []byte) (int, error) {
	l.n += int64(len(p))
	if l.n > l.limit {
		return 0, fmt.Errorf("lessonpack: audio exceeds %d bytes", l.limit)
	}
	return l.w.Write(p)
}

// commit 把校验通过的课件落入正式库：音频按摘要去重，元数据写盘。
func (p *lessonParser) commit() error {
	for i := range p.meta.Cards {
		c := &p.meta.Cards[i]
		if c.Type != "audio" {
			continue
		}
		staged := p.stagedFile(i)
		target := p.store.audioPath(c.SHA256)
		if _, err := os.Stat(target); err == nil {
			// 已有相同音频（本包内或以往课件共享），去重。
			os.Remove(staged)
			continue
		}
		if err := os.Rename(staged, target); err != nil {
			return err
		}
	}
	data, err := json.Marshal(&p.meta)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(p.store.tmpDir(), "lesson-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), p.store.lessonPath(p.meta.ID))
}

// stagedFile 返回第 i 张（音频）卡片对应的临时文件路径。
func (p *lessonParser) stagedFile(i int) string {
	// staged 与音频卡片按出现顺序一一对应。
	n := 0
	for j := 0; j < i; j++ {
		if p.meta.Cards[j].Type == "audio" {
			n++
		}
	}
	return p.staged[n]
}
