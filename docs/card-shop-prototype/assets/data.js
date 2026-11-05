/* 原型假数据。真实实现里这些来自 GET /api/card/goods 等接口。 */

const GOODS = [
  { id: 1, name: 'ChatGPT Plus', spec: '月卡 · 自动发货', price: 172, cost: 120, h: 150, tag: '热销', stock: '充足', stockNum: 42, cat: 'AI 会员', warranty: 168, delivery: 'auto' },
  { id: 2, name: 'Claude Pro', spec: '月卡 · 自动发货', price: 188, cost: 132, h: 38, tag: '', stock: '充足', stockNum: 28, cat: 'AI 会员', warranty: 168, delivery: 'auto' },
  { id: 3, name: 'Midjourney', spec: '基础版 · 月卡', price: 112, cost: 78, h: 280, tag: '', stock: '仅剩 3 张', stockNum: 3, cat: 'AI 工具', warranty: 168, delivery: 'auto' },
  { id: 4, name: 'Cursor Pro', spec: '月卡 · 自动发货', price: 198, cost: 140, h: 250, tag: '', stock: '充足', stockNum: 19, cat: 'AI 工具', warranty: 168, delivery: 'auto' },
  { id: 5, name: 'Gemini Advanced', spec: '月卡 · 自动发货', price: 205, cost: 145, h: 220, tag: '', stock: '充足', stockNum: 15, cat: 'AI 会员', warranty: 168, delivery: 'auto' },
  { id: 6, name: 'ChatGPT 独享号', spec: '永久 · 人工发货', price: 168, cost: 62, h: 20, tag: '限购 1', stock: '仅剩 2 张', stockNum: 2, cat: '成品号', warranty: 72, delivery: 'manual' },
  { id: 7, name: '谷歌邮箱号', spec: '单个 · 自动发货', price: 12, cost: 3, h: 200, tag: '', stock: '充足', stockNum: 310, cat: '成品号', warranty: 24, delivery: 'auto' },
  { id: 8, name: 'Replicate 额度', spec: '$10 面值 · 固定内容', price: 75, cost: 66, h: 305, tag: '', stock: '售罄', stockNum: 0, cat: '算力额度', warranty: 0, delivery: 'fixed' },
];

const CATS = ['全部商品', 'AI 会员', 'AI 工具', '成品号', '算力额度', '资料'];

/* 库存展示规则：永不暴露精确数量，只在 <=5 时提示紧张 */
function stockLabel(g) {
  if (g.stockNum === 0) return { text: '售罄', cls: 'out' };
  if (g.stockNum <= 5) return { text: `仅剩 ${g.stockNum} 张`, cls: 'low' };
  return { text: '充足', cls: '' };
}

/* 后台订单假数据 */
const ORDERS = [
  { no: 'CARD1001NO17628501', user: 'u_10231', goods: 'ChatGPT Plus 月卡', qty: 1, amount: 172, pay: '支付宝', status: '已发货', time: '2026-11-05 15:42', ch: '官方直营' },
  { no: 'CARD1001NO17628304', user: 'u_10088', goods: 'Claude Pro 月卡', qty: 1, amount: 188, pay: '微信支付', status: '已发货', time: '2026-11-05 14:17', ch: '官方直营' },
  { no: 'CARD1002NO17628118', user: 'u_10032', goods: 'ChatGPT 独享号', qty: 1, amount: 168, pay: '支付宝', status: '待审核', time: '2026-11-05 13:02', ch: '代理-A' },
  { no: 'CARD1007NO17627990', user: 'u_10120', goods: '谷歌邮箱号', qty: 3, amount: 36, pay: '支付宝', status: '已发货', time: '2026-11-05 11:48', ch: '官方直营' },
  { no: 'CARD1001NO17627842', user: 'u_10001', goods: 'Cursor Pro 月卡', qty: 1, amount: 198, pay: '微信支付', status: '售后中', time: '2026-11-05 10:31', ch: '代理-A' },
  { no: 'CARD1009NO17627665', user: 'u_10187', goods: 'Gemini Advanced 月卡', qty: 1, amount: 205, pay: '支付宝', status: '已支付待补货', time: '2026-11-05 09:14', ch: '官方直营' },
  { no: 'CARD1001NO17627501', user: 'u_10055', goods: 'Midjourney 基础版', qty: 2, amount: 224, pay: '支付宝', status: '已关闭', time: '2026-11-05 08:52', ch: '官方直营' },
];

/* 后台库存假数据（卡密一律脱敏） */
const STOCK = [
  { id: 8841, goods: 'ChatGPT Plus', mask: '7f3a****e05b', batch: 'B-20261105-01', supplier: '上游A', status: '未售', in: '2026-11-05 09:12' },
  { id: 8842, goods: 'ChatGPT Plus', mask: '2c91****4ad7', batch: 'B-20261105-01', supplier: '上游A', status: '未售', in: '2026-11-05 09:12' },
  { id: 8843, goods: 'ChatGPT Plus', mask: 'd80e****1f3a', batch: 'B-20261105-01', supplier: '上游A', status: '锁定', in: '2026-11-05 09:12' },
  { id: 8844, goods: 'Claude Pro', mask: '5b27****9c04', batch: 'B-20261104-02', supplier: '上游B', status: '已售', in: '2026-11-04 18:40' },
  { id: 8845, goods: 'Claude Pro', mask: 'e13f****72bb', batch: 'B-20261104-02', supplier: '上游B', status: '未售', in: '2026-11-04 18:40' },
  { id: 8846, goods: 'Midjourney', mask: '9a4c****0e51', batch: 'B-20261103-01', supplier: '上游B', status: '作废', in: '2026-11-03 15:22' },
  { id: 8847, goods: 'Cursor Pro', mask: '6d02****b8f9', batch: 'B-20261103-01', supplier: '上游C', status: '未售', in: '2026-11-03 15:22' },
];
