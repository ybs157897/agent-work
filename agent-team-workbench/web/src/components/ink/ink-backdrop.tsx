import { motion, useReducedMotion } from 'motion/react';
import shanShuiPanorama from '../../assets/ink/shan-shui-panorama.webp';
import { inkMotion } from '../../design/motion';

/** Decorative atmosphere only; all information remains live HTML. */
export function InkBackdrop() {
  const reduceMotion = useReducedMotion();
  return (
    <motion.div
      className="ink-mountain-layer"
      aria-hidden="true"
      initial={reduceMotion ? false : { opacity: 0, y: -6 }}
      animate={{ opacity: 0.2, y: 0 }}
      transition={{ duration: reduceMotion ? 0 : inkMotion.duration.atmospheric, ease: inkMotion.easeOut }}
    >
      <img src={shanShuiPanorama} alt="" width={1774} height={887} decoding="async" />
    </motion.div>
  );
}
