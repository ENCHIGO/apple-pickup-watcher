import { type ReactNode, useEffect, useMemo, useState, useSyncExternalStore } from "react";
import {
  AlertTriangle,
  BellRing,
  ChevronDown,
  ChevronUp,
  Download,
  Pause,
  Play,
  Plus,
  RefreshCw,
  Trash2,
  X,
} from "lucide-react";

import { Combobox } from "@/components/Combobox";
import { MultiCombobox } from "@/components/MultiCombobox";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Switch } from "@/components/ui/switch";
import {
  capacityOptions,
  colorOptions,
  familyOptions,
  keepAvailable,
  productsForSelection,
} from "@/lib/product-selection";
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
  describeAdvice,
  describeAvailability,
  formatTime,
  isUntrusted,
  type Product,
  type Settings,
  type StatusTone,
  type Target,
  targetKey,
} from "@/lib/types";
import { cn } from "@/lib/utils";

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

/**
 * 一块带标题的分区。「在哪儿加什么」和「怎么跑」分开放，控件不再挤成一排。
 *
 * 传 `onToggle` 就可以折叠：标题行整行可点，折叠后只剩标题和一行摘要。
 */
function Panel({
  title,
  hint,
  open = true,
  onToggle,
  children,
}: {
  title: string;
  hint?: string;
  open?: boolean;
  onToggle?: () => void;
  children: ReactNode;
}) {
  const heading = (
    <>
      <h2 className="shrink-0 text-sm font-medium">{title}</h2>
      {hint !== undefined && (
        <p className="text-muted-foreground min-w-0 flex-1 truncate text-xs" title={hint}>
          {hint}
        </p>
      )}
      {onToggle !== undefined &&
        (open ? (
          <ChevronUp className="text-muted-foreground size-4 shrink-0" />
        ) : (
          <ChevronDown className="text-muted-foreground size-4 shrink-0" />
        ))}
    </>
  );
  return (
    <section className="bg-card text-card-foreground rounded-xl border shadow-xs">
      {onToggle !== undefined ? (
        <button
          type="button"
          aria-expanded={open}
          onClick={onToggle}
          className={cn(
            "hover:bg-muted/40 flex w-full items-center gap-3 px-4 py-2 text-left",
            open ? "rounded-t-xl" : "rounded-xl",
          )}
        >
          {heading}
        </button>
      ) : (
        <div className="flex items-center gap-3 px-4 py-2">{heading}</div>
      )}
      {open && <div className="grid gap-2.5 border-t px-4 pt-3 pb-4">{children}</div>}
    </section>
  );
}

const SETTINGS_OPEN_KEY = "apw.settingsOpen";

/** 上次是展开还是收起。读不到（首次启动、无 localStorage）按展开。 */
function readSettingsOpen(): boolean {
  try {
    return localStorage.getItem(SETTINGS_OPEN_KEY) !== "0";
  } catch {
    return true;
  }
}

/** 收起时的一行摘要：设置折起来了，但它们是什么一眼还在。 */
function settingsSummary(s: Settings): string {
  return [
    `每 ${s.intervalSeconds} 秒查一轮`,
    s.barkUrl.trim() === "" ? "Bark 未配置" : "Bark 已配置",
    (s.proxies ?? []).length > 0 ? `代理 ${s.proxies.length} 条` : "无代理",
    s.soundEnabled ? "提示音开" : "提示音关",
    s.openBagOnHit ? "有货时开购物袋" : "有货时不开购物袋",
  ].join(" · ");
}

/** 运行状态。一个会呼吸的点比一行灰字更像「它还活着」。 */
function RunState({ running }: { running: boolean }) {
  return (
    <span className="text-muted-foreground flex items-center gap-2 text-sm">
      <span className="relative flex size-2">
        {running && (
          <span className="bg-in-stock/60 absolute inline-flex size-full animate-ping rounded-full [animation-duration:2.4s]" />
        )}
        <span
          className={cn(
            "relative inline-flex size-2 rounded-full",
            running ? "bg-in-stock" : "bg-muted-foreground/40",
          )}
        />
      </span>
      {running ? "监听中" : "已暂停"}
    </span>
  );
}

/** 一个统计数字。数字等宽：每轮查询都会变，几个并排时不该来回抖。 */
function Stat({
  label,
  value,
  tone,
}: {
  label: string;
  value: number;
  tone?: "inStock" | "unknown";
}) {
  return (
    <span className="bg-card inline-flex items-baseline gap-1.5 rounded-md border px-2.5 py-1 text-sm shadow-xs">
      <span className="text-muted-foreground text-xs">{label}</span>
      <span
        className={cn(
          "font-semibold tabular-nums",
          value > 0 && tone === "inStock" && "text-in-stock",
          value > 0 && tone === "unknown" && "text-unknown",
        )}
      >
        {value}
      </span>
    </span>
  );
}

/** 日志行。时间戳弱化，到货那几行用有货色标出来，一屏灰字里一眼能找到。 */
function LogLine({ line }: { line: string }) {
  const match = /^\[(\d{2}:\d{2}:\d{2})\] ?(.*)$/s.exec(line);
  if (!match) return <div className="whitespace-pre-wrap">{line}</div>;
  const [, stamp, text = ""] = match;
  return (
    <div className="whitespace-pre-wrap">
      <span className="text-muted-foreground/60">{stamp}</span>{" "}
      <span className={text.startsWith("有货") ? "text-in-stock font-medium" : "text-muted-foreground"}>
        {text}
      </span>
    </div>
  );
}

export default function App() {
  const ui = useSyncExternalStore(watcherStore.subscribe, watcherStore.getSnapshot);

  useEffect(() => {
    void connect();
    // 刻意不在清理函数里断开：这是应用级的单一连接，窗口活着它就该活着。
    // StrictMode 的重复调用由 connect 内部去重。
  }, []);

  // 门店可以一次选好几家：同城门店合并成一次请求之后，多选不增加请求量。
  const [storeNumbers, setStoreNumbers] = useState<string[]>([]);
  // 非 iPhone 品类直接选一个完整型号。
  const [partNumber, setPartNumber] = useState("");
  // iPhone 按机型 → 容量 → 颜色三级选择，三级都可多选：发售当晚常见的是
  // 「这两款、这三个颜色，哪个有货要哪个」，一台一台加太慢。三级组合里目录中
  // 存在的型号全部加入。
  const [families, setFamilies] = useState<string[]>([]);
  const [capacities, setCapacities] = useState<string[]>([]);
  const [colors, setColors] = useState<string[]>([]);
  const [barkDraft, setBarkDraft] = useState<string | null>(null);
  const [proxiesDraft, setProxiesDraft] = useState<string | null>(null);
  const [intervalDraft, setIntervalDraft] = useState<number | null>(null);
  // 设置改一次就放着不动，折起来把纵向空间还给表格；记住上次的选择。
  const [settingsOpen, setSettingsOpen] = useState(readSettingsOpen());
  function toggleSettings() {
    const next = !settingsOpen;
    setSettingsOpen(next);
    try {
      localStorage.setItem(SETTINGS_OPEN_KEY, next ? "1" : "0");
    } catch {
      // 存不下就每次都展开，不值得报错。
    }
  }

  // null 表示尚未编辑；空字符串是用户明确清空，不能退回已保存的地址。
  const barkValue = barkDraft ?? ui.settings.barkUrl;
  // 旧设置文件没有 proxies 字段，读上来是 undefined，当作空列表。
  const proxiesValue = proxiesDraft ?? (ui.settings.proxies ?? []).join(";");
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
  const iphoneFamilyOptions = useMemo(
    () => familyOptions(ui.products, ui.category),
    [ui.products, ui.category],
  );
  const iphoneCapacityOptions = useMemo(
    () => capacityOptions(ui.products, ui.category, families),
    [ui.products, ui.category, families],
  );
  const iphoneColorOptions = useMemo(
    () => colorOptions(ui.products, ui.category, families, capacities),
    [ui.products, ui.category, families, capacities],
  );
  // 点「添加」时会加入的型号。iPhone 是三级选择的组合，其他品类就是选中的那一个。
  const selectedProducts = useMemo<Product[]>(() => {
    if (ui.category === "iphone") {
      return productsForSelection(ui.products, ui.category, families, capacities, colors);
    }
    const product = ui.products.find((p) => p.partNumber === partNumber);
    return product ? [product] : [];
  }, [ui.products, ui.category, families, capacities, colors, partNumber]);

  function resetProductSelection() {
    setPartNumber("");
    setFamilies([]);
    setCapacities([]);
    setColors([]);
  }

  // 上级变了，下级只去掉不再可选的项，仍然成立的留着（理由见 keepAvailable）。
  function onFamiliesChange(next: string[]) {
    setFamilies(next);
    const nextCapacities = keepAvailable(
      capacities,
      capacityOptions(ui.products, ui.category, next),
    );
    setCapacities(nextCapacities);
    setColors(
      keepAvailable(colors, colorOptions(ui.products, ui.category, next, nextCapacities)),
    );
  }

  function onCapacitiesChange(next: string[]) {
    setCapacities(next);
    setColors(keepAvailable(colors, colorOptions(ui.products, ui.category, families, next)));
  }

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

  const canAdd = storeNumbers.length > 0 && selectedProducts.length > 0;

  async function onAdd() {
    if (!canAdd) return;

    // 门店 × 型号，每个组合一条目标；已经在列表里的跳过，不重复。
    // 按门店分组排，表格里同一家店的几台机器挨在一起。
    const existing = new Set(targets.map(targetKey));
    const added: Target[] = [];
    for (const number of storeNumbers) {
      const store = ui.stores.find((s) => s.number === number);
      if (!store) continue;
      for (const product of selectedProducts) {
        const next: Target = {
          locale: ui.settings.locale,
          storeNumber: store.number,
          storeTitle: store.title,
          partNumber: product.partNumber,
          productName: product.title,
          ...(product.companionPart ? { companionPart: product.companionPart } : {}),
        };
        const key = targetKey(next);
        if (existing.has(key)) continue;
        existing.add(key);
        added.push(next);
      }
    }
    if (added.length === 0) return;
    await setTargets([...targets, ...added]);
    // 只清型号，门店留着：接着给同一批门店加下一个型号是最常见的操作。
    resetProductSelection();
  }

  async function onRemove(t: Target) {
    await setTargets(targets.filter((x) => targetKey(x) !== targetKey(t)));
  }

  return (
    <TooltipProvider delayDuration={200}>
      <div className="mx-auto flex h-screen max-w-6xl flex-col gap-3 p-5">
        <header className="flex items-start justify-between gap-4">
          <div>
            <h1 className="text-xl font-semibold tracking-tight">Apple Pickup Watcher</h1>
            <p className="text-muted-foreground text-sm">
              盯 Apple 直营店的到店取货库存，有货立刻提醒
            </p>
          </div>
          <div className="flex items-center gap-4">
            <RunState running={ui.running} />
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
          // 只有标题是红的。原因和建议各占一行、用正常的字色：三段红字堆在一起，
          // 用户反而分不清哪句是原因、哪句是该做的事。
          <div
            role="alert"
            className="border-destructive/30 bg-destructive/5 border-l-destructive grid gap-1.5 rounded-lg border border-l-4 px-4 py-3 text-sm"
          >
            <p className="text-destructive flex flex-wrap items-center gap-x-2 font-medium">
              <AlertTriangle className="size-4" />
              监控当前不可信
              <span className="text-muted-foreground font-normal">
                恢复之前，表格里的状态不代表门店的真实库存
              </span>
            </p>
            <p className="pl-6">{ui.trouble.reason}</p>
            {ui.trouble.advice !== null && (
              // 用户自己能做的那件事单独拎出来，而且要短：issue #3 里那位对着反复
              // 告警的窗口干等了三个小时，缺的不是解释，是一句「换条网络」。
              <p className="text-muted-foreground pl-6">
                <span className="text-foreground font-medium">建议</span>{" "}
                {describeAdvice(ui.trouble.advice)}
              </p>
            )}
          </div>
        )}

        {ui.update !== null && (
          // 只提示，不自作主张安装。用户可能正等着抢购，被强制重启是灾难。
          <div className="bg-card flex flex-wrap items-center gap-x-4 gap-y-2 rounded-lg border px-4 py-2.5 text-sm shadow-xs">
            <p className="flex items-center gap-2 font-medium">
              <Download className="size-4" /> 有新版本 {ui.update.version}
            </p>
            <p className="text-muted-foreground">
              当前 {ui.update.currentVersion}，安装后需重启应用生效。
            </p>
            <div className="ml-auto flex items-center gap-2">
              <Button size="sm" disabled={ui.installing} onClick={() => void installUpdate()}>
                {ui.installing ? "正在下载…" : "下载并安装"}
              </Button>
              <Button size="sm" variant="ghost" onClick={dismissUpdate}>
                <X /> 稍后
              </Button>
            </div>
          </div>
        )}

        <Panel
          title="添加监控目标"
          hint="不同品类、不同门店可以混着加；门店和 iPhone 的机型、容量、颜色都可多选，组合一次全部加入"
        >
          <div className="flex flex-wrap items-end gap-3">
            <div className="grid gap-1.5">
              <Label>地区</Label>
              <Combobox
                className="w-36"
                options={ui.regions.map((r) => ({ value: r.locale, label: r.title }))}
                value={ui.settings.locale}
                onChange={(locale) => {
                  // 换地区后旧的门店和型号都不再适用，清掉待添加的选择。
                  setStoreNumbers([]);
                  resetProductSelection();
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
                  resetProductSelection();
                  setCategory(value as Category);
                }}
                placeholder="选择品类"
                searchPlaceholder="搜索品类…"
                emptyText="没有匹配的品类"
                disabled={ui.categories.length === 0}
              />
            </div>

            <div className="grid gap-1.5">
              <Label>门店（可多选）</Label>
              <MultiCombobox
                className="w-64"
                options={storeOptions}
                values={storeNumbers}
                onChange={setStoreNumbers}
                placeholder="选择自提门店"
                searchPlaceholder="搜索门店…"
                emptyText="没有匹配的门店"
                countLabel={(n) => `已选 ${n} 家门店`}
                disabled={storeOptions.length === 0}
              />
            </div>
          </div>

          <div className="flex flex-wrap items-end gap-3">
            {ui.category === "iphone" ? (
              <>
                <div className="grid gap-1.5">
                  <Label>机型（可多选）</Label>
                  <MultiCombobox
                    className="w-48"
                    options={iphoneFamilyOptions}
                    values={families}
                    onChange={onFamiliesChange}
                    placeholder="选择机型"
                    searchPlaceholder="搜索机型…"
                    emptyText="没有匹配的机型"
                    countLabel={(n) => `已选 ${n} 款机型`}
                    disabled={iphoneFamilyOptions.length === 0}
                  />
                </div>

                <div className="grid gap-1.5">
                  <Label>容量（可多选）</Label>
                  <MultiCombobox
                    className="w-36"
                    options={iphoneCapacityOptions}
                    values={capacities}
                    onChange={onCapacitiesChange}
                    placeholder="选择容量"
                    searchPlaceholder="搜索容量…"
                    emptyText="没有匹配的容量"
                    countLabel={(n) => `已选 ${n} 种容量`}
                    disabled={families.length === 0 || iphoneCapacityOptions.length === 0}
                  />
                </div>

                <div className="grid gap-1.5">
                  <Label>颜色（可多选）</Label>
                  <MultiCombobox
                    className="w-40"
                    options={iphoneColorOptions}
                    values={colors}
                    onChange={setColors}
                    placeholder="选择颜色"
                    searchPlaceholder="搜索颜色…"
                    emptyText="没有匹配的颜色"
                    countLabel={(n) => `已选 ${n} 种颜色`}
                    disabled={capacities.length === 0 || iphoneColorOptions.length === 0}
                  />
                </div>
              </>
            ) : (
              <div className="grid gap-1.5">
                <Label>型号</Label>
                <Combobox
                  className="w-96"
                  options={productOptions}
                  value={partNumber}
                  onChange={setPartNumber}
                  placeholder="选择型号"
                  searchPlaceholder="搜索型号…"
                  emptyText="没有匹配的型号"
                  disabled={productOptions.length === 0}
                />
              </div>
            )}

            <Button variant="secondary" onClick={() => void onAdd()} disabled={!canAdd}>
              <Plus /> 添加
            </Button>
            {selectedProducts.length > 1 && (
              // 多选时把要加的规模点明：三级各勾几项之后，组合数并不直观。
              <span
                className="text-muted-foreground pb-2 text-sm"
                title={selectedProducts.map((p) => p.title).join("、")}
              >
                {selectedProducts.length} 个型号 × {storeNumbers.length} 家门店
              </span>
            )}

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
          </div>
        </Panel>

        <Panel
          title="运行设置"
          hint={settingsOpen ? "改动即时保存" : settingsSummary(ui.settings)}
          open={settingsOpen}
          onToggle={toggleSettings}
        >
          <div className="flex flex-wrap items-end gap-x-4 gap-y-3">
            <div className="grid gap-1.5">
              <Label htmlFor="interval" className="whitespace-nowrap">
                查询间隔（秒）
              </Label>
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

            <div className="grid min-w-64 flex-1 gap-1.5">
              <Label htmlFor="bark" className="whitespace-nowrap">
                Bark 推送地址
              </Label>
              <Input
                id="bark"
                className="select-text"
                placeholder="https://api.day.app/你的Key，多个用分号分隔，留空不推送"
                value={barkValue}
                onChange={(e) => setBarkDraft(e.target.value)}
                onBlur={() => {
                  setBarkDraft(null);
                  void saveSettings({ ...ui.settings, barkUrl: barkValue.trim() });
                }}
              />
            </div>

            <div className="grid min-w-64 flex-1 gap-1.5">
              <Label htmlFor="proxies" className="whitespace-nowrap">
                代理地址（可选）
              </Label>
              <Input
                id="proxies"
                className="select-text"
                placeholder="http://user:pass@host:port;socks5://host:port，多个用分号分隔"
                value={proxiesValue}
                onChange={(e) => setProxiesDraft(e.target.value)}
                onBlur={() => {
                  setProxiesDraft(null);
                  // 每个代理是一条额外的出口线路：Apple 的配额按出口 IP 计，
                  // 多一条线路多一份配额，被拦时自动换下一条。后端会再校验一遍。
                  const proxies = proxiesValue
                    .split(/[;\s]+/)
                    .map((p) => p.trim())
                    .filter((p) => p !== "");
                  void saveSettings({ ...ui.settings, proxies });
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

            <Button variant="ghost" size="sm" className="mb-1" onClick={() => void testNotify()}>
              <BellRing /> 测试提醒
            </Button>
          </div>
        </Panel>

        <div className="flex flex-wrap items-center gap-2">
          <Stat label="监控" value={ui.rows.length} />
          <Stat label="有货" value={summary.inStock} tone="inStock" />
          <Stat label="无货" value={summary.outOfStock} />
          {summary.untrusted > 0 && (
            // 「其中多少项查不到」单独点出来：这个数字大于 0 时，
            // 表格里那些「无货」也未必反映真实情况。
            <Stat label="查不到" value={summary.untrusted} tone="unknown" />
          )}
          {ui.running && ui.pacing !== null && ui.pacing.paced && (
            // 请求预算在拉长间隔时明说，否则用户会以为设的 30 秒没生效。
            <span className="text-muted-foreground ml-auto text-sm">
              受 Apple 频率限制，下一轮 {ui.pacing.nextCheckInSecs} 秒后
            </span>
          )}
        </div>

        <section className="bg-card min-h-0 flex-1 overflow-hidden rounded-xl border shadow-xs">
          <ScrollArea className="h-full">
            <Table>
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  <TableHead className="text-muted-foreground w-24 pl-4 text-xs">状态</TableHead>
                  <TableHead className="text-muted-foreground text-xs">门店</TableHead>
                  <TableHead className="text-muted-foreground text-xs">型号</TableHead>
                  <TableHead className="text-muted-foreground w-24 text-xs">最后检查</TableHead>
                  <TableHead className="w-12" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {ui.rows.length === 0 ? (
                  <TableRow className="hover:bg-transparent">
                    <TableCell colSpan={5} className="text-muted-foreground h-28 text-center">
                      {ui.ready ? "还没有监控目标。在上面选好门店和型号，点「添加」。" : "正在载入…"}
                    </TableCell>
                  </TableRow>
                ) : (
                  ui.rows.map((row) => (
                    <TableRow
                      key={targetKey(row.target)}
                      // 有货的行整行铺一层有货色。这一刻是这个程序存在的全部理由，
                      // 不该只靠一枚小徽标。
                      className={cn(
                        "odd:bg-muted/30",
                        row.availability.kind === "in_stock" &&
                          "bg-in-stock/10 odd:bg-in-stock/10 hover:bg-in-stock/15",
                      )}
                    >
                      <TableCell className="pl-4">
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

        <section className="bg-card h-28 shrink-0 overflow-hidden rounded-xl border shadow-xs">
          <ScrollArea className="h-full">
            <div className="p-3 font-mono text-xs leading-5 select-text">
              {ui.logs.length === 0 ? (
                <p className="text-muted-foreground">日志会显示在这里。</p>
              ) : (
                ui.logs.map((line, i) => <LogLine key={i} line={line} />)
              )}
            </div>
          </ScrollArea>
        </section>
      </div>
    </TooltipProvider>
  );
}
