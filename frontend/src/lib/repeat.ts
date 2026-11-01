// repeat.ts: the repeat toggle's target value.
//
// Repeat has no optimistic store of its own, and the snapshot's `repeat` lags
// the round trip. Reading it directly makes two keypresses inside one snapshot
// window compute the same target, so a fast double press never turns repeat
// back off. The caller holds the pending intent and this decides the target,
// the same shape as the shuffle toggle.

/** RepeatMode is the engine's three repeat states. */
export type RepeatMode = "off" | "all" | "one";

/**
 * nextRepeatTarget returns the mode the next repeat press should send. Pressing
 * the key for the mode already in force (or already on its way) turns repeat
 * off; pressing it from any other mode switches to that mode.
 */
export function nextRepeatTarget(
  intent: RepeatMode | null,
  server: string,
  want: Exclude<RepeatMode, "off">,
): RepeatMode {
  return (intent ?? server) === want ? "off" : want;
}
