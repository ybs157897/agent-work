package domain

import "testing"

// TestMessageDeltaChannelPinsWireContract 钉住 raw.chunk.type 通道判定：
// 只有 reasoning-delta 归推理通道，其余线形一律答案通道——兜底方向不能反，
// 否则中断 run 的推理链会重新冒充助手正文。
func TestMessageDeltaChannelPinsWireContract(t *testing.T) {
	cases := []struct {
		name    string
		payload map[string]any
		want    DeltaChannel
	}{
		{
			name:    "kimi/dsh/codex 推理帧",
			payload: map[string]any{"raw": map[string]any{"chunk": map[string]any{"type": "reasoning-delta", "text": "想"}}},
			want:    DeltaChannelReasoning,
		},
		{
			name:    "答案帧",
			payload: map[string]any{"raw": map[string]any{"chunk": map[string]any{"type": "text-delta", "text": "答"}}},
			want:    DeltaChannelAnswer,
		},
		{
			name:    "raw 为 JSON 文本的推理帧（Codex 旧版本线形）",
			payload: map[string]any{"raw": "{\"chunk\":{\"type\":\"reasoning-delta\",\"text\":\"想\"}}"},
			want:    DeltaChannelReasoning,
		},
		{
			name:    "未知 chunk type",
			payload: map[string]any{"raw": map[string]any{"chunk": map[string]any{"type": "usage-delta"}}},
			want:    DeltaChannelAnswer,
		},
		{
			name:    "缺 raw.chunk",
			payload: map[string]any{"raw": map[string]any{"delta": "答"}},
			want:    DeltaChannelAnswer,
		},
		{
			name:    "扁平 text 载荷（mock/scripted）",
			payload: map[string]any{"role": "assistant", "text": "答"},
			want:    DeltaChannelAnswer,
		},
		{
			name:    "raw 非法 JSON 文本",
			payload: map[string]any{"raw": "not json"},
			want:    DeltaChannelAnswer,
		},
		{
			name:    "空载荷",
			payload: map[string]any{},
			want:    DeltaChannelAnswer,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MessageDeltaChannel(tc.payload); got != tc.want {
				t.Fatalf("MessageDeltaChannel = %q, want %q", got, tc.want)
			}
		})
	}
}
