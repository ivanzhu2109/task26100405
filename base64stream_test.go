package lessonpack

import (
	"bytes"
	"strings"
	"testing"
)

func decodeAll(t *testing.T, s string) ([]byte, error) {
	t.Helper()
	var buf bytes.Buffer
	d := newB64Decoder(&buf)
	if _, err := d.Write([]byte(s)); err != nil {
		return nil, err
	}
	if err := d.close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func TestB64DecoderStrict(t *testing.T) {
	// 合法输入（含两种补位与空输入）。
	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{"QQ==", "A"},
		{"QUI=", "AB"},
		{"QUJD", "ABC"},
		{"QUJDRA==", "ABCD"},
	} {
		got, err := decodeAll(t, tc.in)
		if err != nil {
			t.Fatalf("decode %q: %v", tc.in, err)
		}
		if string(got) != tc.want {
			t.Fatalf("decode %q = %q, want %q", tc.in, got, tc.want)
		}
	}
	// 非法输入：内嵌空白、缺补位、非零末尾位、补位后还有内容、表外字符。
	for _, in := range []string{
		"QQ =",     // 内嵌空白
		"QQ==\n",   // 换行
		"QUE",      // 缺少必要补位
		"QR==",     // 末尾未使用位非零
		"QUF=",     // 末尾未使用位非零
		"QQ==QQ==", // 补位后还有内容
		"QUJD=",    // 长度非法
		"Q*==",     // 表外字符
		"====",     // 纯补位
	} {
		if got, err := decodeAll(t, in); err == nil {
			t.Fatalf("decode %q should fail, got %q", in, got)
		}
	}
}

func TestEncodeBase64Stream(t *testing.T) {
	data := []byte(strings.Repeat("hello world ", 10000))
	var buf bytes.Buffer
	if err := encodeBase64(&buf, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	got, err := decodeAll(t, buf.String())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("stream base64 round trip mismatch")
	}
}
