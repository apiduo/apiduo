export { CoverageSummary } from './components/coverage-summary'
export {
  buildCoverage,
  collectEndpoints,
  collectModels,
  coveredEndpoints,
  findRouteForModel,
  rejectedCells,
} from './lib/coverage'
export {
  converterOptionsForPath,
  decodeMatrix,
  encodeMatrix,
  validateMatrix,
} from './lib/matrix'
export { MATRIX_DEFAULT_PATHS } from './types'
export type {
  Coverage,
  CoverageCell,
  Matrix,
  MatrixDecodeFailure,
  MatrixDecodeResult,
  MatrixItem,
  MatrixMode,
  MatrixOutcome,
} from './types'
