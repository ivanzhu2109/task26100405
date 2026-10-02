package lessonpack

// FailureReason 区分课件被拒绝的两类音频原因。
type FailureReason int

const (
	// FailureInvalidBase64 音频 data 不是合法的 RFC4648 第4节 Base64。
	FailureInvalidBase64 FailureReason = iota + 1
	// FailureHashMismatch 音频解码字节与 sha256 摘要不符。
	FailureHashMismatch
)

// LessonFailure 定位一条被拒绝的课件及其出错卡片。
type LessonFailure struct {
	LessonIndex int // 在包内 lessons 数组中的下标
	LessonID    string
	CardIndex   int // 在课件 cards 数组中的下标
	Reason      FailureReason
}

// ImportResult 记录每条课件的成功或失败。
type ImportResult struct {
	Imported []string // 成功课件的 id，按包内顺序
	Failures []LessonFailure
}
