// 渠道抽屉里 apiduo 扩展的唯一入口。
//
// drawer 是上游文件（5700 行），每加一个面板就要在它顶部插一次 import，
// 而每次插入都是未来合并时的一处冲突点 —— 这些 import 分散在文件头，
// 与上游的 import 交错排列，冲突时无法一眼判断归属。
//
// 这里把 import 面收成一个：drawer 只认本文件，新增面板或工具改这里，
// 不再动上游的 import 区。
//
// 只做 re-export，不放逻辑。有逻辑就说明它该有自己的模块。
export {
  readImageProtocol,
  writeImageProtocol,
} from './image-protocol-settings'
export {
  fillPrimaryForStats,
  readPrimaryEndpoint,
  writePrimaryEndpoint,
} from './primary-endpoint'
export { CHANNEL_TYPE_ADVANCED_ASYNC } from './advanced-async'
export { AdvancedAsyncPanel } from '../components/advanced-async-panel'
export { PrimaryEndpointSummary } from '../components/primary-endpoint-summary'
export { MultiKeyModeHint } from '../components/multi-key-mode-hint'
export { MinActiveKeysField } from '../components/min-active-keys-field'
export { FileRelaySetting } from '../components/file-relay-setting'
export { ArtifactStorageBlock } from '../components/artifact-storage-setting'
export { apiduoBlockMark } from '@/features/apiduo-marks/marks'
