import { useEffect, useMemo, useState, useSyncExternalStore } from "react";
import { AlertTriangle, Pause, Play, Plus, Trash2 } from "lucide-react";

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ScrollArea } from "@/components/ui/scroll-area";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";

import {
  connect,
  setInterval as setPollInterval,
  setTargets,
  startWatching,
  stopWatching,
  watcherStore,
} from "@/lib/store";
import {
  describeAvailability,
  formatTime,
  isUntrusted,
  type StatusTone,
  type Target,
} from "@/lib/types";

/** 四种展示状态各自的样式。 */
const TONE_CLASS: Record<StatusTone, string> = {
  inStock: "bg-in-stock/15 text-in-stock border-in-stock/30",
  outOfStock: "bg-muted text-muted-foreground border-transparent",
  // 「未知」必须和「无货」长得完全不一样。这是整个项目的意义所在：
  // 上游把查询失败显示成「无货」，用户对着一个早已失效的程序空等了大半年。
  unknown: "bg-unknown/15 text-unknown border-unknown/40",
  pending: "bg-transparent text-muted-foreground/60 border-dashed border-border",
};

function StatusBadge({ state }: { state: Parameters<typeof describeAvailability>[0] }) {
  const { label, tone, detail } = describeAvailability(state);
  const badge = (
    <Badge variant="outline" className={`min-w-16 justify-center ${TONE_CLASS[tone]}`}>
      {label}
    </Badge>
  );
  if (!detail) return badge;
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className="cursor-help">{badge}</span>
      </TooltipTrigger>
      <TooltipContent className="max-w-80">{detail}</TooltipContent>
    </Tooltip>
  );
}

export default function App() {
  const ui = useSyncExternalStore(watcherStore.subscribe, watcherStore.getSnapshot);

  useEffect(() => {
    void connect();
    // 刻意不在清理函数里断开：这是应用级的单一连接，窗口活着它就该活着。
    // StrictMode 的重复调用由 connect 内部去重。
  }, []);

  const [locale, setLocale] = useState("zh_CN");
  const [storeNumber, setStoreNumber] = useState("");
  const [partNumber, setPartNumber] = useState("");
  const [seconds, setSeconds] = useState(30);

  const targets = useMemo(() => ui.rows.map((r) => r.target), [ui.rows]);

  const summary = useMemo(() => {
    let inStock = 0;
    let outOfStock = 0;
    let untrusted = 0;
    for (const r of ui.rows) {
      if (r.availability.kind === "in_stock") inStock += 1;
      else if (r.availability.kind === "out_of_stock") outOfStock += 1;
      if (isUntrusted(r.availability)) untrusted += 1;
    }
    return { inStock, outOfStock, untrusted };
  }, [ui.rows]);

  const canAdd = storeNumber.trim() !== "" && partNumber.trim() !== "";

  async function onAdd() {
    if (!canAdd) return;
    const next: Target = {
      locale,
      storeNumber: storeNumber.trim().toUpperCase(),
      storeTitle: storeNumber.trim().toUpperCase(),
      partNumber: partNumber.trim().toUpperCase(),
      productName: partNumber.trim().toUpperCase(),
    };
    const key = (t: Target) => `${t.locale}|${t.storeNumber}|${t.partNumber}`;
    if (targets.some((t) => key(t) === key(next))) return;
    await setTargets([...targets, next]);
    setPartNumber("");
  }

  async function onRemove(t: Target) {
    const key = (x: Target) => `${x.locale}|${x.storeNumber}|${x.partNumber}`;
    await setTargets(targets.filter((x) => key(x) !== key(t)));
  }

  async function onIntervalCommit() {
    const s = Number.isFinite(seconds) ? Math.max(5, Math.round(seconds)) : 30;
    setSeconds(s);
    await setPollInterval(s);
  }

  return (
    <TooltipProvider delayDuration={200}>
      <div className="mx-auto flex h-screen max-w-5xl flex-col gap-4 p-6">
        <header className="flex items-center justify-between">
          <div>
            <h1 className="text-xl font-semibold tracking-tight">Apple Pickup Watcher</h1>
            <p className="text-muted-foreground text-sm">
              盯 Apple 直营店的到店取货库存，有货立刻提醒
            </p>
          </div>
          <div className="flex items-center gap-2">
            <span className="text-muted-foreground text-sm">
              {ui.running ? "监听中" : "已暂停"}
            </span>
            {ui.running ? (
              <Button variant="secondary" onClick={() => void stopWatching()}>
                <Pause /> 暂停
              </Button>
            ) : (
              <Button onClick={() => void startWatching()} disabled={ui.rows.length === 0}>
                <Play /> 开始
              </Button>
            )}
          </div>
        </header>

        {ui.trouble !== null && (
          <Alert variant="destructive">
            <AlertTriangle />
            <AlertTitle>监控当前不可信</AlertTitle>
            <AlertDescription>
              {ui.trouble}
              <span className="mt-1 block">
                此时列表里的状态不代表门店的真实库存，请先排查原因，不要干等。
              </span>
            </AlertDescription>
          </Alert>
        )}

        <section className="flex flex-wrap items-end gap-3">
          <div className="grid gap-1.5">
            <Label htmlFor="region">地区</Label>
            <Select value={locale} onValueChange={setLocale}>
              <SelectTrigger id="region" className="w-36">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {ui.regions.map((r) => (
                  <SelectItem key={r.locale} value={r.locale}>
                    {r.title}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          {/* 商品与门店目录还没接上，暂时手填编号。目录一落地就换成可搜索下拉。 */}
          <div className="grid gap-1.5">
            <Label htmlFor="store">门店编号</Label>
            <Input
              id="store"
              className="w-36 select-text"
              placeholder="R683"
              value={storeNumber}
              onChange={(e) => setStoreNumber(e.target.value)}
            />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="part">零件号</Label>
            <Input
              id="part"
              className="w-44 select-text"
              placeholder="MG724CH/A"
              value={partNumber}
              onChange={(e) => setPartNumber(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && void onAdd()}
            />
          </div>
          <Button variant="secondary" onClick={() => void onAdd()} disabled={!canAdd}>
            <Plus /> 添加
          </Button>

          <div className="grid gap-1.5">
            <Label htmlFor="interval">查询间隔（秒）</Label>
            <Input
              id="interval"
              type="number"
              min={5}
              className="w-28 select-text"
              value={seconds}
              onChange={(e) => setSeconds(e.target.valueAsNumber)}
              onBlur={() => void onIntervalCommit()}
            />
          </div>
        </section>

        <Separator />

        <section className="min-h-0 flex-1 overflow-hidden rounded-lg border">
          <ScrollArea className="h-full">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-24">状态</TableHead>
                  <TableHead>门店</TableHead>
                  <TableHead>型号</TableHead>
                  <TableHead className="w-24">最后检查</TableHead>
                  <TableHead className="w-12" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {ui.rows.length === 0 ? (
                  <TableRow>
                    <TableCell colSpan={5} className="text-muted-foreground h-24 text-center">
                      还没有监控目标。填入门店编号和零件号后点「添加」。
                    </TableCell>
                  </TableRow>
                ) : (
                  ui.rows.map((row) => {
                    const t = row.target;
                    return (
                      <TableRow key={`${t.locale}|${t.storeNumber}|${t.partNumber}`}>
                        <TableCell>
                          <StatusBadge state={row.availability} />
                        </TableCell>
                        <TableCell className="font-medium">{t.storeTitle}</TableCell>
                        <TableCell className="text-muted-foreground">
                          {t.productName}
                        </TableCell>
                        <TableCell className="text-muted-foreground tabular-nums">
                          {formatTime(row.lastCheckedMs)}
                        </TableCell>
                        <TableCell>
                          <Button
                            variant="ghost"
                            size="icon"
                            aria-label="删除这条监控"
                            onClick={() => void onRemove(t)}
                          >
                            <Trash2 />
                          </Button>
                        </TableCell>
                      </TableRow>
                    );
                  })
                )}
              </TableBody>
            </Table>
          </ScrollArea>
        </section>

        <footer className="text-muted-foreground flex items-center justify-between text-sm">
          <span>
            监控 {ui.rows.length} 项 · 有货 {summary.inStock} · 无货 {summary.outOfStock}
            {summary.untrusted > 0 && (
              // 把「其中多少项查不到」单独点出来：这个数字大于 0 时，
              // 界面上那些「无货」也未必反映真实情况。
              <span className="text-unknown"> · 查不到 {summary.untrusted}</span>
            )}
          </span>
        </footer>

        <section className="h-40 shrink-0 overflow-hidden rounded-lg border">
          <ScrollArea className="h-full p-3">
            <pre className="text-muted-foreground select-text font-mono text-xs leading-5 whitespace-pre-wrap">
              {ui.logs.length === 0 ? "日志会显示在这里。" : ui.logs.join("\n")}
            </pre>
          </ScrollArea>
        </section>
      </div>
    </TooltipProvider>
  );
}
