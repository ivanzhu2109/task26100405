package lessonpack

import (
	"bytes"
	"fmt"
	"io"
	"unicode/utf16"
	"unicode/utf8"
)

// lexer 是不可回退字节流上的流式 JSON 词法器。
// 输入保证语法合法，词法器只做必要的结构识别与字符串解释，
// 任何时刻只持有很小的滑动窗口。
type lexer struct {
	r   io.Reader
	buf []byte
	pos int
	eof bool
}

func newLexer(r io.Reader) *lexer {
	return &lexer{r: r, buf: make([]byte, 0, 32<<10)}
}

var errUnexpectedEOF = fmt.Errorf("lessonpack: unexpected end of JSON input")

// fill 保证缓冲区至少有一个未消费字节；到达流尾时返回 io.EOF。
func (l *lexer) fill() error {
	if l.pos < len(l.buf) {
		return nil
	}
	if l.eof {
		return io.EOF
	}
	// 压缩已消费前缀，保持窗口有界。
	l.buf = l.buf[:0]
	l.pos = 0
	tmp := l.buf[:cap(l.buf)]
	n, err := l.r.Read(tmp)
	if n > 0 {
		l.buf = tmp[:n]
	}
	if err == io.EOF {
		l.eof = true
		if n == 0 {
			return io.EOF
		}
		return nil
	}
	return err
}

// peek 返回当前字节但不消费。
func (l *lexer) peek() (byte, error) {
	if err := l.fill(); err != nil {
		return 0, err
	}
	return l.buf[l.pos], nil
}

// next 消费并返回当前字节。
func (l *lexer) next() (byte, error) {
	c, err := l.peek()
	if err != nil {
		return 0, err
	}
	l.pos++
	return c, nil
}

func isWS(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// skipWS 跳过标准 JSON 空白。
func (l *lexer) skipWS() error {
	for {
		c, err := l.peek()
		if err != nil {
			return err
		}
		if !isWS(c) {
			return nil
		}
		l.pos++
	}
}

// nextNonWS 跳过空白并返回当前字节（不消费）。
func (l *lexer) nextNonWS() (byte, error) {
	if err := l.skipWS(); err != nil {
		return 0, err
	}
	return l.peek()
}

// expect 消费一个非空白字节并要求它等于 want。
func (l *lexer) expect(want byte) error {
	c, err := l.nextNonWS()
	if err != nil {
		return err
	}
	if c != want {
		return fmt.Errorf("lessonpack: expected %q, got %q", want, c)
	}
	l.pos++
	return nil
}

// readString 从当前位置的 '"' 开始，解释 JSON 字符串（含转义），
// 把解码后的字节写入 w，消费到闭引号为止。
func (l *lexer) readString(w io.Writer) error {
	if err := l.skipWS(); err != nil {
		return err
	}
	c, err := l.next()
	if err != nil {
		return err
	}
	if c != '"' {
		return fmt.Errorf("lessonpack: expected string, got %q", c)
	}
	var chunk [1]byte
	for {
		if err := l.fill(); err != nil {
			if err == io.EOF {
				return errUnexpectedEOF
			}
			return err
		}
		// 批量写出普通字符段，仅在引号与反斜杠处逐字节处理。
		i := l.pos
		for i < len(l.buf) && l.buf[i] != '"' && l.buf[i] != '\\' {
			i++
		}
		if i > l.pos {
			if _, err := w.Write(l.buf[l.pos:i]); err != nil {
				return err
			}
			l.pos = i
		}
		c, err = l.next()
		if err != nil {
			return errUnexpectedEOF
		}
		switch c {
		case '"':
			return nil
		case '\\':
			if err := l.readEscape(w); err != nil {
				return err
			}
		default:
			chunk[0] = c
			if _, err := w.Write(chunk[:]); err != nil {
				return err
			}
		}
	}
}

// readEscape 处理反斜杠之后的一个转义序列，把解码字节写入 w。
func (l *lexer) readEscape(w io.Writer) error {
	c, err := l.next()
	if err != nil {
		return errUnexpectedEOF
	}
	var out []byte
	switch c {
	case '"', '\\', '/':
		out = []byte{c}
	case 'b':
		out = []byte{'\b'}
	case 'f':
		out = []byte{'\f'}
	case 'n':
		out = []byte{'\n'}
	case 'r':
		out = []byte{'\r'}
	case 't':
		out = []byte{'\t'}
	case 'u':
		r, err := l.readHexRune()
		if err != nil {
			return err
		}
		if utf16.IsSurrogate(r) {
			// 高代理项后必须跟 \uXXXX 低代理项。
			if _, err := l.next(); err != nil {
				return errUnexpectedEOF
			}
			if _, err := l.next(); err != nil {
				return errUnexpectedEOF
			}
			lo, err := l.readHexRune()
			if err != nil {
				return err
			}
			r = utf16.DecodeRune(r, lo)
		}
		var tmp [utf8.UTFMax]byte
		n := utf8.EncodeRune(tmp[:], r)
		out = tmp[:n]
	default:
		return fmt.Errorf("lessonpack: invalid escape \\%c", c)
	}
	_, err = w.Write(out)
	return err
}

// readHexRune 读取 4 个十六进制数字并返回对应码点。
func (l *lexer) readHexRune() (rune, error) {
	var v rune
	for i := 0; i < 4; i++ {
		c, err := l.next()
		if err != nil {
			return 0, errUnexpectedEOF
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
			return 0, fmt.Errorf("lessonpack: invalid \\u escape digit %q", c)
		}
		v = v*16 + rune(d)
	}
	return v, nil
}

// readSmallString 读取一个长度受限的字符串值。
func (l *lexer) readSmallString(limit int) (string, error) {
	var buf bytes.Buffer
	if err := l.readString(&buf); err != nil {
		return "", err
	}
	if buf.Len() > limit {
		return "", fmt.Errorf("lessonpack: string exceeds %d bytes", limit)
	}
	return buf.String(), nil
}

// copyValue 把当前位置的一个完整 JSON 值的原始字节拷贝到 w。
// w 为 nil 时只跳过该值，不保留字节。
// 不解释数字与字面量，只按结构跳过字符串、数组与对象。
func (l *lexer) copyValue(w *bytes.Buffer) error {
	c, err := l.nextNonWS()
	if err != nil {
		return err
	}
	switch {
	case c == '"':
		return l.copyString(w)
	case c == '{' || c == '[':
		return l.copyComposite(w)
	default:
		// 数字或 true/false/null：拷贝到分隔符为止。
		for {
			c, err := l.peek()
			if err != nil {
				if err == io.EOF {
					return nil
				}
				return err
			}
			if isWS(c) || c == ',' || c == '}' || c == ']' {
				return nil
			}
			if w != nil {
				w.WriteByte(c)
			}
			l.pos++
		}
	}
}

// skipValue 跳过当前位置的一个完整 JSON 值。
func (l *lexer) skipValue() error { return l.copyValue(nil) }

// copyString 原样拷贝一个字符串（含引号与转义序列）。
// w 为 nil 时只跳过。
func (l *lexer) copyString(w *bytes.Buffer) error {
	c, err := l.next()
	if err != nil {
		return err
	}
	if w != nil {
		w.WriteByte(c) // 开引号
	}
	for {
		c, err = l.next()
		if err != nil {
			return errUnexpectedEOF
		}
		if w != nil {
			w.WriteByte(c)
		}
		if c == '"' {
			return nil
		}
		if c == '\\' {
			c, err = l.next()
			if err != nil {
				return errUnexpectedEOF
			}
			if w != nil {
				w.WriteByte(c)
			}
		}
	}
}

// copyComposite 原样拷贝数组或对象（含嵌套）。
// w 为 nil 时只跳过。
func (l *lexer) copyComposite(w *bytes.Buffer) error {
	depth := 0
	for {
		c, err := l.next()
		if err != nil {
			return errUnexpectedEOF
		}
		if w != nil {
			w.WriteByte(c)
		}
		switch c {
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 {
				return nil
			}
		case '"':
			if err := l.copyString(w); err != nil {
				return err
			}
		}
	}
}
