# lessonpack

离线课件包导入导出库（Go，无接口层，由上层直接调用）。
课件元数据与按 SHA-256 寻址的音频均存于本地目录，全程流式处理，
单课件 70 MiB 音频、整包 2 GiB 时峰值 Go 堆增量 < 4 MiB（上限 32 MiB）。

## API

```go
store, err := lessonpack.Open(dir)          // 打开/创建课件库
results, err := store.Import(r)             // 从 io.Reader 流式导入，逐条返回结果
err = store.Export(w, ids)                  // 按给定 ID 顺序向 io.Writer 流式导出
```

`Import` 返回 `[]LessonResult`，与包内课件顺序一致：

```go
type LessonResult struct {
    ID        string
    OK        bool
    Reason    FailureReason // ReasonInvalidBase64 / ReasonSHA256Mismatch
    CardIndex int           // 失败卡片在 cards 中的下标
}
```

音频非法 Base64 或摘要不符只拒绝所属课件，其余课件不受影响；
失败课件的元数据与独占音频不会进入正式库，共享音频不受影响。

## 库目录布局

```
<dir>/lessons/<id>.json   课件元数据（音频卡只存 sha256，不存 data）
<dir>/audio/<sha256hex>   音频字节，按摘要寻址，天然去重
<dir>/tmp/                导入暂存；返回前清理（成功件已改名入库）
```

## 设计要点

- `jsonscan.go`：不可回退流上的 JSON 词法器，32 KiB 滑动窗口；
  字符串解释（含 `\uXXXX` 与代理对）直接写入下游，非音频字段保留原始 JSON 字节。
- `base64stream.go`：严格 RFC4648 §4 流式解码——拒绝内嵌空白、
  缺失补位、非零末尾未使用位；解码字节经 64 KiB 缓冲写入临时文件并同步计算 SHA-256。
- `import.go`：字段乱序无关；`data` 到达即流式落暂存文件；
  整条课件校验通过后才按摘要改名入库（已存在则去重），失败则清理暂存。
- `export.go`：音频从库内文件流式重新编码为 Base64，逐卡内联完整数据。

## 测试

```sh
go test ./...                 # 全部测试（含约 250 MiB 大包内存测试）
LESSONPACK_BIG=1 go test -run TestBigPackMemory -v -timeout 900s .
                              # 2 GiB 规模内存验证
```

实测（Apple Silicon）：2 GiB 包导入峰值堆增量 3.7 MiB，导出 3.4 MiB。
