import { Duplex } from "node:stream";

import type { SshTransport } from "./transport.js";

/**
 * SshStream adapts an SshTransport into a duplex byte stream so the proxy
 * relay can read from and write to it exactly like a socket, mirroring the
 * Go ssh.Transport's role as a StreamConn.  Writes go through the hop's
 * framing (writeApp); decrypted application bytes are pushed to the
 * readable side (onAppData).  Half-close is propagated with closeWrite,
 * and transport teardown ends the readable side and destroys the stream,
 * so the relay's close controller observes a 'close' event like it does
 * for a net.Socket.
 */
export class SshStream extends Duplex {
  private eofPushed = false;

  public constructor(private readonly transport: SshTransport) {
    super({ allowHalfOpen: true });
    transport.onAppData((data) => {
      // The transport reads the socket in flowing mode and has no pause
      // hook, so — exactly like the Go transport's plainBuf — data is
      // pushed without applying back-pressure to the socket.
      if (!this.eofPushed && !this.destroyed) {
        this.push(Buffer.from(data));
      }
    });
    transport.onClose(() => {
      if (!this.eofPushed && !this.destroyed) {
        this.eofPushed = true;
        this.push(null);
      }
      if (!this.destroyed) {
        this.destroy();
      }
    });
  }

  public override _read(): void {
    // Application data is pushed from the transport's onAppData handler.
  }

  public override _write(
    chunk: Buffer,
    _encoding: string,
    callback: (error?: Error | null) => void,
  ): void {
    try {
      this.transport.writeApp(chunk);
      callback();
    } catch (error) {
      callback(toError(error));
    }
  }

  public override _final(callback: (error?: Error | null) => void): void {
    try {
      this.transport.closeWrite();
      callback();
    } catch (error) {
      callback(toError(error));
    }
  }

  public override _destroy(
    error: Error | null,
    callback: (error?: Error | null) => void,
  ): void {
    this.transport.close();
    callback(error);
  }
}

function toError(error: unknown): Error {
  return error instanceof Error ? error : new Error(String(error));
}
