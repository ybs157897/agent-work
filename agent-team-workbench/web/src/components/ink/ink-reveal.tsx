import { motion, useReducedMotion } from 'motion/react';
import type { ReactNode } from 'react';

type RevealTag = 'div' | 'p' | 'h1' | 'h2' | 'h3' | 'h4' | 'h5' | 'h6' | 'ul' | 'ol' | 'li' | 'blockquote' | 'section';

/** A small Aceternity-style reveal primitive tuned for ink wash: transform, opacity and blur only. */
export function InkReveal({
  as = 'div',
  children,
  className,
  enabled = true,
  delay = 0,
  ...props
}: {
  as?: RevealTag;
  children: ReactNode;
  className?: string;
  enabled?: boolean;
  delay?: number;
  [key: string]: unknown;
}) {
  const reduced = useReducedMotion();
  const initial = enabled && !reduced ? { opacity: 0, y: 10, filter: 'blur(7px)' } : false;
  const animate = enabled && !reduced ? { opacity: 1, y: 0, filter: 'blur(0px)' } : undefined;
  const transition = { duration: 0.44, delay, ease: [0.22, 1, 0.36, 1] as const };
  const common = { className, initial, animate, transition, ...props };
  switch (as) {
    case 'p': return <motion.p {...common}>{children}</motion.p>;
    case 'h1': return <motion.h1 {...common}>{children}</motion.h1>;
    case 'h2': return <motion.h2 {...common}>{children}</motion.h2>;
    case 'h3': return <motion.h3 {...common}>{children}</motion.h3>;
    case 'h4': return <motion.h4 {...common}>{children}</motion.h4>;
    case 'h5': return <motion.h5 {...common}>{children}</motion.h5>;
    case 'h6': return <motion.h6 {...common}>{children}</motion.h6>;
    case 'ul': return <motion.ul {...common}>{children}</motion.ul>;
    case 'ol': return <motion.ol {...common}>{children}</motion.ol>;
    case 'li': return <motion.li {...common}>{children}</motion.li>;
    case 'blockquote': return <motion.blockquote {...common}>{children}</motion.blockquote>;
    case 'section': return <motion.section {...common}>{children}</motion.section>;
    default: return <motion.div {...common}>{children}</motion.div>;
  }
}
