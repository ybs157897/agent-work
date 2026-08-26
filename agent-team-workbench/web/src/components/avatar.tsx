/** 头像：有 URL 用图；否则按 id 哈希取身份色阶首字符头像（identity-1..8，DESIGN.md Identity 条）。 */
const PALETTE = [
  'bg-identity-1',
  'bg-identity-2',
  'bg-identity-3',
  'bg-identity-4',
  'bg-identity-5',
  'bg-identity-6',
  'bg-identity-7',
  'bg-identity-8',
];

function pickColor(seed: string): string {
  let h = 0;
  for (let i = 0; i < seed.length; i++) h = (h * 31 + seed.charCodeAt(i)) | 0;
  return PALETTE[Math.abs(h) % PALETTE.length];
}

export function Avatar({
  name,
  url,
  size = 40,
  ring = false,
}: {
  name: string;
  url?: string;
  size?: number;
  ring?: boolean;
}) {
  const cls = `rounded-button object-cover shrink-0 ${ring ? 'ring-2 ring-surface-raised shadow-sm' : ''}`;
  if (url) {
    return <img src={url} alt={name} className={cls} style={{ width: size, height: size }} />;
  }
  return (
    <div
      className={`${pickColor(name)} text-text-inverse flex items-center justify-center font-display shrink-0 rounded-button ${
        ring ? 'ring-2 ring-surface-raised shadow-sm' : ''
      }`}
      style={{ width: size, height: size, fontSize: size * 0.42 }}
      aria-label={name}
    >
      {name.slice(0, 1).toUpperCase()}
    </div>
  );
}
