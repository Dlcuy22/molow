import { describe, expect, it } from "vitest";
import {
  METER_DECAY_DB_PER_SECOND,
  METER_HOLD_MS,
  METER_WALL_DECAY_DB_PER_SECOND,
  stepMeterDb,
  stepMeterWall,
} from "./meter-ballistics";

describe("stepMeterDb", () => {
  it("takes a higher target at once", () => {
    expect(stepMeterDb(-40, -6, 1 / 60)).toBe(-6);
    expect(stepMeterDb(null, -6, 1 / 60)).toBe(-6);
  });

  it("falls by the decay rate over elapsed time", () => {
    const next = stepMeterDb(-6, -60, 0.5);
    expect(next).toBeCloseTo(-6 - METER_DECAY_DB_PER_SECOND * 0.5, 10);
  });

  it("does not fall through the target", () => {
    expect(stepMeterDb(-6, -10, 10)).toBe(-10);
  });

  it("holds still when no time has passed", () => {
    expect(stepMeterDb(-6, -20, 0)).toBeCloseTo(-6, 10);
    expect(stepMeterDb(-6, -20, Number.NaN)).toBeCloseTo(-6, 10);
  });

  it("clears the display when the target is absent", () => {
    expect(stepMeterDb(-6, null, 1 / 60)).toBeNull();
    expect(stepMeterDb(-6, Number.NaN, 1 / 60)).toBeNull();
  });
});

describe("stepMeterWall", () => {
  it("snaps up to a bar that reaches it", () => {
    const next = stepMeterWall({ db: -30, heldMs: 200 }, -6, 1 / 60);
    expect(next.db).toBe(-6);
    expect(next.heldMs).toBe(0);
  });

  it("holds for the hangover, then falls on its own", () => {
    const held = stepMeterWall({ db: -6, heldMs: 0 }, -40, METER_HOLD_MS / 1000 / 2);
    expect(held.db).toBe(-6);

    const falling = stepMeterWall(held, -40, 0.5);
    expect(falling.db).toBeCloseTo(-6 - METER_WALL_DECAY_DB_PER_SECOND * 0.5, 10);
  });

  it("falls slower than the bar, so it stays above it", () => {
    expect(METER_WALL_DECAY_DB_PER_SECOND).toBeLessThan(METER_DECAY_DB_PER_SECOND);

    const wall = { db: -6, heldMs: METER_HOLD_MS };
    const bar = -6 - METER_DECAY_DB_PER_SECOND * 0.5;
    const next = stepMeterWall(wall, bar, 0.5);
    expect(next.db).toBeGreaterThan(bar);
  });

  it("never falls below the bar it is tracking", () => {
    expect(stepMeterWall({ db: -6, heldMs: METER_HOLD_MS }, -8, 10).db).toBe(-8);
  });

  it("resets when the level is absent", () => {
    expect(stepMeterWall({ db: -6, heldMs: 0 }, null, 1 / 60)).toEqual({ db: null, heldMs: 0 });
  });
});