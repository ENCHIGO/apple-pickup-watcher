import { useState } from "react";
import { Check, ChevronsUpDown } from "lucide-react";

import { Button } from "@/components/ui/button";
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { cn } from "@/lib/utils";

import type { ComboboxOption } from "@/components/Combobox";

/**
 * 可搜索的多选下拉，用于一次选好几家门店。
 *
 * 和单选 [`Combobox`] 的差别只有三处：选中项是数组、点一项不关面板、面板底部
 * 有「全选 / 清空」。同城几家店合并成一次请求之后（v0.4.2），多选门店不再增加
 * 请求量，这个控件才值得做（issue #31）。
 */
export function MultiCombobox({
  options,
  values,
  onChange,
  placeholder,
  searchPlaceholder,
  emptyText,
  countLabel = (n) => `已选 ${n} 项`,
  disabled,
  className,
}: {
  options: ComboboxOption[];
  values: string[];
  onChange: (values: string[]) => void;
  placeholder: string;
  searchPlaceholder: string;
  emptyText: string;
  /** 选中两项以上时触发器上显示的文字。 */
  countLabel?: (count: number) => string;
  disabled?: boolean;
  className?: string;
}) {
  const [open, setOpen] = useState(false);
  const selected = options.filter((o) => values.includes(o.value));
  const summary =
    selected.length === 0
      ? placeholder
      : selected.length === 1
        ? (selected[0]?.label ?? placeholder)
        : countLabel(selected.length);

  function toggle(value: string) {
    onChange(
      values.includes(value) ? values.filter((v) => v !== value) : [...values, value],
    );
  }

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          variant="outline"
          role="combobox"
          aria-expanded={open}
          disabled={disabled}
          className={cn("bg-card justify-between font-normal", className)}
        >
          <span
            // 悬停能看全选了哪几家；触发器本身只放得下一个数字。
            title={selected.map((o) => o.label).join("、")}
            className={cn("truncate", selected.length === 0 && "text-muted-foreground")}
          >
            {summary}
          </span>
          <ChevronsUpDown className="opacity-50" />
        </Button>
      </PopoverTrigger>
      <PopoverContent
        className="w-auto min-w-(--radix-popover-trigger-width) max-w-[min(90vw,42rem)] p-0"
        align="start"
      >
        <Command>
          <CommandInput placeholder={searchPlaceholder} className="select-text" />
          <CommandList>
            <CommandEmpty>{emptyText}</CommandEmpty>
            <CommandGroup>
              {options.map((option) => (
                <CommandItem
                  key={option.value}
                  value={option.label}
                  // 多选时不关面板：用户是来一口气勾好几家的，每勾一个就关会很烦。
                  onSelect={() => toggle(option.value)}
                >
                  <Check
                    className={cn(
                      values.includes(option.value) ? "opacity-100" : "opacity-0",
                    )}
                  />
                  <span className="whitespace-normal">{option.label}</span>
                </CommandItem>
              ))}
            </CommandGroup>
          </CommandList>
          <div className="flex items-center justify-between border-t px-2 py-1.5 text-xs">
            <span className="text-muted-foreground">{countLabel(selected.length)}</span>
            <div className="flex gap-1">
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="h-7 px-2"
                onClick={() => onChange(options.map((o) => o.value))}
                disabled={options.length === 0 || selected.length === options.length}
              >
                全选
              </Button>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="h-7 px-2"
                onClick={() => onChange([])}
                disabled={selected.length === 0}
              >
                清空
              </Button>
            </div>
          </div>
        </Command>
      </PopoverContent>
    </Popover>
  );
}
