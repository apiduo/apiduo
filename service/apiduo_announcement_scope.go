package service

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service/authz"
)

// 公告资源（APIDUO:announcement:scope）。
//
// 为什么要单独开一个 resource，而不复用 option:read / option:write：
// agent key 是长期持有的凭证，一个 skill 装上后会一直用同一个 key 跑。
// 给它 option:write 等于把 OAuth 密钥、支付配置、上游价格同步一起交出去；
// 而公告发布只需要碰 announcements 这一个 JSON 数组。
//
// minRole 取 root：公告改动会立刻出现在所有用户的仪表盘上，与系统设置
// 同属全站广播，不是局部操作。这与现有 /api/option 的权限等级一致，
// 没有借新接口放宽任何现有边界。
//
// 用 init 追加而不是改 accessTokenStaticResources 的字面量：那个 slice 在
// 上游文件里，每加一个元素都计入合并面预算，而追加与写进去读路径完全等价。
func init() {
	accessTokenStaticResources = append(accessTokenStaticResources, AccessTokenResource{
		Resource: "announcement",
		LabelKey: "Announcements",
		group:    AccessTokenGroupSystem,
		minRole:  common.RoleRootUser,
		Actions: []authz.ActionDefinition{
			accessTokenView("View announcements, including scheduled and expired ones."),
			accessTokenEdit("Create, edit, and delete announcements."),
		},
	})
}
