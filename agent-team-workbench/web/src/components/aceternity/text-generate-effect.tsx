/** Aceternity UI Text Generate Effect with semantic color and reduced-motion support. */
import { motion, stagger, useAnimate, useReducedMotion } from 'motion/react';
import { Fragment, useEffect } from 'react';
import { cn } from '@/lib/utils';

type TextTag = 'div' | 'span' | 'h1' | 'h2' | 'h3' | 'h4' | 'h5' | 'h6';

/** Chinese headings reveal character-by-character; Latin text keeps word groups and exact whitespace. */
export function textGenerateTokens(words: string): string[] {
  return words.match(/\p{Script=Han}|\s+|[^\p{Script=Han}\s]+/gu) ?? [];
}

export function TextGenerateEffect({
  words,
  className,
  filter = true,
  duration = 0.5,
  as: Tag = 'div',
}: {
  words: string;
  className?: string;
  filter?: boolean;
  duration?: number;
  as?: TextTag;
}) {
  const [scope, animate] = useAnimate();
  const reduceMotion = useReducedMotion();
  const tokens = textGenerateTokens(words);

  useEffect(() => {
    void animate(
      'span',
      { opacity: 1, filter: 'blur(0px)' },
      { duration: reduceMotion ? 0 : duration, delay: reduceMotion ? 0 : stagger(0.12) },
    );
  }, [animate, duration, reduceMotion, words]);

  return (
    <Tag className={cn('font-display text-text-primary', className)}>
      <motion.span ref={scope}>
        {tokens.map((token, index) => (
          <Fragment key={`${token}-${index}`}>
            {/^\s+$/u.test(token) ? token : (
              <motion.span
                className={cn('inline-block', reduceMotion ? 'opacity-100' : 'opacity-0')}
                style={{ filter: filter && !reduceMotion ? 'blur(8px)' : 'none' }}
              >
                {token}
              </motion.span>
            )}
          </Fragment>
        ))}
      </motion.span>
    </Tag>
  );
}
