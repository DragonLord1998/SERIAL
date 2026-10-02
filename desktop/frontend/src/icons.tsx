import type { CSSProperties } from 'react'

export type IconName = 'home' | 'explore' | 'like' | 'dislike' | 'search' | 'library' | 'download' | 'settings' | 'play' | 'pause' | 'close' | 'arrow' | 'check' | 'plus' | 'volume' | 'external' | 'folder' | 'refresh' | 'alert' | 'chevron' | 'stop' | 'film' | 'clock'
const paths: Record<IconName, React.ReactNode> = {
  home: <><path d="m3 10 9-7 9 7v10a1 1 0 0 1-1 1h-5v-7H9v7H4a1 1 0 0 1-1-1Z" /></>,
  explore: <><circle cx="12" cy="12" r="9" /><path d="m16 8-3 5-5 3 3-5Z" /></>,
  like: <><path d="M8 10h-4v10h4ZM8 19h9a2 2 0 0 0 2-1.6l2-7A2 2 0 0 0 19 8h-6l1-4a2 2 0 0 0-2-2l-4 8Z" /></>,
  dislike: <><path d="M8 14h-4V4h4ZM8 5h9a2 2 0 0 1 2 1.6l2 7a2 2 0 0 1-2 2h-6l1 4a2 2 0 0 1-2 2l-4-8Z" /></>,
  search: <><circle cx="10.5" cy="10.5" r="6.5" /><path d="m16 16 5 5" /></>,
  library: <><rect x="3" y="4" width="5" height="16" rx="1" /><path d="M12 4v16M16 4l5 15" /></>,
  download: <><path d="M12 3v12m-5-5 5 5 5-5M4 17v4h16v-4" /></>,
  settings: <><path d="m9 3-1 3-3 1-2 3 2 2v3l2 2 3-1 2 2h3l1-3 3-1 2-3-2-2V7l-2-2-3 1-2-3Z" /><circle cx="12" cy="11" r="3" /></>,
  play: <path d="m8 4 12 8-12 8Z" fill="currentColor" stroke="none" />,
  pause: <><path d="M8 5v14M16 5v14" strokeWidth="4" /></>,
  close: <path d="m6 6 12 12M6 18 18 6" />,
  arrow: <path d="M4 12h16m-6-6 6 6-6 6" />,
  check: <path d="m5 12 4 4L19 6" />,
  plus: <path d="M12 4v16M4 12h16" />,
  volume: <><path d="M4 9h4l5-4v14l-5-4H4ZM17 8a6 6 0 0 1 0 8m3-11a10 10 0 0 1 0 14" /></>,
  external: <><path d="M14 3h7v7m0-7L11 13M9 3H4v17h17v-5" /></>,
  folder: <path d="M3 6h7l2 3h9v11H3Z" />,
  refresh: <><path d="M20 10a8 8 0 1 0-1 8M20 4v6h-6" /></>,
  alert: <><path d="m12 3 10 18H2Z" /><path d="M12 9v5m0 3h.01" /></>,
  chevron: <path d="m9 5 7 7-7 7" />,
  stop: <rect x="6" y="6" width="12" height="12" rx="1" fill="currentColor" stroke="none" />,
  film: <><rect x="3" y="4" width="18" height="16" rx="2" /><path d="M7 4v16M17 4v16M3 9h4M3 15h4M17 9h4M17 15h4" /></>,
  clock: <><circle cx="12" cy="12" r="9" /><path d="M12 7v5l3 2" /></>,
}
export function Icon({ name, size = 20, className, style }: { name: IconName; size?: number; className?: string; style?: CSSProperties }) {
  return <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" className={className} style={style}>{paths[name]}</svg>
}
