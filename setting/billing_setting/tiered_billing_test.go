package billing_setting

import (
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSmokeTestTaskExprValidatesDeclaredUsageVectors(t *testing.T) {
	videoSchema := map[string]jsplugin.UsageFieldSchema{
		"seconds": {Type: "number", Unit: "second"},
		"mode":    {Enum: []string{"std", "pro"}},
		"quality": {Enum: []string{"sd", "hd"}},
	}

	tests := []struct {
		name          string
		schema        map[string]jsplugin.UsageFieldSchema
		expression    string
		expectedError string
	}{
		{
			name:          "fixed prices are not task usage prices",
			schema:        videoSchema,
			expression:    `true ? tier("normal", u("seconds") * 0.4) : tier("fixed", fixed(0.01))`,
			expectedError: "fixed pricing is not supported for task usage expressions",
		},
		{
			// Tasks have no token counts: the expression would bill 0.
			name:          "token counts are not task usage prices",
			schema:        videoSchema,
			expression:    `tier("base", u("seconds") * 0.4 + (len > 1000 ? c * 8 : 0))`,
			expectedError: "task usage expressions cannot read chat request quantities",
		},
		{
			// image_count is the chat request's image count: a task would read 1.
			name:          "image count is not a task usage price",
			schema:        map[string]jsplugin.UsageFieldSchema{"image_count": {Type: "number", Unit: "count"}},
			expression:    `tier("images", image_count * 0.04)`,
			expectedError: "task usage expressions cannot read chat request quantities",
		},
		{
			name:       "task image counts come from usage facts",
			schema:     map[string]jsplugin.UsageFieldSchema{"image_count": {Type: "number", Unit: "count"}},
			expression: `tier("images", u("image_count") * 0.04)`,
		},
		{
			name:       "declared numeric and enum facts",
			schema:     videoSchema,
			expression: `u("mode") == "pro" ? tier("pro", u("seconds") * 0.8) : tier("std", u("seconds") * 0.4)`,
		},
		{
			name:          "undeclared literal key",
			schema:        videoSchema,
			expression:    `tier("base", u("clips") * 0.1)`,
			expectedError: `usage key "clips" is not declared`,
		},
		{
			name:          "negative duration boundary",
			schema:        videoSchema,
			expression:    fmt.Sprintf(`u("seconds") == %d ? -1 : 0`, relaycommon.MaxTaskDurationSeconds),
			expectedError: "result must be finite and non-negative",
		},
		{
			name:          "negative count boundary",
			schema:        map[string]jsplugin.UsageFieldSchema{"clips": {Type: "number", Unit: "count"}},
			expression:    fmt.Sprintf(`u("clips") == %d ? -1 : 0`, dto.MaxImageN),
			expectedError: "result must be finite and non-negative",
		},
		{
			name:          "negative token boundary",
			schema:        map[string]jsplugin.UsageFieldSchema{"tokens": {Type: "number", Unit: "token"}},
			expression:    fmt.Sprintf(`u("tokens") == %d ? -1 : 0`, common.MaxQuota),
			expectedError: "result must be finite and non-negative",
		},
		{
			name:          "negative credit boundary",
			schema:        map[string]jsplugin.UsageFieldSchema{"units": {Type: "number", Unit: "credit"}},
			expression:    fmt.Sprintf(`u("units") == %d ? -1 : 0`, common.MaxQuota),
			expectedError: "result must be finite and non-negative",
		},
		{
			name:          "negative enum combination",
			schema:        videoSchema,
			expression:    `u("mode") == "pro" && u("quality") == "hd" ? -1 : 0`,
			expectedError: "result must be finite and non-negative",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			err := SmokeTestTaskExpr(testCase.expression, testCase.schema)
			if testCase.expectedError == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, testCase.expectedError)
		})
	}
}

func TestSmokeTestTaskExprCapsOversizedEnumProductsAtLastCombination(t *testing.T) {
	schema := make(map[string]jsplugin.UsageFieldSchema, 7)
	condition := ""
	for index := range 7 {
		schema[fmt.Sprintf("enum_%d", index)] = jsplugin.UsageFieldSchema{Enum: []string{"first", "middle", "last"}}
		if condition != "" {
			condition += " && "
		}
		condition += fmt.Sprintf(`u("enum_%d") == "last"`, index)
	}

	err := SmokeTestTaskExpr(condition+" ? -1 : 0", schema)
	require.ErrorContains(t, err, "result must be finite and non-negative")
}

func TestSmokeTestExprRejectsTaskUsageWithoutSchema(t *testing.T) {
	err := SmokeTestExpr(`u("mode") == "std" ? 1 : 2`)
	require.Error(t, err)
	assert.ErrorContains(t, err, "mode")
	assert.ErrorContains(t, err, "no task plugin usage schema")

	require.NoError(t, SmokeTestExpr(`tier("base", p * 2 + c * 8)`))
}

// APIDUO:task-usage-facts:BEGIN
// 内置事实表是包级变量，借出去必须给副本，否则调用方一次 delete 就能让
// 之后所有保存校验失去这一项。
func TestAdvancedAsyncUsageSchemaReturnsIndependentCopy(t *testing.T) {
	schema := AdvancedAsyncUsageSchema()
	require.Contains(t, schema, "seconds")

	delete(schema, "seconds")
	schema["injected"] = jsplugin.UsageFieldSchema{Unit: "count"}

	fresh := AdvancedAsyncUsageSchema()
	require.Contains(t, fresh, "seconds", "删除副本的键不能影响内置表")
	require.NotContains(t, fresh, "injected", "副本上的新增不能回流内置表")
	require.True(t, IsAdvancedAsyncUsageKey("factor"))
	require.False(t, IsAdvancedAsyncUsageKey("injected"))
}

// A token price without a non-token tier settles at zero whenever the vendor
// omits usage. The save path has to refuse it, or the hole is invisible.
func TestTaskTokenPriceNeedsFallback(t *testing.T) {
	accepted := []string{
		`u("tokens") > 0 ? tier("token", u("tokens") * 10.0 / 1000000) : tier("call", u("count") * 0.20)`,
		`u("tokens") > 0 ? tier("token", u("tokens") * 10.0 / 1000000) : tier("second", u("seconds") * u("factor") * 0.02)`,
		`tier("base", u("seconds") * u("factor") * 0.01)`,
	}
	for _, expression := range accepted {
		require.NoError(t, TaskTokenPriceNeedsFallback(expression), expression)
	}

	rejected := []string{
		`tier("base", u("tokens") * 10.0 / 1000000)`,
		`u("tokens") > 0 ? tier("token", u("tokens") * 10.0 / 1000000) : tier("free", u("tokens") * 0)`,
	}
	for _, expression := range rejected {
		err := TaskTokenPriceNeedsFallback(expression)
		require.Error(t, err, expression)
		require.Contains(t, err.Error(), "charge something", expression)
	}
}

// APIDUO:task-usage-facts:END
