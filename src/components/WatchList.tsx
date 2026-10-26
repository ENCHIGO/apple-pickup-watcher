import { Fragment } from "react";
import { AlertTriangle, Trash2 } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { type ProductGroup, storeAreas, summarize } from "@/lib/groups";
import {
  describeAvailability,
  formatTime,
  type StatusTone,
  type Store,
  type Target,
  type TargetState,
  targetKey,
} from "@/lib/types";
import { cn } from "@/lib/utils";

/**
 * 门店标签的样式。无货是常态，只是一行灰字；有货、未知、待查询才有边框。
 *
 * 「未知」必须和「无货」长得完全不一样：带框、带图标、用告警色。这是整个项目
 * 的意义所在：上游把查询失败显示成「无货」，用户对着一个早已失效的程序空等了
 * 大半年。
 */
const CHIP_CLASS: Record<StatusTone, string> = {
  inStock:
    "border-in-stock/30 bg-in-stock/15 text-in-stock font-medium hover:bg-in-stock/25 data-[state=open]:bg-in-stock/25",
  unknown:
    "border-unknown/40 bg-unknown/10 text-unknown font-medium hover:bg-unknown/20 data-[state=open]:bg-unknown/20",
  pending:
    "border-dashed border-border text-muted-foreground/70 hover:bg-muted data-[state=open]:bg-muted",
  outOfStock:
    "border-transparent text-muted-foreground hover:bg-muted data-[state=open]:bg-muted",
};

/** 状态文字的颜色，组标题后的计数和门店详情里共用。 */
const TONE_TEXT: Record<StatusTone, string> = {
  inStock: "text-in-stock font-medium",
  unknown: "text-unknown font-medium",
  pending: "text-muted-foreground",
  outOfStock: "text-muted-foreground",
};

/** 一家门店。点开看状态详情、失败原因和最后检查时间，也在这里删掉它。 */
function StoreChip({
  row,
  name,
  onRemove,
}: {
  row: TargetState;
  name: string;
  onRemove: (targets: Target[]) => void;
}) {
  const { label, tone, detail } = describeAvailability(row.availability);
  return (
    <Popover>
      <PopoverTrigger asChild>
        <button
          type="button"
          aria-label={`${row.target.storeTitle}：${label}`}
          className={cn(
            "focus-visible:ring-ring/50 inline-flex h-6 items-center gap-1 rounded-md border px-2 text-xs outline-none focus-visible:ring-[3px]",
            CHIP_CLASS[tone],
          )}
        >
          {tone === "inStock" && <span className="bg-in-stock size-1.5 rounded-full" />}
          {tone === "unknown" && <AlertTriangle className="size-3" />}
          {name}
        </button>
      </PopoverTrigger>
      <PopoverContent align="start" className="grid w-auto max-w-80 gap-1 p-3 text-sm">
        <p className="font-medium">{row.target.storeTitle}</p>
        <p className="text-muted-foreground text-xs">{row.target.productName}</p>
        <p className={cn("mt-1", TONE_TEXT[tone])}>{label}</p>
        {detail !== null && <p className="text-muted-foreground text-xs">{detail}</p>}
        <p className="text-muted-foreground text-xs tabular-nums">
          {row.lastCheckedMs === null ? "还没查过" : `最后检查 ${formatTime(row.lastCheckedMs)}`}
        </p>
        <Button
          variant="ghost"
          size="sm"
          className="text-muted-foreground hover:text-destructive mt-1 -ml-2 h-7 justify-self-start px-2"
          onClick={() => onRemove([row.target])}
        >
          <Trash2 /> 不再监控这家店
        </Button>
      </PopoverContent>
    </Popover>
  );
}

/** 整组删除。一组可能有八家店，误点一下就没了，所以先确认一次。 */
function RemoveGroup({ group, onRemove }: { group: ProductGroup; onRemove: (targets: Target[]) => void }) {
  return (
    <Popover>
      <PopoverTrigger asChild>
        <Button
          variant="ghost"
          size="icon"
          aria-label={`不再监控${group.productName}`}
          className="text-muted-foreground ml-auto size-7 shrink-0 opacity-0 group-hover/card:opacity-100 focus-visible:opacity-100 data-[state=open]:opacity-100"
        >
          <Trash2 className="size-3.5" />
        </Button>
      </PopoverTrigger>
      <PopoverContent align="end" className="grid w-80 gap-3 p-3 text-sm">
        <div className="grid gap-1">
          <p className="font-medium">不再监控 {group.productName}？</p>
          <p className="text-muted-foreground">
            这个型号在 {group.rows.length} 家门店的监控会一起删掉。
          </p>
        </div>
        <Button
          variant="destructive"
          size="sm"
          className="justify-self-end"
          onClick={() => onRemove(group.rows.map((r) => r.target))}
        >
          删除
        </Button>
      </PopoverContent>
    </Popover>
  );
}

function GroupCard({
  group,
  stores,
  region,
  onRemove,
}: {
  group: ProductGroup;
  stores: ReadonlyMap<string, Store>;
  region: string | null;
  onRemove: (targets: Target[]) => void;
}) {
  return (
    <section
      aria-label={group.productName}
      className={cn(
        "group/card bg-card rounded-xl border py-2.5 pr-2 pl-4 shadow-xs",
        group.inStock && "border-in-stock/40 bg-in-stock/5",
      )}
    >
      <div className="flex min-h-7 items-center gap-3">
        <h3 className="min-w-0 truncate text-sm font-medium">{group.productName}</h3>
        {region !== null && <span className="text-muted-foreground shrink-0 text-xs">{region}</span>}
        <p className="shrink-0 text-xs">
          {summarize(group.rows).map((s, i) => (
            <span key={s.label}>
              {i > 0 && <span className="text-muted-foreground/50"> · </span>}
              <span className={TONE_TEXT[s.tone]}>
                {s.count} 家{s.label}
              </span>
            </span>
          ))}
        </p>
        <RemoveGroup group={group} onRemove={onRemove} />
      </div>
      <div className="mt-1 -ml-2 flex flex-wrap items-center gap-x-0.5 gap-y-1.5 pr-2">
        {storeAreas(group.rows, stores).map(({ area, stores: items }, i) => (
          <Fragment key={area}>
            {area !== "" && (
              <span className={cn("text-muted-foreground/60 px-2 text-xs", i > 0 && "ml-3")}>
                {area}
              </span>
            )}
            {items.map(({ row, name }) => (
              <StoreChip key={targetKey(row.target)} row={row} name={name} onRemove={onRemove} />
            ))}
          </Fragment>
        ))}
      </div>
    </section>
  );
}

/**
 * 监控列表：一个型号一张卡，卡里列各家门店。
 *
 * 型号名每组只写一次，门店名去掉共用的地区前缀，状态靠门店标签的样式区分，
 * 不再每行重复一遍型号、一枚「无货」和同一个检查时间。
 */
export function WatchList({
  groups,
  stores,
  regionTitle,
  onRemove,
}: {
  groups: ProductGroup[];
  /** 当前地区目录里的门店，用来把标题拆成「地区」和「店名」。 */
  stores: ReadonlyMap<string, Store>;
  /** 列表里不止一个地区时，组标题旁标出地区名；只有一个地区时为 null。 */
  regionTitle: ((locale: string) => string) | null;
  onRemove: (targets: Target[]) => void;
}) {
  return (
    <div className="grid gap-2 px-6 pb-4">
      {groups.map((group) => (
        <GroupCard
          key={group.key}
          group={group}
          stores={stores}
          region={regionTitle === null ? null : regionTitle(group.locale)}
          onRemove={onRemove}
        />
      ))}
    </div>
  );
}
