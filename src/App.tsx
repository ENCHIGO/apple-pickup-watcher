import { useEffect, useMemo, useState, useSyncExternalStore } from "react";
import {
  AlertTriangle,
  BellRing,
  Download,
  Pause,
  Play,
  Plus,
  RefreshCw,
  Trash2,
  X,
} from "lucide-react";

import { Combobox } from "@/components/Combobox";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Separator } from "@/components/ui/separator";
import { Switch } from "@/components/ui/switch";
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
  changeLocale,
  connect,
  dismissUpdate,
  installUpdate,
  refreshProducts,
  saveSettings,
  setCategory,
  setIntervalSeconds,
  setTargets,
  startWatching,
  stopWatching,
  testNotify,
  watcherStore,
} from "@/lib/store";
import {
  type Availability,
  type Category,
  describeAvailability,
  formatTime,
  isUntrusted,
  type StatusTone,
  type Target,
  targetKey,
} from "@/lib/types";

/** 四种展示状态各自的样式。 */
const TONE_CLASS: Record<StatusTone, string> = {
  inStock: "bg-in-stock/15 text-in-stock border-in-stock/30 font-medium",
  outOfStock: "bg-muted text-muted-foreground border-transparent",
  // 「未知」必须和「无货」长得完全不一样。这是整个项目的意义所在：上游把查询
  // 失败显示成「无货」，用户对着一个早已失效的程序空等了大半年。
  unknown: "bg-unknown/15 text-unknown border-unknown/40 font-medium",
  pending: "bg-transparent text-muted-foreground/60 border-dashed",
};

function StatusBadge({ availability }: { availability: Availability }) {
  const { label, tone, detail } = describeAvailability(availability);
  const badge = (
    <Badge variant="outline" className={`min-w-18 justify-center ${TONE_CLASS[tone]}`}>
      {label}
    </Badge>
  );
  if (!detail) return badge;
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className="cursor-help">{badge}</span>
      </TooltipTrigger>
      <TooltipContent className="max-w-90">{detail}</TooltipContent>
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

  const [storeNumber, setStoreNumber] = useState("");
  const [partNumber, setPartNumber] = useState("");
  const [barkDraft, setBarkDraft] = useState("");
  const [intervalDraft, setIntervalDraft] = useState<number | null>(null);

  // 设置从后端载入之前，输入框用后端的值做初值；之后由用户的草稿接管。
  const barkValue = barkDraft || ui.settings.barkUrl;
  const intervalValue = intervalDraft ?? ui.settings.intervalSeconds;

  const storeOptions = useMemo(
    () => ui.stores.map((s) => ({ value: s.number, label: s.title })),
    [ui.stores],
  );
  // 只列当前品类。四个品类的型号加起来好几百条，全堆进一个下拉框，
  // 想找一台 Mac 得先划过所有 iPhone。
  const productOptions = useMemo(
    () =>
      ui.products
        .filter((p) => p.category === ui.category)
        .map((p) => ({ value: p.partNumber, label: p.title })),
    [ui.products, ui.category],
  );

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

  const canAdd = storeNumber !== "" && partNumber !== "";

  async function onAdd() {
    if (!canAdd) return;
    const store = ui.stores.find((s) => s.number === storeNumber);
    const product = ui.products.find((p) => p.partNumber === partNumber);
    if (!store || !product) return;

    const next: Target = {
      locale: ui.settings.locale,
      storeNumber: store.number,
      storeTitle: store.title,
      partNumber: product.partNumber,
      productName: product.title,
    };
    if (targets.some((t) => targetKey(t) === targetKey(next))) return;
    await setTargets([...targets, next]);
    setPartNumber("");
  }

  async function onRemove(t: Target) {
    await setTargets(targets.filter((x) => targetKey(x) !== targetKey(t)));
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
          <div className="flex items-center gap-3">
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

        {ui.update !== null && (
          // 只提示，不自作主张安装。用户可能正等着抢购，被强制重启是灾难。
          <Alert>
            <Download />
            <AlertTitle>有新版本 {ui.update.version}</AlertTitle>
            <AlertDescription>
              <span>当前版本 {ui.update.currentVersion}。安装后需重启应用生效。</span>
              <div className="mt-2 flex items-center gap-2">
                <Button
                  size="sm"
                  disabled={ui.installing}
                  onClick={() => void installUpdate()}
                >
                  {ui.installing ? "正在下载…" : "下载并安装"}
                </Button>
                <Button size="sm" variant="ghost" onClick={dismissUpdate}>
                  <X /> 稍后
                </Button>
              </div>
            </AlertDescription>
          </Alert>
        )}

        <section className="flex flex-wrap items-end gap-3">
          <div className="grid gap-1.5">
            <Label>地区</Label>
            <Combobox
              className="w-36"
              options={ui.regions.map((r) => ({ value: r.locale, label: r.title }))}
              value={ui.settings.locale}
              onChange={(locale) => {
                // 换地区后旧的门店和型号都不再适用，清掉待添加的选择。
                setStoreNumber("");
                setPartNumber("");
                void changeLocale(locale);
              }}
              placeholder="选择地区"
              searchPlaceholder="搜索地区…"
              emptyText="没有匹配的地区"
            />
          </div>

          <div className="grid gap-1.5">
            <Label>品类</Label>
            <Combobox
              className="w-36"
              options={ui.categories.map((c) => ({ value: c.value, label: c.title }))}
              value={ui.category}
              onChange={(value) => {
                // 换品类后旧的型号不再在下拉框里，清掉待添加的选择。门店不用清，
                // 它跟品类无关。
                setPartNumber("");
                setCategory(value as Category);
              }}
              placeholder="选择品类"
              searchPlaceholder="搜索品类…"
              emptyText="没有匹配的品类"
              disabled={ui.categories.length === 0}
            />
          </div>

          <div className="grid gap-1.5">
            <Label>门店</Label>
            <Combobox
              className="w-56"
              options={storeOptions}
              value={storeNumber}
              onChange={setStoreNumber}
              placeholder="选择自提门店"
              searchPlaceholder="搜索门店…"
              emptyText="没有匹配的门店"
              disabled={storeOptions.length === 0}
            />
          </div>

          <div className="grid gap-1.5">
            <Label>型号</Label>
            <Combobox
              className="w-80"
              options={productOptions}
              value={partNumber}
              onChange={setPartNumber}
              placeholder="选择型号"
              searchPlaceholder="搜索型号…"
              emptyText="没有匹配的型号"
              disabled={productOptions.length === 0}
            />
          </div>

          <Button variant="secondary" onClick={() => void onAdd()} disabled={!canAdd}>
            <Plus /> 添加
          </Button>

          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                variant="ghost"
                size="icon"
                aria-label="从 Apple 官网更新当前品类的型号列表"
                disabled={ui.refreshing}
                onClick={() => void refreshProducts()}
              >
                <RefreshCw className={ui.refreshing ? "animate-spin" : undefined} />
              </Button>
            </TooltipTrigger>
            <TooltipContent>
              从 Apple 官网更新当前品类的型号列表。新机发布后用这个，不必等程序更新。
            </TooltipContent>
          </Tooltip>
        </section>

        <section className="flex flex-wrap items-end gap-4">
          <div className="grid gap-1.5">
            <Label htmlFor="interval">查询间隔（秒）</Label>
            <Input
              id="interval"
              type="number"
              min={5}
              className="w-28 select-text"
              value={intervalValue}
              onChange={(e) => setIntervalDraft(e.target.valueAsNumber)}
              onBlur={() => {
                const s = Number.isFinite(intervalValue) ? Math.round(intervalValue) : 30;
                setIntervalDraft(null);
                void setIntervalSeconds(s);
              }}
            />
          </div>

          <div className="grid flex-1 gap-1.5">
            <Label htmlFor="bark">Bark 推送地址（留空则不推送）</Label>
            <Input
              id="bark"
              className="select-text"
              placeholder="https://api.day.app/你的BarkKey"
              value={barkValue}
              onChange={(e) => setBarkDraft(e.target.value)}
              onBlur={() => {
                setBarkDraft("");
                void saveSettings({ ...ui.settings, barkUrl: barkValue.trim() });
              }}
            />
          </div>

          <div className="flex items-center gap-2 pb-2">
            <Switch
              id="sound"
              checked={ui.settings.soundEnabled}
              onCheckedChange={(v) =>
                void saveSettings({ ...ui.settings, soundEnabled: v })
              }
            />
            <Label htmlFor="sound">提示音</Label>
          </div>

          <div className="flex items-center gap-2 pb-2">
            <Switch
              id="openbag"
              checked={ui.settings.openBagOnHit}
              onCheckedChange={(v) =>
                void saveSettings({ ...ui.settings, openBagOnHit: v })
              }
            />
            <Label htmlFor="openbag">有货时打开购物袋</Label>
          </div>

          <Button variant="ghost" className="pb-2" onClick={() => void testNotify()}>
            <BellRing /> 测试提醒
          </Button>
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
                      {ui.ready
                        ? "还没有监控目标。选好门店和型号后点「添加」。"
                        : "正在载入…"}
                    </TableCell>
                  </TableRow>
                ) : (
                  ui.rows.map((row) => (
                    <TableRow key={targetKey(row.target)}>
                      <TableCell>
                        <StatusBadge availability={row.availability} />
                      </TableCell>
                      <TableCell className="font-medium">{row.target.storeTitle}</TableCell>
                      <TableCell className="text-muted-foreground">
                        {row.target.productName}
                      </TableCell>
                      <TableCell className="text-muted-foreground tabular-nums">
                        {formatTime(row.lastCheckedMs)}
                      </TableCell>
                      <TableCell>
                        <Button
                          variant="ghost"
                          size="icon"
                          aria-label="删除这条监控"
                          onClick={() => void onRemove(row.target)}
                        >
                          <Trash2 />
                        </Button>
                      </TableCell>
                    </TableRow>
                  ))
                )}
              </TableBody>
            </Table>
          </ScrollArea>
        </section>

        <footer className="text-muted-foreground text-sm">
          监控 {ui.rows.length} 项 · 有货 {summary.inStock} · 无货 {summary.outOfStock}
          {summary.untrusted > 0 && (
            // 把「其中多少项查不到」单独点出来：这个数字大于 0 时，
            // 界面上那些「无货」也未必反映真实情况。
            <span className="text-unknown"> · 查不到 {summary.untrusted}</span>
          )}
        </footer>

        <section className="h-36 shrink-0 overflow-hidden rounded-lg border">
          <ScrollArea className="h-full p-3">
            <pre className="text-muted-foreground font-mono text-xs leading-5 whitespace-pre-wrap select-text">
              {ui.logs.length === 0 ? "日志会显示在这里。" : ui.logs.join("\n")}
            </pre>
          </ScrollArea>
        </section>
      </div>
    </TooltipProvider>
  );
}
