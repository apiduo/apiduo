import {
  Activity,
  BookOpen,
  Bot,
  Box,
  Boxes,
  Brain,
  Briefcase,
  Calendar,
  Cloud,
  Code,
  Compass,
  CreditCard,
  Database,
  ExternalLink,
  Film,
  FileText,
  FlaskConical,
  Gauge,
  Globe,
  HardDrive,
  ImageIcon,
  Key,
  LayoutDashboard,
  ListTodo,
  MessageSquare,
  Monitor,
  Network,
  Package,
  Plug,
  PlugZap,
  Radio,
  Rocket,
  Search,
  Server,
  ServerCog,
  Settings,
  Shield,
  Sparkles,
  Terminal,
  Ticket,
  TrendingUp,
  Users,
  Wallet,
  Wrench,
  Zap,
} from 'lucide-react'
import {
  SiAlibabacloud,
  SiAnthropic,
  SiClaude,
  SiClickhouse,
  SiCloudflare,
  SiCursor,
  SiDiscord,
  SiDocker,
  SiFigma,
  SiGithub,
  SiGitlab,
  SiGooglegemini,
  SiGrafana,
  SiHuggingface,
  SiKubernetes,
  SiLangchain,
  SiLmstudio,
  SiMistralai,
  SiNotion,
  SiNvidia,
  SiOllama,
  SiOpenrouter,
  SiPerplexity,
  SiPostman,
  SiTelegram,
  SiVercel,
  SiZedindustries,
  SiZoom,
} from 'react-icons/si'

import { customNavAppIcon, type NavAppIconComponent } from './custom-icon'

/**
 * Curated icon set for configured navigation entries.
 *
 * The entry stores a name, not markup, and it is resolved through this closed
 * map. Arbitrary icon markup, font classes or image URLs are deliberately not
 * supported here: they would put administrator input into the render tree and
 * into outbound image requests. A separately validated https image address
 * (`icon_url`) is the only way to supply a file. An unknown name falls back to
 * a neutral globe rather than breaking the row.
 */
const PRESET_ICONS: Record<string, NavAppIconComponent> = {
  activity: Activity,
  'book-open': BookOpen,
  bot: Bot,
  box: Box,
  boxes: Boxes,
  brain: Brain,
  briefcase: Briefcase,
  calendar: Calendar,
  cloud: Cloud,
  code: Code,
  compass: Compass,
  'credit-card': CreditCard,
  database: Database,
  'external-link': ExternalLink,
  'file-text': FileText,
  film: Film,
  flask: FlaskConical,
  gauge: Gauge,
  globe: Globe,
  'hard-drive': HardDrive,
  image: ImageIcon,
  key: Key,
  'layout-dashboard': LayoutDashboard,
  'list-todo': ListTodo,
  'message-square': MessageSquare,
  monitor: Monitor,
  network: Network,
  package: Package,
  plug: Plug,
  'plug-zap': PlugZap,
  radio: Radio,
  rocket: Rocket,
  search: Search,
  server: Server,
  'server-cog': ServerCog,
  settings: Settings,
  shield: Shield,
  sparkles: Sparkles,
  terminal: Terminal,
  ticket: Ticket,
  'trending-up': TrendingUp,
  users: Users,
  wallet: Wallet,
  wrench: Wrench,
  zap: Zap,
}

/**
 * Brand marks come from Simple Icons via react-icons. They are real components
 * from a pinned dependency, not administrator input, so adding them does not
 * widen the render-tree surface described above. They cover the external
 * services an operator most often links from a gateway sidebar.
 */
const AI_MODEL_ICONS: Record<string, NavAppIconComponent> = {
  claude: SiClaude,
  anthropic: SiAnthropic,
  gemini: SiGooglegemini,
  huggingface: SiHuggingface,
  ollama: SiOllama,
  langchain: SiLangchain,
  mistralai: SiMistralai,
  perplexity: SiPerplexity,
  openrouter: SiOpenrouter,
  nvidia: SiNvidia,
  lmstudio: SiLmstudio,
}

const DEV_TOOL_ICONS: Record<string, NavAppIconComponent> = {
  github: SiGithub,
  gitlab: SiGitlab,
  docker: SiDocker,
  vercel: SiVercel,
  cloudflare: SiCloudflare,
  grafana: SiGrafana,
  postman: SiPostman,
  clickhouse: SiClickhouse,
  kubernetes: SiKubernetes,
  cursor: SiCursor,
}

const APP_ICONS: Record<string, NavAppIconComponent> = {
  notion: SiNotion,
  discord: SiDiscord,
  telegram: SiTelegram,
  figma: SiFigma,
  zoom: SiZoom,
  zed: SiZedindustries,
  alibabacloud: SiAlibabacloud,
}

export const NAV_APP_ICONS: Record<string, NavAppIconComponent> = {
  ...PRESET_ICONS,
  ...AI_MODEL_ICONS,
  ...DEV_TOOL_ICONS,
  ...APP_ICONS,
}

export const NAV_APP_ICON_OPTIONS = Object.keys(NAV_APP_ICONS).sort()

/** Grouped, human-readable choices for the settings dropdown. */
export const NAV_APP_ICON_GROUPS = [
  {
    id: 'preset',
    labelKey: 'Preset icons',
    names: Object.keys(PRESET_ICONS).sort(),
  },
  {
    id: 'ai',
    labelKey: 'AI & models',
    names: Object.keys(AI_MODEL_ICONS).sort(),
  },
  {
    id: 'dev',
    labelKey: 'Dev tools',
    names: Object.keys(DEV_TOOL_ICONS).sort(),
  },
  {
    id: 'apps',
    labelKey: 'Apps & services',
    names: Object.keys(APP_ICONS).sort(),
  },
] as const

const BRAND_LABELS: Record<string, string> = {
  claude: 'Claude',
  anthropic: 'Anthropic',
  gemini: 'Gemini',
  huggingface: 'Hugging Face',
  ollama: 'Ollama',
  langchain: 'LangChain',
  mistralai: 'Mistral AI',
  perplexity: 'Perplexity',
  openrouter: 'OpenRouter',
  nvidia: 'NVIDIA',
  lmstudio: 'LM Studio',
  github: 'GitHub',
  gitlab: 'GitLab',
  docker: 'Docker',
  vercel: 'Vercel',
  cloudflare: 'Cloudflare',
  grafana: 'Grafana',
  postman: 'Postman',
  clickhouse: 'ClickHouse',
  kubernetes: 'Kubernetes',
  cursor: 'Cursor',
  notion: 'Notion',
  discord: 'Discord',
  telegram: 'Telegram',
  figma: 'Figma',
  zoom: 'Zoom',
  zed: 'Zed',
  alibabacloud: 'Alibaba Cloud',
}

/** A readable label for a stored icon name; brand names keep their casing. */
export function navAppIconLabel(name: string): string {
  return (
    BRAND_LABELS[name] ??
    name
      .split('-')
      .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
      .join(' ')
  )
}

export function navAppIcon(
  name: string | undefined,
  iconUrl?: string
): NavAppIconComponent {
  // The preset stays meaningful even with a custom image: it is what the row
  // falls back to when the image cannot load.
  const preset = (name && NAV_APP_ICONS[name]) || Globe
  const url = iconUrl?.trim()
  return url ? customNavAppIcon(url, preset) : preset
}
