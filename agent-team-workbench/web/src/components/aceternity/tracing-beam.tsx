'use client';
import React, { useEffect, useId, useRef, useState, type RefObject } from 'react';
import { motion, useScroll, useSpring, useTransform } from 'motion/react';
import { cn } from '@/lib/utils';

export const TracingBeam = ({
  children,
  className,
  scrollContainerRef,
}: {
  children: React.ReactNode;
  className?: string;
  /** Aceternity's original primitive follows window scroll; chat supplies its own scroll viewport. */
  scrollContainerRef?: RefObject<HTMLDivElement | null>;
}) => {
  const ref = useRef<HTMLDivElement>(null);
  const { scrollYProgress } = useScroll({
    container: scrollContainerRef,
    target: ref,
    offset: ['start start', 'end start'],
  });

  const contentRef = useRef<HTMLDivElement>(null);
  const [svgHeight, setSvgHeight] = useState(0);

  const gradientId = `ink-beam-${useId().replace(/:/g, '')}`;

  useEffect(() => {
    const node = contentRef.current;
    if (!node) return;
    const update = () => setSvgHeight(Math.max(node.offsetHeight, 1));
    update();
    if (typeof ResizeObserver === 'undefined') return undefined;
    const observer = new ResizeObserver(update);
    observer.observe(node);
    return () => observer.disconnect();
  }, []);

  const y1 = useSpring(
    useTransform(scrollYProgress, [0, 0.8], [50, svgHeight]),
    {
      stiffness: 500,
      damping: 90,
    },
  );
  const y2 = useSpring(
    useTransform(scrollYProgress, [0, 1], [50, svgHeight - 200]),
    {
      stiffness: 500,
      damping: 90,
    },
  );

  return (
    <motion.div
      ref={ref}
      className={cn('relative mx-auto h-full w-full max-w-4xl', className)}
    >
      <div className="absolute top-3 -left-4 sm:-left-10" aria-hidden="true">
        <div className="ml-5 flex h-4 w-4 items-center justify-center rounded-full border border-border-strong bg-surface-base shadow-sm">
          <div className="h-2 w-2 rounded-full border border-brand-primary/50 bg-brand-primary/70" />
        </div>
        <svg
          viewBox={`0 0 20 ${svgHeight}`}
          width="20"
          height={svgHeight} // Set the SVG height
          className="ml-4 block"
          aria-hidden="true"
        >
          <motion.path
            d={`M 1 0V -36 l 18 24 V ${svgHeight * 0.8} l -18 24V ${svgHeight}`}
            fill="none"
            stroke="currentColor"
            strokeOpacity="0.16"
            className="text-border-strong"
            transition={{
              duration: 10,
            }}
          ></motion.path>
          <motion.path
            d={`M 1 0V -36 l 18 24 V ${svgHeight * 0.8} l -18 24V ${svgHeight}`}
            fill="none"
            stroke={`url(#${gradientId})`}
            strokeWidth="1.25"
            className="motion-reduce:hidden"
            transition={{
              duration: 10,
            }}
          ></motion.path>
          <defs>
            <motion.linearGradient
              id={gradientId}
              className="text-brand-primary"
              gradientUnits="userSpaceOnUse"
              x1="0"
              x2="0"
              y1={y1} // set y1 for gradient
              y2={y2} // set y2 for gradient
            >
              <stop stopColor="currentColor" stopOpacity="0" />
              <stop stopColor="currentColor" />
              <stop offset="0.325" stopColor="currentColor" stopOpacity="0.72" />
              <stop offset="1" stopColor="currentColor" stopOpacity="0" />
            </motion.linearGradient>
          </defs>
        </svg>
      </div>
      <div ref={contentRef}>{children}</div>
    </motion.div>
  );
};
