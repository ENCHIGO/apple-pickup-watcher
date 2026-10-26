import { type ReactNode, useEffect, useMemo, useState, useSyncExternalStore } from "react";
import {
  AlertTriangle,
  BellRing,
  Download,
  Pause,
  Play,
  Plus,
  RefreshCw,
  Settings2,
  Trash2,
  X,
} from "lucide-react";

import { Combobox } from "@/components/Combobox";
import { LogFooter } from "@/components/LogFooter";
import { MultiCombobox } from "@/components/MultiCombobox";
import { WatchList } from "@/components/WatchList";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ScrollArea } from "@/components/ui/scroll-area";
import {
  Sheet,
  SheetBody,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { groupByProduct, inAddedOrder, latestCheck } from "@/lib/groups";
import {
  capacityOptions,
  colorOptions,
  familyOptions,
  keepAvailable,
  productsForSelection,
} from "@/lib/product-selection";
import {
  cleanPushRows,
  PUSH_KIND_LABEL,
  type PushKind,
  samePushUrls,
  savedPushAddresses,
} from "@/lib/push";
import {
  changeLocale,
  connect,
  dismissUpdate,
  installUpdate,
  refreshProducts,
  saveSettings,
  setCategory,
  setIntervalSeconds,
  setPushUrls,
  setTargets,
  startWatching,
  stopWatching,
  testNotify,
  watcherStore,
} from "@/lib/store";
import {
  type Category,
  describeAdvice,
  formatTime,
  isUntrusted,
  type Product,
  type Target,
  targetKey,
} from "@/lib/types";
import { cn } from "@/lib/utils";

/** 运行状态。一个会呼吸的点比一行灰字更像「它还活着」。 */
function RunState({ running }: { running: boolean }) {
  return (
    <span className="flex shrink-0 items-center gap-2 text-sm font-medium">
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

/** 添加面板里的一行：左边标签，右边控件。 */
function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid grid-cols-[3rem_minmax(0,1fr)] items-center gap-3">
      <Label className="text-muted-foreground font-normal">{label}</Label>
      {children}
    </div>
  );
}

/** 设置面板里的一节。 */
function SettingsSection({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="grid gap-3">
      <h3 className="text-muted-foreground text-xs font-medium">{title}</h3>
      {children}
    </section>
  );
}

/**
 * 面板打开时焦点落在面板本身，不自动进第一个输入框：设置面板一打开就亮着
 * 「查询间隔」的焦点框，像是在催人改它。按 Tab 照常进入第一个控件。
 */
function focusPanel(event: Event) {
  event.preventDefault();
  (event.currentTarget as HTMLElement | null)?.focus();
}

export default function App() {
  const ui = useSyncExternalStore(watcherStore.subscribe, watcherStore.getSnapshot);

  useEffect(() => {
    void connect();
    // 刻意不在清理函数里断开：这是应用级的单一连接，窗口活着它就该活着。
    // StrictMode 的重复调用由 connect 内部去重。
  }, []);

  // 主窗口只放监控结果。添加和设置是从右侧滑出的面板，用完就收：它们一直摆在
  // 上面的时候，默认窗口里的监控列表只剩三分之一的高度。
  const [sheet, setSheet] = useState<"add" | "settings" | null>(null);
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
  // 刚加进去几个。新的一组排在列表后面，可能被面板挡着或者要滚动才看得到，
  // 面板底部说一声。
  const [justAdded, setJustAdded] = useState(0);
  // 推送地址：Bark 和飞书是同一个列表，一行一个。null 表示还没动过，跟着后端走；
  // 动过之后以这份为准，保持用户填写的顺序，渠道标签按后端的归类显示。
  const [pushDraft, setPushDraft] = useState<string[] | null>(null);
  // 代理地址和推送地址一样一行一个，草稿的规矩也相同，见 pushDraft。
  const [proxyDraft, setProxyDraft] = useState<string[] | null>(null);
  const [intervalDraft, setIntervalDraft] = useState<number | null>(null);

  function openAdd() {
    setJustAdded(0);
    setSheet("add");
  }

  const savedPush = useMemo(() => savedPushAddresses(ui.settings), [ui.settings]);
  const savedPushKind = new Map<string, PushKind>(savedPush.map((p) => [p.url, p.kind]));
  const pushRows = pushDraft ?? savedPush.map((p) => p.url);
  // 一个地址都没有时留一个空行，直接粘贴就行，不用先点「添加」。
  const visiblePushRows = pushRows.length > 0 ? pushRows : [""];
  const lastPushRow = visiblePushRows[visiblePushRows.length - 1] ?? "";

  function editPushRow(index: number, value: string) {
    setPushDraft(visiblePushRows.map((row, i) => (i === index ? value : row)));
  }

  // 失焦和删除都走这里：整理后整组交给后端归类保存；没有实际改动就不写盘。
  // 整理后的列表留作草稿：保存是异步的，这时退回「跟着后端走」，界面会先闪回
  // 旧地址，紧接着点「添加」还会以旧列表为底，把刚填的地址弄丢。
  function commitPushRows(rows: string[]) {
    const urls = cleanPushRows(rows);
    setPushDraft(urls);
    if (!samePushUrls(urls, savedPush.map((p) => p.url))) {
      void setPushUrls(urls);
    }
  }
  // 旧设置文件没有 proxies 字段，读上来是 undefined，当作空列表。
  const savedProxies = ui.settings.proxies ?? [];
  const proxyRows = proxyDraft ?? savedProxies;
  const visibleProxyRows = proxyRows.length > 0 ? proxyRows : [""];
  const lastProxyRow = visibleProxyRows[visibleProxyRows.length - 1] ?? "";

  function editProxyRow(index: number, value: string) {
    setProxyDraft(visibleProxyRows.map((row, i) => (i === index ? value : row)));
  }

  // 每个代理是一条额外的出口线路：Apple 的配额按出口 IP 计，多一条线路多一份配额，
  // 被拦时自动换下一条。后端只留能解析的地址，留不下的那行会标「未生效」。
  function commitProxyRows(rows: string[]) {
    const proxies = cleanPushRows(rows);
    setProxyDraft(proxies);
    if (!samePushUrls(proxies, savedProxies)) {
      void saveSettings({ ...ui.settings, proxies });
    }
  }
  const intervalValue = intervalDraft ?? ui.settings.intervalSeconds;

  const storeOptions = useMemo(
    () => ui.stores.map((s) => ({ value: s.number, label: s.title })),
    [ui.stores],
  );
  const storeIndex = useMemo(() => new Map(ui.stores.map((s) => [s.number, s])), [ui.stores]);
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
  const selectedStores = ui.stores.filter((s) => storeNumbers.includes(s.number));

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

  // 添加、删除都在这份上改，再整份交给后端：按加入的先后排，列表里各组的位置才不会变。
  const targets = useMemo(
    () => inAddedOrder(ui.rows, ui.settings.targets),
    [ui.rows, ui.settings.targets],
  );
  const groups = useMemo(
    () => groupByProduct(ui.rows, ui.settings.targets),
    [ui.rows, ui.settings.targets],
  );
  // 列表里不止一个地区时，组标题旁标出地区：同一个型号名可能在两个地区各有一组。
  const regionTitle = useMemo(() => {
    const locales = new Set(ui.rows.map((r) => r.target.locale));
    if (locales.size < 2) return null;
    return (locale: string) => ui.regions.find((r) => r.locale === locale)?.title ?? locale;
  }, [ui.rows, ui.regions]);

  const summary = useMemo(() => {
    let inStock = 0;
    let untrusted = 0;
    for (const r of ui.rows) {
      if (r.availability.kind === "in_stock") inStock += 1;
      if (isUntrusted(r.availability)) untrusted += 1;
    }
    return { inStock, untrusted, lastChecked: latestCheck(ui.rows) };
  }, [ui.rows]);

  // 门店 × 型号，每个组合一条目标；已经在列表里的跳过，不重复。
  const newTargets = useMemo(() => {
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
    return added;
  }, [targets, storeNumbers, selectedProducts, ui.stores, ui.settings.locale]);
  const combos = storeNumbers.length * selectedProducts.length;

  async function onAdd() {
    if (newTargets.length === 0) return;
    const added = newTargets;
    await setTargets([...targets, ...added]);
    // 只清型号，门店留着，面板也不关：接着给同一批门店加下一个型号是最常见的操作。
    resetProductSelection();
    setJustAdded(added.length);
  }

  async function onRemove(remove: Target[]) {
    const keys = new Set(remove.map(targetKey));
    await setTargets(targets.filter((t) => !keys.has(targetKey(t))));
  }

  // 顶栏的一行状态：怎么在查、上一轮什么时候、查出了什么。
  const status: { text: string; className?: string }[] = [];
  if (ui.running) {
    status.push({
      // 请求预算在拉长间隔时明说，否则用户会以为设的 30 秒没生效。
      text:
        ui.pacing !== null && ui.pacing.paced
          ? `受 Apple 频率限制，下一轮 ${ui.pacing.nextCheckInSecs} 秒后`
          : `每 ${ui.settings.intervalSeconds} 秒查一轮`,
    });
  }
  if (summary.lastChecked !== null) {
    status.push({ text: `上次 ${formatTime(summary.lastChecked)}` });
  }
  if (ui.rows.length > 0) status.push({ text: `${ui.rows.length} 个目标` });
  if (summary.inStock > 0) {
    status.push({ text: `${summary.inStock} 个有货`, className: "text-in-stock font-medium" });
  }
  if (summary.untrusted > 0) {
    // 「其中多少项查不到」一直点出来：这个数字大于 0 时，
    // 列表里那些「无货」也未必反映真实情况。
    status.push({ text: `${summary.untrusted} 个查不到`, className: "text-unknown font-medium" });
  }

  return (
    <TooltipProvider delayDuration={200}>
      <div className="mx-auto flex h-screen max-w-6xl flex-col">
        <header className="flex items-center gap-3 px-6 pt-4 pb-3">
          <RunState running={ui.running} />
          <p
            className="text-muted-foreground min-w-0 flex-1 truncate text-sm"
            title={status.map((s) => s.text).join(" · ")}
          >
            {status.map((s, i) => (
              <span key={s.text}>
                {i > 0 && " · "}
                <span className={s.className}>{s.text}</span>
              </span>
            ))}
          </p>
          <Button variant="ghost" onClick={openAdd}>
            <Plus /> 添加
          </Button>
          <Button variant="ghost" onClick={() => setSheet("settings")}>
            <Settings2 /> 设置
          </Button>
          {ui.running ? (
            <Button variant="secondary" onClick={() => void stopWatching()}>
              <Pause /> 暂停
            </Button>
          ) : (
            <Button onClick={() => void startWatching()} disabled={ui.rows.length === 0}>
              <Play /> 开始
            </Button>
          )}
        </header>

        {(ui.trouble !== null || ui.update !== null) && (
          <div className="grid gap-2 px-6 pb-3">
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
                    恢复之前，列表里的状态不代表门店的真实库存
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
          </div>
        )}

        {ui.rows.length === 0 ? (
          <div className="flex min-h-0 flex-1 flex-col items-center justify-center gap-3 pb-16">
            {ui.ready ? (
              <>
                <p className="text-muted-foreground text-sm">还没有监控目标</p>
                <Button onClick={openAdd}>
                  <Plus /> 添加监控目标
                </Button>
              </>
            ) : (
              <p className="text-muted-foreground text-sm">正在载入…</p>
            )}
          </div>
        ) : (
          <ScrollArea className="min-h-0 flex-1">
            <WatchList
              groups={groups}
              stores={storeIndex}
              regionTitle={regionTitle}
              onRemove={(remove) => void onRemove(remove)}
            />
          </ScrollArea>
        )}

        <LogFooter logs={ui.logs} />
      </div>

      <Sheet open={sheet === "add"} onOpenChange={(open) => setSheet(open ? "add" : null)}>
        <SheetContent aria-describedby={undefined} onOpenAutoFocus={focusPanel}>
          <SheetHeader>
            <SheetTitle>添加监控目标</SheetTitle>
          </SheetHeader>
          <SheetBody className="grid content-start gap-3">
            <Field label="地区">
              <Combobox
                className="w-full"
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
            </Field>
            <Field label="门店">
              <MultiCombobox
                className="w-full"
                options={storeOptions}
                values={storeNumbers}
                onChange={setStoreNumbers}
                placeholder="选择自提门店"
                searchPlaceholder="搜索门店…"
                emptyText="没有匹配的门店"
                countLabel={(n) => `已选 ${n} 家门店`}
                disabled={storeOptions.length === 0}
              />
            </Field>
            {selectedStores.length > 1 && (
              // 门店在面板收起后仍然保留，下次打开要能直接看到是哪几家；
              // 触发器上只放得下一个数字。
              <p
                className="text-muted-foreground -mt-1 line-clamp-2 pl-15 text-xs"
                title={selectedStores.map((s) => s.title).join("、")}
              >
                {selectedStores.map((s) => s.title).join("、")}
              </p>
            )}

            <div className="my-1 border-t" />

            <Field label="品类">
              <Combobox
                className="w-full"
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
            </Field>
            {ui.category === "iphone" ? (
              <>
                <Field label="机型">
                  <MultiCombobox
                    className="w-full"
                    options={iphoneFamilyOptions}
                    values={families}
                    onChange={onFamiliesChange}
                    placeholder="选择机型"
                    searchPlaceholder="搜索机型…"
                    emptyText="没有匹配的机型"
                    countLabel={(n) => `已选 ${n} 款机型`}
                    disabled={iphoneFamilyOptions.length === 0}
                  />
                </Field>
                <Field label="容量">
                  <MultiCombobox
                    className="w-full"
                    options={iphoneCapacityOptions}
                    values={capacities}
                    onChange={onCapacitiesChange}
                    placeholder="选择容量"
                    searchPlaceholder="搜索容量…"
                    emptyText="没有匹配的容量"
                    countLabel={(n) => `已选 ${n} 种容量`}
                    disabled={families.length === 0 || iphoneCapacityOptions.length === 0}
                  />
                </Field>
                <Field label="颜色">
                  <MultiCombobox
                    className="w-full"
                    options={iphoneColorOptions}
                    values={colors}
                    onChange={setColors}
                    placeholder="选择颜色"
                    searchPlaceholder="搜索颜色…"
                    emptyText="没有匹配的颜色"
                    countLabel={(n) => `已选 ${n} 种颜色`}
                    disabled={capacities.length === 0 || iphoneColorOptions.length === 0}
                  />
                </Field>
              </>
            ) : (
              <Field label="型号">
                <Combobox
                  className="w-full"
                  options={productOptions}
                  value={partNumber}
                  onChange={setPartNumber}
                  placeholder="选择型号"
                  searchPlaceholder="搜索型号…"
                  emptyText="没有匹配的型号"
                  disabled={productOptions.length === 0}
                />
              </Field>
            )}
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  variant="link"
                  size="sm"
                  className="text-muted-foreground hover:text-foreground ml-15 h-auto justify-self-start p-0 text-xs font-normal has-[>svg]:px-0 [&_svg]:size-3"
                  disabled={ui.refreshing}
                  onClick={() => void refreshProducts()}
                >
                  <RefreshCw className={ui.refreshing ? "animate-spin" : undefined} />
                  更新型号列表
                </Button>
              </TooltipTrigger>
              <TooltipContent>
                从 Apple 官网更新当前品类的型号列表。新机发布后用这个，不必等程序更新。
              </TooltipContent>
            </Tooltip>
          </SheetBody>
          <SheetFooter>
            <p className="text-muted-foreground min-w-0 flex-1 text-sm">
              {combos > 0 ? (
                // 多选时把要加的规模点明：三级各勾几项之后，组合数并不直观。
                <span title={selectedProducts.map((p) => p.title).join("、")}>
                  {selectedProducts.length} 个型号 × {storeNumbers.length} 家门店
                  {combos > newTargets.length
                    ? newTargets.length === 0
                      ? "，都已在监控列表里"
                      : `，其中 ${combos - newTargets.length} 个已在监控列表里`
                    : null}
                </span>
              ) : justAdded > 0 ? (
                `已添加 ${justAdded} 个`
              ) : null}
            </p>
            <Button onClick={() => void onAdd()} disabled={newTargets.length === 0}>
              <Plus /> 添加
            </Button>
          </SheetFooter>
        </SheetContent>
      </Sheet>

      <Sheet
        open={sheet === "settings"}
        onOpenChange={(open) => setSheet(open ? "settings" : null)}
      >
        <SheetContent onOpenAutoFocus={focusPanel}>
          <SheetHeader>
            <SheetTitle>设置</SheetTitle>
            <SheetDescription>改动即时保存</SheetDescription>
          </SheetHeader>
          <SheetBody className="grid content-start gap-7">
            <SettingsSection title="查询">
              <div className="flex items-center justify-between gap-4">
                <Label htmlFor="interval">查询间隔</Label>
                <div className="flex items-center gap-2">
                  <Input
                    id="interval"
                    type="number"
                    min={5}
                    className="h-8 w-20 select-text"
                    value={intervalValue}
                    onChange={(e) => setIntervalDraft(e.target.valueAsNumber)}
                    onBlur={() => {
                      const s = Number.isFinite(intervalValue) ? Math.round(intervalValue) : 30;
                      setIntervalDraft(null);
                      void setIntervalSeconds(s);
                    }}
                  />
                  <span className="text-muted-foreground text-sm">秒</span>
                </div>
              </div>
            </SettingsSection>

            <SettingsSection title="提醒">
              <div className="flex items-center justify-between gap-4">
                <Label htmlFor="sound">提示音</Label>
                <Switch
                  id="sound"
                  checked={ui.settings.soundEnabled}
                  onCheckedChange={(v) => void saveSettings({ ...ui.settings, soundEnabled: v })}
                />
              </div>
              <div className="flex items-center justify-between gap-4">
                <Label htmlFor="openbag">有货时打开购物袋</Label>
                <Switch
                  id="openbag"
                  checked={ui.settings.openBagOnHit}
                  onCheckedChange={(v) => void saveSettings({ ...ui.settings, openBagOnHit: v })}
                />
              </div>
              <div className="grid gap-1.5 pt-1">
                <div className="flex items-center gap-2">
                  <Label htmlFor="push-0" className="shrink-0">
                    推送地址
                  </Label>
                  <span className="text-muted-foreground min-w-0 truncate text-xs">
                    Bark 或飞书群机器人，一行一个
                  </span>
                  {lastPushRow.trim() !== "" && (
                    <Button
                      variant="link"
                      size="sm"
                      className="text-muted-foreground hover:text-foreground ml-auto h-auto shrink-0 gap-1 p-0 text-xs has-[>svg]:px-0 [&_svg]:size-3"
                      onClick={() => setPushDraft([...cleanPushRows(visiblePushRows), ""])}
                    >
                      <Plus /> 添加推送地址
                    </Button>
                  )}
                </div>
                {visiblePushRows.map((row, index) => {
                  const kind = savedPushKind.get(row.trim());
                  return (
                    <div key={index} className="flex items-center gap-2">
                      {/* 渠道标签占固定宽度：还没保存的行留空，输入框照样对齐。 */}
                      <span className="flex w-12 shrink-0 justify-center">
                        {kind !== undefined && (
                          <Badge variant="outline" className="text-muted-foreground">
                            {PUSH_KIND_LABEL[kind]}
                          </Badge>
                        )}
                      </span>
                      <Input
                        id={`push-${index}`}
                        aria-label={`推送地址 ${index + 1}`}
                        className="select-text"
                        placeholder="Bark 地址，或飞书群机器人的 webhook"
                        value={row}
                        // 只有点「添加」新开的那一行自动聚焦；打开面板时的空行不抢焦点。
                        autoFocus={pushDraft !== null && row === "" && index > 0}
                        onChange={(e) => editPushRow(index, e.target.value)}
                        onBlur={() => commitPushRows(visiblePushRows)}
                      />
                      <Button
                        variant="ghost"
                        size="icon"
                        aria-label={`删除推送地址 ${index + 1}`}
                        disabled={visiblePushRows.length === 1 && row === ""}
                        onClick={() =>
                          commitPushRows(visiblePushRows.filter((_, i) => i !== index))
                        }
                      >
                        <Trash2 />
                      </Button>
                    </div>
                  );
                })}
                {savedPush.some((p) => p.kind === "feishu") && (
                  // 飞书机器人最常见的配置错误是安全设置，只在真填了飞书地址时提醒。
                  <p className="text-muted-foreground pl-14 text-xs">
                    飞书机器人的安全设置请选「自定义关键词」，填「有货」。
                  </p>
                )}
              </div>
              {/* 测的就是上面这些：提示音、系统通知和推送一起发一遍。 */}
              <Button
                variant="outline"
                size="sm"
                className="justify-self-start"
                onClick={() => void testNotify()}
              >
                <BellRing /> 测试提醒
              </Button>
            </SettingsSection>

            <SettingsSection title="网络">
              <div className="grid gap-1.5">
                <div className="flex items-center gap-2">
                  <Label htmlFor="proxy-0" className="shrink-0">
                    代理地址
                  </Label>
                  <span className="text-muted-foreground min-w-0 truncate text-xs">
                    http / https / socks5，一行一个
                  </span>
                  {lastProxyRow.trim() !== "" && (
                    <Button
                      variant="link"
                      size="sm"
                      className="text-muted-foreground hover:text-foreground ml-auto h-auto shrink-0 gap-1 p-0 text-xs has-[>svg]:px-0 [&_svg]:size-3"
                      onClick={() => setProxyDraft([...cleanPushRows(visibleProxyRows), ""])}
                    >
                      <Plus /> 添加代理
                    </Button>
                  )}
                </div>
                {visibleProxyRows.map((row, index) => {
                  // 保存后后端没留下这一行：地址解析不了，这条线路不存在。
                  const rejected =
                    proxyDraft !== null && row.trim() !== "" && !savedProxies.includes(row.trim());
                  return (
                    <div key={index} className="flex items-center gap-2">
                      <Input
                        id={`proxy-${index}`}
                        aria-label={`代理地址 ${index + 1}`}
                        aria-invalid={rejected || undefined}
                        className="select-text"
                        placeholder="socks5://127.0.0.1:1080 或 http://host:port"
                        value={row}
                        autoFocus={proxyDraft !== null && row === "" && index > 0}
                        onChange={(e) => editProxyRow(index, e.target.value)}
                        onBlur={() => commitProxyRows(visibleProxyRows)}
                      />
                      {rejected && (
                        <Badge
                          variant="outline"
                          className="text-destructive border-destructive/40 shrink-0"
                          title="地址格式不对，这条线路没有生效。支持 http、https、socks5、socks5h，例如 socks5://127.0.0.1:1080"
                        >
                          未生效
                        </Badge>
                      )}
                      <Button
                        variant="ghost"
                        size="icon"
                        aria-label={`删除代理地址 ${index + 1}`}
                        disabled={visibleProxyRows.length === 1 && row === ""}
                        onClick={() =>
                          commitProxyRows(visibleProxyRows.filter((_, i) => i !== index))
                        }
                      >
                        <Trash2 />
                      </Button>
                    </div>
                  );
                })}
              </div>
            </SettingsSection>
          </SheetBody>
        </SheetContent>
      </Sheet>
    </TooltipProvider>
  );
}
