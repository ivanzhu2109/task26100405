package lessonpack

import (
	"encoding/base64"
	"fmt"
	"io"
)

// errInvalidBase64 表示音频 data 不是严格的 RFC4648 第4节 Base64。
var errInvalidBase64 = fmt.Errorf("lessonpack: invalid base64 audio data")

// b64Decoder 是流式严格 Base64 解码器：实现 io.Writer，
// 接收 JSON 字符串解释后的字符流，把解码字节写入下游。
// 拒绝内嵌空白、错误补位与非零末尾未使用位。
type b64Decoder struct {
	w        io.Writer
	quantum  [4]byte
	n        int  // quantum 中已收字符数
	padded   bool // 已出现 '=' 补位，之后不得再有输入
	finished bool
}

func newB64Decoder(w io.Writer) *b64Decoder { return &b64Decoder{w: w} }

var b64rev [256]int8

func init() {
	for i := range b64rev {
		b64rev[i] = -1
	}
	alphabet := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	for i := 0; i < len(alphabet); i++ {
		b64rev[alphabet[i]] = int8(i)
	}
}

func (d *b64Decoder) Write(p []byte) (int, error) {
	for i, c := range p {
		if err := d.feed(c); err != nil {
			return i, err
		}
	}
	return len(p), nil
}

func (d *b64Decoder) feed(c byte) error {
	if d.finished {
		return errInvalidBase64
	}
	if c == '=' {
		// '=' 只允许出现在量子末尾两位。
		if d.n < 2 {
			return errInvalidBase64
		}
		d.padded = true
	} else {
		if d.padded || b64rev[c] < 0 {
			// 补位后还有有效字符，或字符不在字母表内（含空白）。
			return errInvalidBase64
		}
	}
	d.quantum[d.n] = c
	d.n++
	if d.n == 4 {
		return d.flush()
	}
	return nil
}

// flush 解码一个完整量子并写下游。
func (d *b64Decoder) flush() error {
	q := d.quantum
	var out [3]byte
	var n int
	switch {
	case q[2] == '=':
		// xx==：第二字符低 4 位必须为零。
		v0, v1 := b64rev[q[0]], b64rev[q[1]]
		if q[3] != '=' || v1&0x0f != 0 {
			return errInvalidBase64
		}
		out[0] = byte(v0)<<2 | byte(v1)>>4
		n = 1
		d.finished = true
	case q[3] == '=':
		// xxx=：第三字符低 2 位必须为零。
		v0, v1, v2 := b64rev[q[0]], b64rev[q[1]], b64rev[q[2]]
		if v2&0x03 != 0 {
			return errInvalidBase64
		}
		out[0] = byte(v0)<<2 | byte(v1)>>4
		out[1] = byte(v1)<<4 | byte(v2)>>2
		n = 2
		d.finished = true
	default:
		v0, v1 := b64rev[q[0]], b64rev[q[1]]
		v2, v3 := b64rev[q[2]], b64rev[q[3]]
		out[0] = byte(v0)<<2 | byte(v1)>>4
		out[1] = byte(v1)<<4 | byte(v2)>>2
		out[2] = byte(v2)<<6 | byte(v3)
		n = 3
	}
	d.n = 0
	d.padded = false
	_, err := d.w.Write(out[:n])
	return err
}

// close 在字符串结束时做最终校验：长度必须是 4 的倍数。
func (d *b64Decoder) close() error {
	if d.n != 0 {
		// 缺少必要的等号补位。
		return errInvalidBase64
	}
	return nil
}

// encodeBase64 把 src 的全部字节以标准 Base64（无换行）流式写入 dst。
func encodeBase64(dst io.Writer, src io.Reader) error {
	buf := make([]byte, 48<<10) // 3 的倍数，避免跨块补位
	out := make([]byte, base64.StdEncoding.EncodedLen(len(buf)))
	for {
		n, err := src.Read(buf)
		if n > 0 {
			m := base64.StdEncoding.EncodedLen(n)
			base64.StdEncoding.Encode(out[:m], buf[:n])
			if _, werr := dst.Write(out[:m]); werr != nil {
				return werr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
