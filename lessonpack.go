// Package lessonpack 提供离线课件包的流式导入与导出。
// 课件元数据与按 SHA-256 寻址的音频均存于本地目录。
package lessonpack

import "os"

// FailureReason 区分课件导入失败的原因。
type FailureReason int

const (
	// ReasonInvalidBase64 表示音频 data 字段不是合法的 RFC4648 第4节 Base64。
	ReasonInvalidBase64 FailureReason = iota + 1
	// ReasonSHA256Mismatch 表示音频解码字节与声明的 sha256 不符。
	ReasonSHA256Mismatch
)

// LessonResult 记录一条课件的导入结果。
type LessonResult struct {
	ID        string
	OK        bool
	Reason    FailureReason // 仅 OK 为 false 时有效
	CardIndex int           // 仅 OK 为 false 时有效，失败卡片在 cards 中的下标
}

// Store 是课件库，位于本地目录 dir 下。
type Store struct {
	dir string
}

// Open 打开（必要时创建）位于 dir 的课件库。
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}
