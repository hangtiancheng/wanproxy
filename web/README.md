# WANProxy.go — Web Dashboard

React dashboard for the wanproxy.go WAN optimization proxy demo. It visualizes
the live proxy fleet, runs multi-arm A/B experiments and protocol benchmarks
(HTTP / SOCKS5 / SSH), drives file transfers through selectable proxy chains,
and inspects XCodec frame composition with the original C++ toolchain.

## Pages

| Route         | Page                    | Purpose                                                        |
| ------------- | ----------------------- | -------------------------------------------------------------- |
| `/`           | Overview                | Live fleet status, demo topology, codec wire/app byte ratios   |
| `/experiment` | A/B Experiment          | Multi-arm experiments with presets, warm-up curve and verdicts |
| `/files`      | File Transfers          | Upload/download through a proxy chain, generate test files     |
| `/scenarios`  | Protocol Benchmarks     | Server-side wanbench runs per scenario with wire-byte compare  |
| `/inspector`  | XCodec Stream Inspector | `tack -c` / `tack -p` / `xcdump -v` analysis of stored files   |

## Development

```bash
pnpm install
pnpm dev        # http://localhost:5173
```

The dashboard expects the Go SRV on `http://127.0.0.1:3101`
(`cd ../srv && go run ./cmd/server`), which spawns the wanproxy.go demo
fleet and exposes the monitor/file/bench/experiment APIs. Without it, pages
render their offline states.

```bash
pnpm build      # tsc -b + vite build (vendor chunks split via advancedChunks)
pnpm lint       # eslint (flat config; ui/ components exempt from fast-refresh rule)
pnpm preview    # serve the production build
```

## Theming

Colors are semantic tokens defined in `src/index.css` (`:root` / `.dark`):
`primary` (blue identity), `success` / `warning` / `info` status colors, and
`chart-1..5` for data visualization (wire-byte bars, frame composition).
Extend shadcn components by editing their `cva` variants in
`src/components/ui`, and add new ones with:

```bash
pnpm dlx shadcn@latest add <component>
```
