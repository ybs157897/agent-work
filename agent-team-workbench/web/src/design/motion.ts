export const inkMotion = {
  duration: {
    fast: 0.14,
    normal: 0.22,
    slow: 0.36,
    atmospheric: 0.9,
  },
  easeOut: [0.16, 1, 0.3, 1] as const,
  spring: { type: 'spring', stiffness: 260, damping: 30 } as const,
} as const;
