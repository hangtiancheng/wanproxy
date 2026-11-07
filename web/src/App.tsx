import { NavLink, Route, Routes, useLocation } from "react-router-dom";
import {
  Activity,
  ArrowLeftRight,
  ChevronRight,
  FlaskConical,
  Gauge,
  LayoutDashboard,
  Microscope,
  Server,
  Waypoints,
  Zap,
} from "lucide-react";
import { ThemeToggle } from "@/components/theme-toggle";
import { Separator } from "@/components/ui/separator";
import {
  Sidebar,
  SidebarContent,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarInset,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarProvider,
  SidebarRail,
  SidebarTrigger,
} from "@/components/ui/sidebar";
import { Dashboard } from "@/pages/Dashboard";
import { Experiment } from "@/pages/Experiment";
import { Files } from "@/pages/Files";
import { Scenarios } from "@/pages/Scenarios";
import { Inspector } from "@/pages/Inspector";

const nav = [
  { to: "/", label: "Overview", icon: LayoutDashboard },
  { to: "/experiment", label: "A/B Experiment", icon: FlaskConical },
  { to: "/files", label: "File Transfers", icon: ArrowLeftRight },
  { to: "/scenarios", label: "Protocol Benchmarks", icon: Gauge },
  { to: "/inspector", label: "Stream Inspector", icon: Microscope },
];

const backend = [
  { icon: Server, label: "SRV", value: "srv.go :3101" },
  { icon: Waypoints, label: "Proxy", value: "wanproxy.go daemon" },
  { icon: Activity, label: "Monitor", value: ":9901/status" },
];

export default function App() {
  const location = useLocation();
  const current = nav.find((item) =>
    item.to === "/"
      ? location.pathname === "/"
      : location.pathname.startsWith(item.to),
  );

  return (
    <SidebarProvider>
      <Sidebar collapsible="icon">
        <SidebarHeader>
          <SidebarMenu>
            <SidebarMenuItem>
              <SidebarMenuButton size="lg" render={<NavLink to="/" />}>
                <div className="bg-primary text-primary-foreground flex aspect-square size-8 items-center justify-center rounded-lg">
                  <Zap />
                </div>
                <div className="grid flex-1 text-left text-sm leading-tight">
                  <span className="truncate font-semibold">WANProxy.go</span>
                  <span className="text-muted-foreground truncate text-xs">
                    WAN optimization demo
                  </span>
                </div>
              </SidebarMenuButton>
            </SidebarMenuItem>
          </SidebarMenu>
        </SidebarHeader>
        <SidebarContent>
          <SidebarGroup>
            <SidebarGroupLabel>Monitor</SidebarGroupLabel>
            <SidebarGroupContent>
              <SidebarMenu>
                {nav.map((item) => (
                  <SidebarMenuItem key={item.to}>
                    <SidebarMenuButton
                      tooltip={item.label}
                      isActive={item.to === current?.to}
                      render={<NavLink to={item.to} end={item.to === "/"} />}
                    >
                      <item.icon />
                      <span>{item.label}</span>
                    </SidebarMenuButton>
                  </SidebarMenuItem>
                ))}
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>
          <SidebarGroup className="mt-auto">
            <SidebarGroupContent>
              <div className="bg-muted/60 flex flex-col gap-2 rounded-lg p-3 text-xs">
                {backend.map((row) => (
                  <div key={row.label} className="flex items-center gap-2">
                    <row.icon className="text-muted-foreground size-3.5 shrink-0" />
                    <span className="text-muted-foreground">{row.label}</span>
                    <span className="text-foreground/80 ml-auto truncate font-mono text-[11px]">
                      {row.value}
                    </span>
                  </div>
                ))}
              </div>
            </SidebarGroupContent>
          </SidebarGroup>
        </SidebarContent>
        <SidebarRail />
      </Sidebar>
      <SidebarInset>
        <header className="bg-background/80 sticky top-0 z-10 flex h-14 shrink-0 items-center gap-2 border-b px-4 backdrop-blur-sm">
          <SidebarTrigger className="-ml-1" />
          <Separator orientation="vertical" className="mr-1 h-4" />
          <nav className="text-muted-foreground flex items-center gap-1.5 text-sm">
            <span>WANProxy</span>
            <ChevronRight className="size-3.5" />
            <span className="text-foreground font-medium">
              {current?.label ?? "Overview"}
            </span>
          </nav>
          <div className="ml-auto flex items-center gap-2">
            <ThemeToggle />
          </div>
        </header>
        <div className="min-w-0 flex-1 p-4 lg:p-6">
          <Routes>
            <Route path="/" element={<Dashboard />} />
            <Route path="/experiment" element={<Experiment />} />
            <Route path="/files" element={<Files />} />
            <Route path="/scenarios" element={<Scenarios />} />
            <Route path="/inspector" element={<Inspector />} />
          </Routes>
        </div>
      </SidebarInset>
    </SidebarProvider>
  );
}
