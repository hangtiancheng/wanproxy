import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { XCodecDisk } from "@/xcodec/disk-cache.js";
import { XCODEC_SEGMENT_LENGTH, XCodecHash } from "@/xcodec/index.js";

const DISK_SIZE = 8 * 1024 * 1024;

function segment(fill: number, salt = 0): Uint8Array {
  const data = new Uint8Array(XCODEC_SEGMENT_LENGTH).fill(fill);
  data[0] = salt & 0xff;
  data[1] = (salt >>> 8) & 0xff;
  return data;
}

describe("XCodecDisk (XCDFS)", () => {
  let dir: string;
  beforeEach(() => {
    dir = mkdtempSync(join(tmpdir(), "wanproxy-xcdfs-"));
  });
  afterEach(() => {
    rmSync(dir, { recursive: true, force: true });
  });

  it("stores and retrieves a segment", () => {
    const disk = XCodecDisk.open(join(dir, "c.xcdfs"), DISK_SIZE);
    const cache = disk.local();
    const seg = segment(0xab);
    const hash = XCodecHash.hashSegment(seg);
    cache.enter(hash, seg);
    expect(cache.lookup(hash)).toEqual(seg);
    expect(cache.has(hash)).toBe(true);
    disk.close();
  });

  it("persists segments across reopen", () => {
    const path = join(dir, "c.xcdfs");
    const seg = segment(0xcd, 7);
    const hash = XCodecHash.hashSegment(seg);

    let disk = XCodecDisk.open(path, DISK_SIZE);
    disk.local().enter(hash, seg);
    disk.close();

    disk = XCodecDisk.open(path, 0);
    expect(disk.local().lookup(hash)).toEqual(seg);
    disk.close();
  });

  it("keeps separate namespaces per connected UUID", () => {
    const path = join(dir, "c.xcdfs");
    const disk = XCodecDisk.open(path, DISK_SIZE);
    const local = disk.local();

    const peerId = new Uint8Array(16).fill(0x42);
    const peer = disk.connect(peerId);
    expect(peer).not.toBe(local);

    const seg = segment(0x11, 3);
    const hash = XCodecHash.hashSegment(seg);
    peer.enter(hash, seg);

    // The segment is visible through the peer front-end but not the local
    // one, mirroring the C++ per-XUID index separation.
    expect(peer.lookup(hash)).toEqual(seg);
    expect(local.lookup(hash)).toBeUndefined();

    // Reconnecting the same UUID returns the same front-end.
    expect(disk.connect(peerId)).toBe(peer);
    disk.close();
  });

  it("survives FIFO wraparound and re-reads surviving segments", () => {
    const path = join(dir, "c.xcdfs");
    // A small disk forces the write head to wrap after a few hundred
    // inserts, exercising index-block invalidation.
    const smallSize = 512 * 1024;
    const disk = XCodecDisk.open(path, smallSize);
    const cache = disk.local();

    const kept: { hash: bigint; seg: Uint8Array }[] = [];
    for (let i = 0; i < 600; i += 1) {
      const seg = segment(0x20 + (i % 16), i);
      const hash = XCodecHash.hashSegment(seg);
      cache.enter(hash, seg);
      kept.push({ hash, seg });
    }

    // The most recently entered segments must still resolve; the oldest may
    // have been overwritten by the FIFO.  Verify the tail is intact.
    const tail = kept.slice(-50);
    for (const { hash, seg } of tail) {
      expect(cache.lookup(hash)).toEqual(seg);
    }
    disk.close();
  });
});
