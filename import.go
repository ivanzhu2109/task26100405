package lessonpack

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// lessonMeta 是课件的内存元数据（不含音频 data，规模受 16KiB 限制）。
type lessonMeta struct {
	id    string
	title string
	cards []cardMeta
}

// cardMeta 中 extra 保留 type/sha256/data 之外的其他字段的 JSON 值。
type cardMeta struct {
	typ   string
	sha   string
	extra map[string]any
}

// pendingAudio 是课件导入期间已解码到临时文件、待提交的音频。
type pendingAudio struct {
	tmpPath string
	hashHex string
}

// maxLessons 是包内课件数上限。
const maxLessons = 200

// Import 从流中导入自包含 JSON 包，返回每条课件的成功或失败记录。
// 音频数据流式解码落临时文件，单条课件全部校验通过后才提交进正式库。
func (l *Library) Import(r io.Reader) (*ImportResult, error) {
	s := newScanner(r)
	res := &ImportResult{}
	err := l.importPackage(s, res)
	// 操作返回前清理临时目录（成功课件及其共享音频已在正式库中，不在此列）。
	if cerr := l.cleanTmp(); cerr != nil && err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	return res, nil
}

func (l *Library) cleanTmp() error {
	if err := os.RemoveAll(l.tmpDir()); err != nil {
		return err
	}
	return os.MkdirAll(l.tmpDir(), 0o755)
}

func (l *Library) importPackage(s *scanner, res *ImportResult) error {
	if err := s.expect('{'); err != nil {
		return err
	}
	if c, err := s.next(); err != nil {
		return err
	} else if c == '}' {
		return nil
	} else if err := s.br.UnreadByte(); err != nil {
		return err
	}
	for {
		key, err := s.readString()
		if err != nil {
			return err
		}
		if err := s.expect(':'); err != nil {
			return err
		}
		if key == "lessons" {
			if err := l.importLessons(s, res); err != nil {
				return err
			}
		} else if err := s.skipValue(); err != nil {
			return err
		}
		sep, err := s.next()
		if err != nil {
			return err
		}
		if sep == '}' {
			return nil
		}
		if sep != ',' {
			return fmt.Errorf("json: expect ',' or '}', got %q", sep)
		}
	}
}

func (l *Library) importLessons(s *scanner, res *ImportResult) error {
	if err := s.expect('['); err != nil {
		return err
	}
	if c, err := s.next(); err != nil {
		return err
	} else if c == ']' {
		return nil
	} else if err := s.br.UnreadByte(); err != nil {
		return err
	}
	for idx := 0; ; idx++ {
		if idx >= maxLessons {
			return fmt.Errorf("too many lessons: more than %d", maxLessons)
		}
		if err := l.importOneLesson(s, idx, res); err != nil {
			return err
		}
		sep, err := s.next()
		if err != nil {
			return err
		}
		if sep == ']' {
			return nil
		}
		if sep != ',' {
			return fmt.Errorf("json: expect ',' or ']', got %q", sep)
		}
	}
}

// importOneLesson 解析并导入一条课件；音频校验失败只拒绝本课件。
func (l *Library) importOneLesson(s *scanner, idx int, res *ImportResult) error {
	m := &lessonMeta{}
	var pend []pendingAudio
	var failure *LessonFailure
	cardIdx := 0

	if err := s.expect('{'); err != nil {
		return err
	}
	if c, err := s.next(); err != nil {
		return err
	} else if c != '}' {
		if err := s.br.UnreadByte(); err != nil {
			return err
		}
		for {
			key, err := s.readString()
			if err != nil {
				return err
			}
			if err := s.expect(':'); err != nil {
				return err
			}
			switch key {
			case "id":
				if m.id, err = s.readString(); err != nil {
					return err
				}
			case "title":
				if m.title, err = s.readString(); err != nil {
					return err
				}
			case "cards":
				if err := l.importCards(s, m, &pend, &failure, &cardIdx); err != nil {
					return err
				}
			default:
				if err := s.skipValue(); err != nil {
					return err
				}
			}
			sep, err := s.next()
			if err != nil {
				return err
			}
			if sep == '}' {
				break
			}
			if sep != ',' {
				return fmt.Errorf("json: expect ',' or '}', got %q", sep)
			}
		}
	}

	if failure != nil {
		// 拒绝本课件：删除其临时音频；未被其他成功课件引用的内容不会进正式库。
		for _, p := range pend {
			os.Remove(p.tmpPath)
		}
		failure.LessonIndex = idx
		failure.LessonID = m.id
		res.Failures = append(res.Failures, *failure)
		return nil
	}
	for _, p := range pend {
		if err := l.commitAudio(p.tmpPath, p.hashHex); err != nil {
			return err
		}
	}
	if err := l.writeMeta(m); err != nil {
		return err
	}
	res.Imported = append(res.Imported, m.id)
	return nil
}

func (l *Library) importCards(s *scanner, m *lessonMeta, pend *[]pendingAudio, failure **LessonFailure, cardIdx *int) error {
	if err := s.expect('['); err != nil {
		return err
	}
	if c, err := s.next(); err != nil {
		return err
	} else if c == ']' {
		return nil
	} else if err := s.br.UnreadByte(); err != nil {
		return err
	}
	for {
		c, err := l.importCard(s, *cardIdx, failure)
		if err != nil {
			return err
		}
		m.cards = append(m.cards, c.meta)
		if c.audio != nil {
			*pend = append(*pend, *c.audio)
		}
		*cardIdx++
		sep, err := s.next()
		if err != nil {
			return err
		}
		if sep == ']' {
			return nil
		}
		if sep != ',' {
			return fmt.Errorf("json: expect ',' or ']', got %q", sep)
		}
	}
}

type parsedCard struct {
	meta  cardMeta
	audio *pendingAudio
}

// importCard 解析一张卡片；音频 data 流式解码到临时文件并计算摘要。
// 若课件已被拒绝（*failure != nil），仍完整解析但丢弃音频内容。
func (l *Library) importCard(s *scanner, cardIdx int, failure **LessonFailure) (*parsedCard, error) {
	pc := &parsedCard{meta: cardMeta{extra: map[string]any{}}}
	var hashHex string
	var tmpPath string
	hashDone := false

	if err := s.expect('{'); err != nil {
		return nil, err
	}
	if c, err := s.next(); err != nil {
		return nil, err
	} else if c != '}' {
		if err := s.br.UnreadByte(); err != nil {
			return nil, err
		}
		for {
			key, err := s.readString()
			if err != nil {
				return nil, err
			}
			if err := s.expect(':'); err != nil {
				return nil, err
			}
			switch key {
			case "type":
				if pc.meta.typ, err = s.readString(); err != nil {
					return nil, err
				}
			case "sha256":
				if pc.meta.sha, err = s.readString(); err != nil {
					return nil, err
				}
			case "data":
				if *failure != nil {
					// 课件已被拒绝：丢弃后续音频内容
					if err := s.skipString(); err != nil {
						return nil, err
					}
					break
				}
				hh, tp, err := l.decodeAudio(s)
				if err != nil {
					if err == ErrInvalidBase64 {
						*failure = &LessonFailure{CardIndex: cardIdx, Reason: FailureInvalidBase64}
						break
					}
					return nil, err
				}
				hashHex, tmpPath, hashDone = hh, tp, true
				if pc.meta.sha != "" && pc.meta.sha != hashHex {
					*failure = &LessonFailure{CardIndex: cardIdx, Reason: FailureHashMismatch}
				}
			default:
				v, err := s.readValue()
				if err != nil {
					return nil, err
				}
				pc.meta.extra[key] = v
			}
			sep, err := s.next()
			if err != nil {
				return nil, err
			}
			if sep == '}' {
				break
			}
			if sep != ',' {
				return nil, fmt.Errorf("json: expect ',' or '}', got %q", sep)
			}
		}
	}

	if *failure != nil {
		if tmpPath != "" {
			os.Remove(tmpPath)
		}
		return pc, nil
	}
	if pc.meta.typ == "audio" && hashDone {
		if pc.meta.sha != hashHex {
			// sha256 字段出现在 data 之后的情况
			*failure = &LessonFailure{CardIndex: cardIdx, Reason: FailureHashMismatch}
			os.Remove(tmpPath)
			return pc, nil
		}
		pc.audio = &pendingAudio{tmpPath: tmpPath, hashHex: hashHex}
	}
	return pc, nil
}

// decodeAudio 把当前位置的 JSON 字符串值流式解码为音频字节，
// 写入临时文件并返回其 SHA-256 摘要（小写十六进制）与临时路径。
func (l *Library) decodeAudio(s *scanner) (hashHex, tmpPath string, err error) {
	sr, err := s.stringReader()
	if err != nil {
		return "", "", err
	}
	tmp, err := os.CreateTemp(l.tmpDir(), "audio-*")
	if err != nil {
		return "", "", err
	}
	h := sha256.New()
	_, decErr := io.Copy(io.MultiWriter(tmp, h), newB64Decoder(sr))
	if decErr != nil {
		// 解码失败：排干字符串其余部分以保持流位置，再报告
		io.Copy(io.Discard, sr)
		tmp.Close()
		os.Remove(tmp.Name())
		if decErr == ErrInvalidBase64 {
			return "", "", ErrInvalidBase64
		}
		return "", "", decErr
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", "", err
	}
	return hex.EncodeToString(h.Sum(nil)), tmp.Name(), nil
}

// cardsJSON 生成卡片数组的 JSON 值（音频卡片不含 data）。
func (m *lessonMeta) cardsJSON() []any {
	cards := make([]any, 0, len(m.cards))
	for _, c := range m.cards {
		obj := map[string]any{"type": c.typ}
		for k, v := range c.extra {
			obj[k] = v
		}
		if c.typ == "audio" {
			obj["sha256"] = c.sha
		}
		cards = append(cards, obj)
	}
	return cards
}

// metaFromJSON 把 json.Unmarshal(UseNumber) 得到的对象还原为 lessonMeta。
func metaFromJSON(obj map[string]any) (*lessonMeta, error) {
	m := &lessonMeta{}
	id, _ := obj["id"].(string)
	title, _ := obj["title"].(string)
	m.id, m.title = id, title
	rawCards, ok := obj["cards"].([]any)
	if !ok {
		return nil, fmt.Errorf("meta: missing cards")
	}
	for _, rc := range rawCards {
		co, ok := rc.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("meta: bad card")
		}
		c := cardMeta{extra: map[string]any{}}
		for k, v := range co {
			switch k {
			case "type":
				c.typ, _ = v.(string)
			case "sha256":
				c.sha, _ = v.(string)
			default:
				c.extra[k] = v
			}
		}
		m.cards = append(m.cards, c)
	}
	return m, nil
}
