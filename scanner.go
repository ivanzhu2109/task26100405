package lessonpack

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"unicode/utf16"
	"unicode/utf8"
)

// scanner 是基于小缓冲的流式 JSON 扫描器，支持任意分段边界的不可回退字节流。
type scanner struct {
	br *bufio.Reader
}

func newScanner(r io.Reader) *scanner {
	return &scanner{br: bufio.NewReaderSize(r, 32*1024)}
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// peek 返回下一个非空白字节，不消费。
func (s *scanner) peek() (byte, error) {
	for {
		c, err := s.br.ReadByte()
		if err != nil {
			return 0, err
		}
		if !isSpace(c) {
			return c, s.br.UnreadByte()
		}
	}
}

// next 返回并消费下一个非空白字节。
func (s *scanner) next() (byte, error) {
	for {
		c, err := s.br.ReadByte()
		if err != nil {
			return 0, err
		}
		if !isSpace(c) {
			return c, nil
		}
	}
}

// expect 消费下一个非空白字节并要求等于 c。
func (s *scanner) expect(c byte) error {
	got, err := s.next()
	if err != nil {
		return err
	}
	if got != c {
		return fmt.Errorf("json: expect %q, got %q", c, got)
	}
	return nil
}

// readString 完整读取一个较短的 JSON 字符串值（键、id、sha256 等）。
func (s *scanner) readString() (string, error) {
	sr, err := s.stringReader()
	if err != nil {
		return "", err
	}
	b, err := io.ReadAll(sr)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// stringReader 返回一个流式产出 JSON 字符串解码字节的 Reader，
// 调用时流的下一个非空白字节应为开引号；Reader 返回 io.EOF 时
// 闭引号已被消费，流位于字符串之后。
func (s *scanner) stringReader() (io.Reader, error) {
	if err := s.expect('"'); err != nil {
		return nil, err
	}
	return &strReader{s: s}, nil
}

type strReader struct {
	s    *scanner
	pend [utf8.UTFMax]byte // 待吐出的已解码字节（代理对拼接时可能暂存）
	plen int
	done bool
}

func (r *strReader) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		if r.plen > 0 {
			m := copy(p[n:], r.pend[:r.plen])
			r.plen -= m
			copy(r.pend[:r.plen], r.pend[m:m+r.plen])
			n += m
			continue
		}
		if r.done {
			return n, io.EOF
		}
		c, err := r.s.br.ReadByte()
		if err != nil {
			return n, err
		}
		switch c {
		case '"':
			r.done = true
		case '\\':
			if err := r.readEscape(); err != nil {
				return n, err
			}
		default:
			p[n] = c
			n++
		}
	}
	return n, nil
}

// readEscape 处理反斜杠之后的转义序列，结果放入 pend。
func (r *strReader) readEscape() error {
	c, err := r.s.br.ReadByte()
	if err != nil {
		return err
	}
	switch c {
	case '"', '\\', '/':
		r.pend[0] = c
		r.plen = 1
	case 'b':
		r.pend[0] = '\b'
		r.plen = 1
	case 'f':
		r.pend[0] = '\f'
		r.plen = 1
	case 'n':
		r.pend[0] = '\n'
		r.plen = 1
	case 'r':
		r.pend[0] = '\r'
		r.plen = 1
	case 't':
		r.pend[0] = '\t'
		r.plen = 1
	case 'u':
		r1, err := r.readHex4()
		if err != nil {
			return err
		}
		if utf16.IsSurrogate(r1) {
			// 高代理后必须紧跟 \uXXXX 低代理
			if err := r.s.expect('\\'); err != nil {
				return err
			}
			if err := r.s.expect('u'); err != nil {
				return err
			}
			r2, err := r.readHex4()
			if err != nil {
				return err
			}
			r.plen = utf8.EncodeRune(r.pend[:], utf16.DecodeRune(r1, r2))
		} else {
			r.plen = utf8.EncodeRune(r.pend[:], r1)
		}
	default:
		return fmt.Errorf("json: invalid escape \\%c", c)
	}
	return nil
}

func (r *strReader) readHex4() (rune, error) {
	var v rune
	for i := 0; i < 4; i++ {
		c, err := r.s.br.ReadByte()
		if err != nil {
			return 0, err
		}
		var d byte
		switch {
		case c >= '0' && c <= '9':
			d = c - '0'
		case c >= 'a' && c <= 'f':
			d = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			d = c - 'A' + 10
		default:
			return 0, fmt.Errorf("json: invalid hex digit %q", c)
		}
		v = v*16 + rune(d)
	}
	return v, nil
}

// skipString 跳过字符串值，不分配内容。
func (s *scanner) skipString() error {
	if err := s.expect('"'); err != nil {
		return err
	}
	for {
		c, err := s.br.ReadByte()
		if err != nil {
			return err
		}
		switch c {
		case '"':
			return nil
		case '\\':
			if _, err := s.br.ReadByte(); err != nil {
				return err
			}
		}
	}
}

// readValue 读取任意 JSON 值；数字保留为 json.Number。
func (s *scanner) readValue() (any, error) {
	c, err := s.peek()
	if err != nil {
		return nil, err
	}
	switch {
	case c == '{':
		if err := s.expect('{'); err != nil {
			return nil, err
		}
		obj := map[string]any{}
		if c, err := s.next(); err != nil {
			return nil, err
		} else if c == '}' {
			return obj, nil
		} else if err := s.br.UnreadByte(); err != nil {
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
			v, err := s.readValue()
			if err != nil {
				return nil, err
			}
			obj[key] = v
			sep, err := s.next()
			if err != nil {
				return nil, err
			}
			if sep == '}' {
				return obj, nil
			}
			if sep != ',' {
				return nil, fmt.Errorf("json: expect ',' or '}', got %q", sep)
			}
		}
	case c == '[':
		if err := s.expect('['); err != nil {
			return nil, err
		}
		arr := []any{}
		if c, err := s.next(); err != nil {
			return nil, err
		} else if c == ']' {
			return arr, nil
		} else if err := s.br.UnreadByte(); err != nil {
			return nil, err
		}
		for {
			v, err := s.readValue()
			if err != nil {
				return nil, err
			}
			arr = append(arr, v)
			sep, err := s.next()
			if err != nil {
				return nil, err
			}
			if sep == ']' {
				return arr, nil
			}
			if sep != ',' {
				return nil, fmt.Errorf("json: expect ',' or ']', got %q", sep)
			}
		}
	case c == '"':
		return s.readString()
	default:
		return s.readLiteral()
	}
}

// readLiteral 读取数字、true、false、null。
func (s *scanner) readLiteral() (any, error) {
	var buf bytes.Buffer
	for {
		c, err := s.br.ReadByte()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		if isSpace(c) || c == ',' || c == ']' || c == '}' {
			if err := s.br.UnreadByte(); err != nil {
				return nil, err
			}
			break
		}
		buf.WriteByte(c)
	}
	switch lit := buf.String(); lit {
	case "true":
		return true, nil
	case "false":
		return false, nil
	case "null":
		return nil, nil
	default:
		return json.Number(lit), nil
	}
}

// skipValue 跳过任意 JSON 值。
func (s *scanner) skipValue() error {
	c, err := s.peek()
	if err != nil {
		return err
	}
	switch c {
	case '{':
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
			if err := s.skipString(); err != nil {
				return err
			}
			if err := s.expect(':'); err != nil {
				return err
			}
			if err := s.skipValue(); err != nil {
				return err
			}
			sep, err := s.next()
			if err != nil {
				return err
			}
			if sep == '}' {
				return nil
			}
		}
	case '[':
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
			if err := s.skipValue(); err != nil {
				return err
			}
			sep, err := s.next()
			if err != nil {
				return err
			}
			if sep == ']' {
				return nil
			}
		}
	case '"':
		return s.skipString()
	default:
		_, err := s.readLiteral()
		return err
	}
}
