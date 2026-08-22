/** 头像：有 URL 用图；否则按 id 哈希取主题色阶的首字符头像（主题规范禁用紫色）。 */
const PALETTE = [
  'bg-sky-500',
  'bg-cyan-600',
  'bg-teal-500',
  'bg-emerald-500',
  'bg-blue-500',
  'bg-amber-500',
  'bg-slate-500',
  'bg-rose-400',
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
  const cls = `rounded-full object-cover shrink-0 ${ring ? 'ring-2 ring-white shadow-sm' : ''}`;
  if (url) {
    return <img src={url} alt={name} className={cls} style={{ width: size, height: size }} />;
  }
  return (
    <div
      className={`${pickColor(name)} text-white flex items-center justify-center font-semibold shrink-0 rounded-full ${
        ring ? 'ring-2 ring-white shadow-sm' : ''
      }`}
      style={{ width: size, height: size, fontSize: size * 0.42 }}
      aria-label={name}
    >
      {name.slice(0, 1).toUpperCase()}
    </div>
  );
}
