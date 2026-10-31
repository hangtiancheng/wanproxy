/**
 * parseSize parses the C++ ConfigTypeSize format: a number with an
 * optional b/kb/mb/gb/tb suffix (case-insensitive), base 1024, mirroring
 * config/size.go.  The numeric part is parsed base 0, so 0x/0o/0b
 * prefixes and a leading zero (octal) are accepted, exactly like the C++
 * type.  Throws on an empty or invalid size.
 */
export function parseSize(input: string): number {
  const trimmed = input.trim();
  if (trimmed === "") {
    throw new Error("empty size");
  }

  let multiplier = 1;
  let body = trimmed;
  const lower = trimmed.toLowerCase();
  const suffixes: ReadonlyArray<readonly [string, number]> = [
    ["tb", 1024 ** 4],
    ["gb", 1024 ** 3],
    ["mb", 1024 ** 2],
    ["kb", 1024],
    ["t", 1024 ** 4],
    ["g", 1024 ** 3],
    ["m", 1024 ** 2],
    ["k", 1024],
    ["b", 1],
  ];
  for (const [suffix, factor] of suffixes) {
    if (lower.endsWith(suffix)) {
      multiplier = factor;
      body = body.slice(0, body.length - suffix.length);
      break;
    }
  }

  return parseIntegerBase0(body.trim(), trimmed) * multiplier;
}

/**
 * parseIntegerBase0 mirrors strconv.ParseInt(s, 0, 64): the base is
 * inferred from the prefix (0x hex, 0o/leading-0 octal, 0b binary,
 * otherwise decimal).
 */
function parseIntegerBase0(input: string, original: string): number {
  if (input === "") {
    throw new Error(`invalid size "${original}"`);
  }

  let body = input;
  let negative = false;
  if (body[0] === "+" || body[0] === "-") {
    negative = body[0] === "-";
    body = body.slice(1);
  }

  let radix = 10;
  if (/^0[xX]/.test(body)) {
    radix = 16;
    body = body.slice(2);
  } else if (/^0[bB]/.test(body)) {
    radix = 2;
    body = body.slice(2);
  } else if (/^0[oO]/.test(body)) {
    radix = 8;
    body = body.slice(2);
  } else if (body.length > 1 && body[0] === "0") {
    radix = 8;
    body = body.slice(1);
  }

  const digits =
    radix === 16
      ? /^[0-9a-fA-F]+$/
      : radix === 8
        ? /^[0-7]+$/
        : radix === 2
          ? /^[01]+$/
          : /^[0-9]+$/;
  if (body === "" || !digits.test(body)) {
    throw new Error(`invalid size "${original}"`);
  }

  const value = Number.parseInt(body, radix);
  if (!Number.isFinite(value)) {
    throw new Error(`invalid size "${original}"`);
  }
  return negative ? -value : value;
}
