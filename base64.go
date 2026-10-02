package lessonpack

import (
	"errors"
	"io"
)

// ErrInvalidBase64 表示音频 data 不是合法的 RFC4648 第4节 Base64。
var ErrInvalidBase64 = errors.New("invalid base64 data")

// b64Decoder 是严格的 RFC4648 第4节 Base64 流式解码器：
// 不接受内嵌空白、缺少等号补位或非零的末尾未使用位。
type b64Decoder struct {
	r       io.Reader
	scratch [4096]byte // 未消费的上游字节
	sn, sp  int
	quad    [4]byte // 当前四字组中已解码的 6bit 值
	qn      int     // quad 中已收集的字符数
	pad     int     // 当前四字组末尾的 '=' 数
	out     [3]byte // 已解码待吐出
	on, op  int
	done    bool // 已见到带补位的末尾四字组
	eof     bool // 上游已 EOF
}

// newB64Decoder 包装 r，Read 产出解码后的字节；任何违规以 ErrInvalidBase64 报告。
func newB64Decoder(r io.Reader) io.Reader {
	return &b64Decoder{r: r}
}

var b64rev [256]byte

func init() {
	for i := range b64rev {
		b64rev[i] = 0xFF
	}
	const alpha = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	for i := 0; i < len(alpha); i++ {
		b64rev[alpha[i]] = byte(i)
	}
}

func (d *b64Decoder) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		if d.on > 0 {
			m := copy(p[n:], d.out[d.op:d.op+d.on])
			d.op += m
			d.on -= m
			if d.on == 0 {
				d.op = 0
			}
			n += m
			continue
		}
		if err := d.fillQuad(); err != nil {
			if err == io.EOF && n > 0 {
				return n, nil
			}
			return n, err
		}
	}
	return n, nil
}

// fillQuad 收集一个四字组并解码到 out；流结束时返回 io.EOF。
func (d *b64Decoder) fillQuad() error {
	for d.qn < 4 {
		c, err := d.readByte()
		if err != nil {
			if err == io.EOF {
				if d.qn != 0 {
					// 长度不是 4 的倍数：缺少必要等号补位
					return ErrInvalidBase64
				}
				return io.EOF
			}
			return err
		}
		if c == '=' {
			if d.qn < 2 {
				return ErrInvalidBase64
			}
			d.pad++
			d.qn++
			continue
		}
		if d.pad > 0 {
			// '=' 只能出现在末尾
			return ErrInvalidBase64
		}
		v := b64rev[c]
		if v == 0xFF {
			// 包括空白在内的任何非字母表字符
			return ErrInvalidBase64
		}
		d.quad[d.qn] = v
		d.qn++
	}
	if d.done {
		// 末尾四字组之后还有内容
		return ErrInvalidBase64
	}
	switch d.pad {
	case 0:
		d.out[0] = d.quad[0]<<2 | d.quad[1]>>4
		d.out[1] = d.quad[1]<<4 | d.quad[2]>>2
		d.out[2] = d.quad[2]<<6 | d.quad[3]
		d.on = 3
	case 1:
		if d.qn != 4 || d.quad[2]&0x03 != 0 {
			// 末尾未使用位必须为零
			return ErrInvalidBase64
		}
		d.out[0] = d.quad[0]<<2 | d.quad[1]>>4
		d.out[1] = d.quad[1]<<4 | d.quad[2]>>2
		d.on = 2
		d.done = true
	case 2:
		if d.quad[1]&0x0F != 0 {
			return ErrInvalidBase64
		}
		d.out[0] = d.quad[0]<<2 | d.quad[1]>>4
		d.on = 1
		d.done = true
	default:
		return ErrInvalidBase64
	}
	d.qn = 0
	d.pad = 0
	return nil
}

func (d *b64Decoder) readByte() (byte, error) {
	if d.sp >= d.sn {
		if d.eof {
			return 0, io.EOF
		}
		n, err := d.r.Read(d.scratch[:])
		if n > 0 {
			d.sn = n
			d.sp = 0
		}
		if err != nil {
			if err == io.EOF {
				d.eof = true
				if d.sn > 0 {
					c := d.scratch[d.sp]
					d.sp++
					return c, nil
				}
			}
			return 0, err
		}
		if n == 0 {
			return 0, io.ErrNoProgress
		}
	}
	c := d.scratch[d.sp]
	d.sp++
	return c, nil
}
