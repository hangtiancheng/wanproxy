/**
 * CodecStats mirrors the C++ PipeByteCount counters around a codec and the
 * Go CodecStats: bytes crossing each of the codec's four boundaries.
 *
 * Node is single-threaded, so the counters are plain numbers rather than
 * atomics.
 */

/** CodecStatsSnapshot is a point-in-time copy of the four counters. */
export interface CodecStatsSnapshot {
  /** Bytes entering from the interface side. */
  readonly incomingToCodecBytes: number;
  /** Bytes leaving toward the peer side. */
  readonly codecToOutgoingBytes: number;
  /** Bytes entering from the peer side. */
  readonly outgoingToCodecBytes: number;
  /** Bytes leaving toward the interface side. */
  readonly codecToIncomingBytes: number;
}

export class CodecStats {
  private incomingToCodec = 0;
  private codecToOutgoing = 0;
  private outgoingToCodec = 0;
  private codecToIncoming = 0;

  public addIncomingToCodec(bytes: number): void {
    this.incomingToCodec += bytes;
  }

  public addCodecToOutgoing(bytes: number): void {
    this.codecToOutgoing += bytes;
  }

  public addOutgoingToCodec(bytes: number): void {
    this.outgoingToCodec += bytes;
  }

  public addCodecToIncoming(bytes: number): void {
    this.codecToIncoming += bytes;
  }

  /** snapshot reads the four counters. */
  public snapshot(): CodecStatsSnapshot {
    return {
      codecToIncomingBytes: this.codecToIncoming,
      codecToOutgoingBytes: this.codecToOutgoing,
      incomingToCodecBytes: this.incomingToCodec,
      outgoingToCodecBytes: this.outgoingToCodec,
    };
  }
}
