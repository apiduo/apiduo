package mcp

import (
	"encoding/json"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 能力注册声明的 parameters 是平铺的（aspect_ratio / resolution…），工具入参
// 结构体只有固定几个字段。SDK 默认给 struct 生成 additionalProperties: false，
// agent 照 models.list 传参就会被 schema 打回 —— 线上表现是「调用成功、日志没有」。
func TestGenerateInputSchemaAllowsExtraProperties(t *testing.T) {
	schema := generateInputSchema()
	require.NotNil(t, schema, "schema must be built from generateInput")
	require.NotNil(t, schema.AdditionalProperties, "additional properties must not be rejected")
	assert.Empty(t, schema.AdditionalProperties.Not, "an empty sub-schema accepts any value")

	// 声明的字段仍然在，否则 agent 不知道 prompt / model 怎么传。
	for _, field := range []string{"prompt", "model", "size", "n", "quality", "extra"} {
		assert.Contains(t, schema.Properties, field, "declared property %q disappeared", field)
	}
}

func TestAbsorbUndeclaredParamsMovesFlatParamsIntoExtra(t *testing.T) {
	raw := json.RawMessage(`{
		"prompt": "a cat",
		"model": "flux-3-image",
		"aspect_ratio": "16:9",
		"resolution": "1k",
		"extra": {"quality": "hd"}
	}`)
	in := generateInput{}
	// SDK 会先把已知字段解进结构体；这里复现那一步，才能验证「显式 extra 优先」。
	require.NoError(t, json.Unmarshal(raw, &in))
	absorbUndeclaredParams(&mcpsdk.CallToolRequest{Params: &mcpsdk.CallToolParamsRaw{Arguments: raw}}, &in)

	require.NotNil(t, in.Extra)
	assert.Equal(t, "16:9", in.Extra["aspect_ratio"])
	assert.Equal(t, "1k", in.Extra["resolution"])
	// 显式 extra 里的键优先，不能被同名平铺参数覆盖。
	assert.Equal(t, "hd", in.Extra["quality"])
	// 结构体已声明的键不该被搬进 Extra，否则会以字符串形态覆盖解析好的字段。
	assert.NotContains(t, in.Extra, "prompt")
	assert.NotContains(t, in.Extra, "model")
}

func TestAbsorbUndeclaredParamsIgnoresMissingRequest(t *testing.T) {
	in := generateInput{}
	assert.NotPanics(t, func() { absorbUndeclaredParams(nil, &in) })
	assert.Nil(t, in.Extra)
}
