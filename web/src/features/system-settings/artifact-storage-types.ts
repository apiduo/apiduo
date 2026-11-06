/**
 * Option keys for the gateway-wide object storage.
 *
 * They live in their own module for one reason: `types.ts` is edited by
 * upstream roughly every ten days, and a dozen inline fields there would
 * collide with that churn on every sync. Intersecting a separate type keeps the
 * change to the shared file at a single line.
 */

/** A storage service the gateway knows how to address. */
export type ArtifactStorageProvider =
  | 'minio'
  | 'aliyun-oss'
  | 'tencent-cos'
  | 'volcengine-tos'
  | 's3'

/**
 * One destination in the ordered pool.
 *
 * All providers speak S3; the field only picks addressing defaults and the
 * endpoint hint, so adding a provider is a constant, not a new transport.
 */
export type ArtifactStorageTarget = {
  provider?: ArtifactStorageProvider
  endpoint: string
  bucket: string
  region?: string
  access_key?: string
  secret_key?: string
  prefix?: string
  path_style?: boolean
}

export const ARTIFACT_STORAGE_PROVIDERS: readonly {
  value: ArtifactStorageProvider
  label: string
  endpointHint: string
  regionHint: string
  /** MinIO is the only one that addresses buckets by path. */
  pathStyle: boolean
}[] = [
  {
    value: 'minio',
    label: 'MinIO',
    endpointHint: 'http://minio:9000',
    regionHint: 'us-east-1',
    pathStyle: true,
  },
  {
    value: 'aliyun-oss',
    label: 'Alibaba Cloud OSS',
    endpointHint: 'https://s3.oss-cn-hangzhou.aliyuncs.com',
    regionHint: 'cn-hangzhou',
    pathStyle: false,
  },
  {
    value: 'tencent-cos',
    label: 'Tencent Cloud COS',
    endpointHint: 'https://cos.ap-guangzhou.myqcloud.com',
    regionHint: 'ap-guangzhou',
    pathStyle: false,
  },
  {
    value: 'volcengine-tos',
    label: 'Volcengine TOS',
    endpointHint: 'https://tos-s3-cn-beijing.volces.com',
    regionHint: 'cn-beijing',
    pathStyle: false,
  },
  {
    value: 's3',
    label: 'S3 compatible',
    endpointHint: 'https://s3.amazonaws.com',
    regionHint: 'us-east-1',
    pathStyle: false,
  },
]

export function providerDefaults(provider: string | undefined) {
  return (
    ARTIFACT_STORAGE_PROVIDERS.find((item) => item.value === provider) ??
    ARTIFACT_STORAGE_PROVIDERS[ARTIFACT_STORAGE_PROVIDERS.length - 1]
  )
}

export type ArtifactStorageSettingKeys = {
  'artifact_storage_setting.enabled': boolean
  /**
   * JSON encoded `ArtifactStorageTarget[]`. The option store is a flat
   * key/value map, so a list has to travel as a string.
   */
  'artifact_storage_setting.targets': string
  'artifact_storage_setting.presign_ttl_seconds': number
  'artifact_storage_setting.max_object_bytes': number
  /**
   * JSON encoded `string[]` of JSONPath selectors used when a channel declares
   * none. Empty means "use the built-in field matching".
   */
  'artifact_storage_setting.rewrite_paths': string
  /**
   * JSON encoded `string[]` of field names for the built-in matching. Empty
   * means the built-in default list.
   */
  'artifact_storage_setting.field_names': string
  // Deprecated single-target fields. Still read so an installation configured
  // before the pool existed keeps working without a migration step.
  'artifact_storage_setting.endpoint'?: string
  'artifact_storage_setting.bucket'?: string
  'artifact_storage_setting.region'?: string
  'artifact_storage_setting.access_key'?: string
  'artifact_storage_setting.secret_key'?: string
  'artifact_storage_setting.prefix'?: string
  'artifact_storage_setting.path_style'?: boolean
}

/** Parses the stored JSON, falling back to the deprecated single target. */
export function parseArtifactStorageTargets(
  raw: string | undefined,
  legacy: Partial<ArtifactStorageTarget>
): ArtifactStorageTarget[] {
  if (raw?.trim()) {
    try {
      const parsed: unknown = JSON.parse(raw)
      if (Array.isArray(parsed)) {
        return parsed
          .filter(
            (item): item is ArtifactStorageTarget =>
              Boolean(item) &&
              typeof item === 'object' &&
              typeof (item as ArtifactStorageTarget).endpoint === 'string' &&
              typeof (item as ArtifactStorageTarget).bucket === 'string'
          )
          .map((item) => ({ ...item }))
      }
    } catch {
      // A malformed blob is treated as empty; saving overwrites it anyway.
    }
  }
  if (legacy.endpoint?.trim() || legacy.bucket?.trim()) {
    return [
      {
        provider: 's3',
        endpoint: legacy.endpoint ?? '',
        bucket: legacy.bucket ?? '',
        region: legacy.region,
        access_key: legacy.access_key,
        secret_key: legacy.secret_key,
        prefix: legacy.prefix,
        path_style: legacy.path_style,
      },
    ]
  }
  return []
}
