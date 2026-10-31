/**
 * Meter ballistics. Audacity drives its meters at 60 Hz, jumps a peak to a new
 * maximum immediately, then lets it fall at a constant 36 dB per second. That
 * is what keeps a meter reactive on the attack and readable on the release: a
 * bar that only mirrors the latest block looks stepped, and a bar with a long
 * CSS transition lags the audio.
 *
 * The engine already reports a peak with its own release, but that release is
 * per audio block, so its rate depends on the block size and the value arrives
 * in steps. A display that decelerates in dB per second, anchored to wall-clock
 * time, is stable at any event rate.
 */
export const METER_DECAY_DB_PER_SECOND = 36;

/**
 * METER_HOLD_MS is how long the wall keeps a peak before gravity takes over.
 * Audacity's AudioMeter allows one audio block of hangover, so a kick stays
 * visible for a frame or two instead of being clipped the instant the block
 * ends.
 */
export const METER_HOLD_MS = 40;

/**
 * METER_WALL_DECAY_DB_PER_SECOND is the wall's own fall rate. It is slower than
 * the bar, which is what keeps the wall above it: the bar drops as soon as the
 * sample does, the wall sinks after it and lands on the bar when the bar does.
 */
export const METER_WALL_DECAY_DB_PER_SECOND = 18;

/**
 * stepMeterDb advances one displayed level toward a new target. A target above
 * the previous value is taken at once (attack); otherwise the value falls by
 * the decay rate over elapsed time, never below the target. A missing target
 * resets the display so an inert row does not animate from a stale level.
 */
export function stepMeterDb(
  previous: number | null,
  target: number | null,
  dtSeconds: number,
  decayDbPerSecond = METER_DECAY_DB_PER_SECOND,
): number | null {
  if (target === null || !Number.isFinite(target)) {
    return null;
  }
  if (previous === null || !Number.isFinite(previous) || target >= previous) {
    return target;
  }

  const dt = Number.isFinite(dtSeconds) && dtSeconds > 0 ? dtSeconds : 0;
  const decayed = previous - decayDbPerSecond * dt;

  return Math.max(target, decayed);
}

/**
 * MeterWall is the level the bar must beat to climb. It is not a retained peak
 * that sits and waits: the bar pushes it up on a hit, then it falls under its
 * own gravity, always staying at or above the bar.
 */
export interface MeterWall {
  db: number | null;
  heldMs: number;
}

/**
 * stepMeterWall advances the wall. It snaps to a bar that reaches it, holds for
 * holdMs so the hit is visible, then falls at the decay rate on its own and
 * lands on the bar if the bar is higher. The bar follows its own value, so it
 * drops as soon as the sample does and the wall follows a moment later.
 */
export function stepMeterWall(
  previous: MeterWall,
  level: number | null,
  dtSeconds: number,
  holdMs = METER_HOLD_MS,
  decayDbPerSecond = METER_WALL_DECAY_DB_PER_SECOND,
): MeterWall {
  if (level === null || !Number.isFinite(level)) {
    return { db: null, heldMs: 0 };
  }
  if (previous.db === null || !Number.isFinite(previous.db) || level >= previous.db) {
    return { db: level, heldMs: 0 };
  }

  const dt = Number.isFinite(dtSeconds) && dtSeconds > 0 ? dtSeconds : 0;
  const heldMs = previous.heldMs + dt * 1000;
  if (heldMs < holdMs) {
    return { db: previous.db, heldMs };
  }

  return { db: Math.max(level, previous.db - decayDbPerSecond * dt), heldMs };
}