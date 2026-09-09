package domain

import "encoding/json"

// message.delta 的 raw.chunk 线形是 canonical 契约（contracts/events/asyncapi.yaml
// MessageDeltaData；前端镜像 web/src/stores/delta-chunk.ts）：type 区分助手文本通道。
const (
	// DeltaChunkTypeText 答案通道：助手正文，可进最终文本与对话回放。
	DeltaChunkTypeText = "text-delta"
	// DeltaChunkTypeReasoning 推理通道：过程推理，只服务实时流。
	DeltaChunkTypeReasoning = "reasoning-delta"
)

// DeltaChannel 助手文本通道。
type DeltaChannel string

const (
	DeltaChannelAnswer    DeltaChannel = "answer"
	DeltaChannelReasoning DeltaChannel = "reasoning"
)

// MessageDeltaChannel 判定 message.delta 载荷的助手文本通道。只认 raw.chunk.type
// 一条契约；无法识别（缺 raw.chunk、扁平 text 载荷、未来新增 chunk type）一律归
// 答案通道——提取类消费方宁可多收正文，也绝不能把过程推理当成助手回答。
func MessageDeltaChannel(payload map[string]any) DeltaChannel {
	if chunkType(payload["raw"]) == DeltaChunkTypeReasoning {
		return DeltaChannelReasoning
	}
	return DeltaChannelAnswer
}

// chunkType 兼容 raw 的两种线形：对象（kimi/dsh/codex 现行）与 JSON 文本
// （Codex app-server 旧版本把 params 序列化成字符串）。
func chunkType(raw any) string {
	switch value := raw.(type) {
	case map[string]any:
		chunk, _ := value["chunk"].(map[string]any)
		text, _ := chunk["type"].(string)
		return text
	case string:
		var decoded map[string]any
		if json.Unmarshal([]byte(value), &decoded) != nil {
			return ""
		}
		return chunkType(decoded)
	}
	return ""
}
