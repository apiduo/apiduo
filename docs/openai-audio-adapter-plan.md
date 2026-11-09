# 语音接口适配方案

> 目标：三类入口（speech / transcriptions / translations）全做，覆盖 OpenAI 兼容与国内厂商。
> 前提：补通用能力层，而不是逐家硬编码。
> 配套原型：`docs/audio-prototype.html`（四层架构图 + 配置面板可交互原型 + 请求转换 demo）

---

## 0. 一句话结论

照 `advancedcustom` 已有的 image 三层范式加一套 audio 协议层，**但必须先把 STT 的按秒计量补上**——
否则每多接一家上游，就多一条白送的通道。

---

## 1. 已核实的事实

| 项 | 现状 | 位置 |
|---|---|---|
| 三个入口 | 都已注册，统一走 `AudioHelper` | `router/relay-router.go:143-152` |
| 渠道实现 | 35 个 adaptor 里 **29 个**首行 `return nil, errors.New("not implemented")` | `relay/channel/*/adaptor.go` |
| 真能用 | `openai`（全形态）、`siliconflow`（委托 openai）、`minimax`（仅 TTS）、`volcengine`（仅 TTS） | 同上 |
| apiduo 介入 | **零**。audio 相关无任何 `APIDUO:*` 标记块 | `grep APIDUO relay/ router/` |
| STT 载体 | multipart 原样透传，无结构化 DTO | `openai/adaptor.go:462-500` |
| 能力声明 | 无。只能运行时撞 `not implemented` | — |
| 时长计量 | **无**。`dto.Usage` 没有 duration/seconds 字段 | `relaykit/dto/openai_response.go:228-250` |
| 计费分支 | 靠 details 猜，而 tag 单复数不匹配 ⇒ 恒走文本价 | `relay/audio_handler.go:71-75` |

---

## 2. 第一性原理：语音不是一个接口类，是三个正交维度

```
方向     文本→音频 (TTS)        音频→文本 (STT)
         │                      │
载体     JSON body              multipart(file 二进制)
         │                      │
计量     字符 / token            **秒**
```

三个维度各自独立，且**每个维度都决定一件不同的事**：

- **方向** 决定响应形态：TTS 返回二进制流（`DoResponse` 直接写 `c.Writer`），STT 返回 JSON。
  → 渠道选择器必须按方向判断，不能笼统说“这个渠道支持语音”。
- **载体** 决定 canonical 能不能是纯 map：image 能，audio 不能（见 §3）。
- **计量** 决定计费字段：这是现有代码唯一算错的地方，也是最贵的错。

国内厂商在这三个维度上**全部错位**：TTS 和 ASR 是两套独立端点、两套独立签名、两套独立响应
（火山 TTS `/api/v1/tts` + ASR `/api/v1/asr`；阿里 DashScope 的 speech-synthesis 与
paraformer 分属不同 path 和不同的鉴权头）。所以配置必须**按方向分别声明**，一个 `upstream`
字段包打天下是错的。

---

## 3. 为什么不能照抄 image 的 canonical

image 的 canonical 是 `map[string]any` + 纯函数折叠（`apiduo_image_canonical.go`）。
audio 直接照抄会死在三处：

1. **STT 的 body 是二进制。** multipart 的 file part 是 10–25MB 音频字节。塞进
   `map[string]any` 就得 base64，内存翻 1.33 倍；而 `common.GetBodyStorage` 可能已经落盘
   （`common/gin.go:130` 有 disk-backed 分支），再解一遍就是二次全量拷贝。
2. **STT 需要跨格式重编码。** 火山 ASR 收 pcm/wav，OpenAI 收 mp3/m4a/webm。
   canonical 必须保留**原始字节 + mime + filename**，让脚本层决定要不要转码——
   而 image 层的 `resolveImageRef` 是“下载后内联”，方向相反。
3. **duration 是必填计量。** image 没有这个需求，audio 的 STT 全靠它。

结论：audio canonical 是 `fields map[string]any + blob *audioBlob` 的**混合体**，
TTS 走 fields，STT 走 blob。折叠层仍然保持纯函数（不做 IO、不判断厂商）。

---

## 4. 方案：四层

### L0 计量层（必须先做，独立 PR）

计费链路上有两处断裂，都是实证结论不是猜测。

#### 断裂点一：时长计量缺失

现在 STT 计费是 0：

- `PrepareRequestBilling`（`relay/request_billing.go:28-45`）只在 `info.Request.GetTokenCountMeta()`
  时取文本；`AudioRequest.GetTokenCountMeta()` 返回 `CombineText: r.Input`（`relaykit/dto/audio.go:32-41`）。
- STT 请求的 `Input` 是空串 ⇒ 估算 token = 0。
- `OpenaiSTTHandler`（`relay/channel/openai/audio.go:127-147`）：上游不返回 `usage.total_tokens`
  时**回落**到 `info.GetEstimatePromptTokens()` ⇒ 0。
- whisper-1 的 `verbose_json` 只返回 `duration`，不返回 `usage`；而
  `WhisperVerboseJSONResponse.Duration` 定义了却没人读（`relaykit/dto/audio.go:56`）。

**净效果：whisper-1 在 apiduo 上完全免费。**

#### 断裂点二：计费分支判错（更隐蔽）

`AudioHelper`（`relay/audio_handler.go:71-75`）靠 details 里有没有音频 token 来决定走哪套计价：

```go
if usage.CompletionTokenDetails.AudioTokens > 0 || usage.PromptTokensDetails.AudioTokens > 0 {
    service.PostAudioConsumeQuota(...)   // 音频倍率
} else {
    service.PostTextConsumeQuota(...)    // 文本倍率
}
```

但 `dto.Usage.InputTokensDetails` 的 JSON tag 是 `input_tokens_details`（复数，
`relaykit/dto/openai_response.go:241`），而 OpenAI 语音接口返回的是 `input_token_details`
（单数 token）。**差一个 `s`** ⇒ `AudioTokens` 永远反序列化成 0
⇒ gpt-4o-transcribe 的音频输入也按文本价结算，`audio_ratio` 配置形同虚设。

修法：不要靠 details 猜。`info.RelayMode` 已经是 `RelayModeAudioSpeech /
RelayModeAudioTranscription / RelayModeAudioTranslation` 三选一（`relay/constant/relay_mode.go:34-36`），
直接按它分派。

### 修法（三选一，推荐 A）：

**A. 时长挂在 RelayInfo 上**（影响面最小，不动 `dto.Usage` 的 merge 面）
```go
// relay/common/relay_info.go — 用 APIDUO:audio-duration 标记块
func (info *RelayInfo) SetAudioSeconds(v float64)
func (info *RelayInfo) GetAudioSeconds() float64
```
`PostAudioConsumeQuota(ctx, info, usage, extra)` 同时握有 info 和 usage
（`service/quota.go:301`），读 info 即可，不必改 usage 结构。

**B. `dto.Usage` 加字段** —— 改动会进 relaykit 的 merge 面，且 `dto.Usage` 被全仓 200+ 处引用。

**C. 转成伪 token 计费** —— 要改动 ratio 体系语义，一致性和可解释性都变差。否。

duration 的三级来源（优先级从高到低）：
1. 上游响应里的 `duration`（whisper verbose_json / 火山 ASR `result.duration` / 阿里 `audio_duration`）
2. 本地容器头解析（wav 按 header 算、mp3 按帧/Xing、m4a 按 mvhd）——纯 Go，无外部依赖
3. 按字节数 + 编码码率给保守下限（宁可少收，不虚报）

### L1 协议配置层

新文件 `relaykit/dto/apiduo_audio_protocol.go`（新文件不进 merge 面）：

```go
const (
    AudioUpstreamOpenAI     = "openai"      // 含所有 OpenAI 兼容
    AudioUpstreamVolcengine = "volcengine"
    AudioUpstreamDashscope  = "dashscope"
    AudioUpstreamTencent    = "tencent"
    AudioUpstreamMinimax    = "minimax"
)

type AudioProtocolConfig struct {
    Enabled bool `json:"enabled,omitempty"`
    Upstream string `json:"upstream,omitempty"`
    // 按方向分别声明：国内厂商 TTS 与 ASR 是两套独立端点与签名
    Speech        *AudioEndpoint `json:"speech,omitempty"`         // 文本→音频
    Transcription *AudioEndpoint `json:"transcription,omitempty"`  // 音频→文本
    Translation   *AudioEndpoint `json:"translation,omitempty"`    // 音频→英文文本
}

type AudioEndpoint struct {
    Path    string `json:"path,omitempty"`     // 空则按 upstream 默认
    // 载体：json | multipart。空则按 upstream 默认。
    Body    string `json:"body,omitempty"`
    // 鉴权：header | query | volc_hmac | tencent_tc3。空则按 upstream 默认。
    Auth    string `json:"auth,omitempty"`
}
```

关键设计：**没有 `allow_*` 开关**。image 层已经吃过这个教训（`apiduo_image_protocol.go:31-36` 的注释）——
开关等于“把功能关掉”，而正确做法是在实现层做强校验。

### L2 canonical 层

新文件 `relay/channel/advancedcustom/apiduo_audio_canonical.go`：

```go
type audioCanonical struct {
    fields map[string]any   // 非文件字段，无损透传（含厂商 extras）
    blob   *audioBlob       // nil = TTS 请求
}

type audioBlob struct {
    Data     []byte // 原始字节，不 base64，不转码
    Filename string
    MimeType string
}
```

三个折叠入口：

| 入站 | 折叠函数 | 说明 |
|---|---|---|
| `/v1/audio/speech` | `canonicalFromSpeechJSON` | JSON → fields，blob=nil |
| `/v1/audio/transcriptions` | `canonicalFromAudioForm` | multipart → fields + blob；`file` 取首个，其余字段全留 |
| `/v1/audio/translations` | 同上 | 与转录同构，仅语义不同 |

必须保留的坑点：
- `timestamp_granularities[]` 这类**带方括号的重复键**不能用 `map[string]any` 收，
  `ParseMultipartFormReusable` 的 `Value` 是 `map[string][]string`，要按 `[]string` 存。
- 空 file part、多 file part 要显式报错，不能静默取第一个。

### L3 上游脚本层

新文件 `relay/channel/advancedcustom/apiduo_audio_script.go`，对齐 image 的 `planImageRequest` 形态：

```go
type audioPlan struct {
    Path     string
    Body     []byte
    MimeType string
}

func planAudioRequest(c *audioCanonical, mode int, cfg *dto.AudioProtocolConfig) (*audioPlan, error)
```

按 `(mode, cfg.Upstream)` 分派：`planSpeechOpenAI` / `planSpeechVolcengine` / `planSTTVolcengine` …
一个上游一个函数，**不许在一个函数里堆 switch 厂商分支**。

---

## 5. 能力声明（把“支不支持”前移到选渠道）

`model/channel_constraint.go:113` 现在是：
```go
if ch.GetOtherSettings().ImageProtocol.ServesPath(filter.RequestPath) {
    return true
}
```

旁边加同构的一句，但语义要收紧——**audio 必须按方向判断**：

```go
// APIDUO:audio-protocol:BEGIN
if ch.GetOtherSettings().AudioProtocol.ServesPath(filter.RequestPath) {
    return true
}
// APIDUO:audio-protocol:END
```

```go
// ServesPath 只在该方向的端点真的配了（或 upstream 默认有）时返回 true。
// 启用了 TTS 不等于能接转录——这是 audio 和 image 的语义差别。
func (c *AudioProtocolConfig) ServesPath(path string) bool {
    if c == nil || !c.Enabled { return false }
    switch normalize(path) {
    case "/v1/audio/speech":        return c.Speech != nil
    case "/v1/audio/transcriptions": return c.Transcription != nil
    case "/v1/audio/translations":   return c.Translation != nil
    }
    return false
}
```

不做这一步的后果：客户端拿到 503 而协议层其实能处理（image 已经踩过一次）。

---

## 6. 实施顺序

顺序按**风险**排，不按功能排。

| PR | 内容 | 为什么在这个位置 |
|---|---|---|
| **P1** | L0 计量：`RelayInfo.AudioSeconds` + 三级 duration 来源 + `PostAudioConsumeQuota` 按秒结算 + 计费分支改按 `RelayMode` 分派 | 不修就先别接。接得越多漏得越多，且已发出的量收不回来 |
| **P2** | L1 配置 + L2 canonical + L2 单测（纯函数，无网络无厂商） | 结构地基，可以独立合入且零行为变化（`Enabled=false` 时一字不差） |
| **P3** | L3 openai 脚本 + 三类入口打通 + 能力声明挂钩 | OpenAI 兼容面最广（siliconflow / deepseek / moonshot 这类只需改 base_url） |
| **P4** | L3 国内厂商：先火山（已有 TTS 可复用），再阿里，再腾讯 | 每家一套签名，拆成独立 PR，一家一个失败面 |

**P1 的具体风险点**：`PostAudioConsumeQuota` 现在按 token 结算，改成按秒后，
存量模型的价格配置（`ratio_setting.defaultAudioRatio`）语义会变。
必须保证：**没有配按秒价的模型，回落到现有 token 计价**，不改存量行为。

---

## 7. 明确不做

| 不做 | 理由 |
|---|---|
| 把 STT 塞进 `/v1/chat/completions` | 语义污染。image 层的 `canonicalFromChat` 是特例（出图本身是单轮），STT 不是 |
| `/v1/audio/translations` 全量适配 | 上游支持面极窄（OpenAI 只有 whisper-1，国内厂商基本没有）。只做 openai 兼容透传，其余**明确 400 + 可读原因**，不假装支持 |
| `/v1/realtime` WS | 独立链路（`relay/common/relay_info.go` 的 `RelayFormatOpenAIRealtime` 走 `wsRouter`），本次不碰 |
| 为 audio 新增渠道类型 | `advancedcustom` 的 converter + route 机制已经够用，新增类型等于多一套运维面 |
| 音频转码（ffmpeg） | 引入外部二进制依赖，且容器镜像体积敏感。格式不匹配就在脚本层报错，让运维换上游或换输入 |

---

## 8. 验收

- `go run ./tools/markercheck` 绿；`-cost` 后 **files / blocks / added 只降不涨**
  （当前 97 / 286 / 2698，新增 L0 的两个标记块会计入 blocks，需在别处抵掉或显式说明）
- `web/scripts/format-with-protected-headers.mjs --check` **不跑**（会删 AGPL 头）
- 单测覆盖：
  - canonical 折叠：TTS JSON 全字段透传、STT 单/多/空 file、`timestamp_granularities[]` 保序
  - 脚本 plan：表驱动，一 upstream 一 mode 一用例
  - 计量：上游返 duration / 本地解析 / 字节回落 三条路径各一例；无按秒价时回落 token 计价
- 端到端：mock 上游回放，断言扣费金额 = 时长 × 按秒单价

---

## 9. 待你拍板的两件事

1. **按秒计价的配置面放哪？** 现有模型价格体系是 `ModelRatio`/`ModelPrice`（倍率/固定价）。
   按秒价是第三种语义。选择：(a) 新增 `audio_price_per_second` 表；(b) 复用 `ModelPrice` 但单位变成秒；
   (c) 折算成“每秒 = N 个伪 token”。我倾向 (a)——语义清晰，不影响存量。
2. **P4 先做哪家？** 火山已有 TTS 实现可复用，代价最低；阿里支持面最全（TTS+ASR+翻译都有）。
