# lessonpack

离线课件包导入导出库（Go，纯本地文件存储，无外部服务）。

## 用法

```go
lib, _ := lessonpack.Open("/path/to/library") // 打开/创建课件库
res, _ := lib.Import(r)                       // 从字节流导入自包含 JSON 包
_ = lib.Export(w, []string{"id1", "id2"})     // 按 id 序列导出到字节流
```

- `Import` 返回 `*ImportResult`：`Imported` 为成功课件 id（按包内顺序），
  `Failures` 区分 `FailureInvalidBase64` 与 `FailureHashMismatch`，
  并定位到课件（下标与 id）及卡片下标。单条课件失败不影响其他课件。
- `Export` 输出同格式自包含包，顺序与给定 id 序列一致；
  每张音频卡片都携带完整 Base64 内容。

## 存储布局

```
root/meta/<hex(id)>.json   课件元数据（音频卡片不含 data）
root/audio/<sha256hex>     按 SHA-256 寻址、去重后的音频
root/tmp/                  导入临时文件，操作返回前清理
```

## 流式与内存

输入输出均为不可回退字节流；音频 JSON 字符串经流式 unescape、
严格 RFC4648 第4节 Base64 解码（拒绝内嵌空白、缺少补位、非零末尾位），
边解码边算 SHA-256 落临时文件，单条课件全部校验通过才提交进正式库。
单课件 70MiB 音频、整包 2GiB 时导入/导出峰值 Go 堆占用均远低于 32MiB
（实测约 3.6MiB / 2.0MiB，见 `big_test.go`）。

## 测试

```sh
go test -short ./...   # 快速：往返、失败场景、严格 Base64
go test ./...          # 完整：含 2GiB 大包与峰值内存测试
```
