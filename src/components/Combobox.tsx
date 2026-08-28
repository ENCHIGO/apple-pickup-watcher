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

export interface ComboboxOption {
  value: string;
  label: string;
}

/**
 * 可搜索的下拉。
 *
 * 门店有 49 个、型号有几百个，普通 select 只能一路滚，实际用起来很痛苦，
 * 尤其是在发售当晚要快。所以搜索是硬需求，不是锦上添花。
 */
export function Combobox({
  options,
  value,
  onChange,
  placeholder,
  searchPlaceholder,
  emptyText,
  disabled,
  className,
}: {
  options: ComboboxOption[];
  value: string;
  onChange: (value: string) => void;
  placeholder: string;
  searchPlaceholder: string;
  emptyText: string;
  disabled?: boolean;
  className?: string;
}) {
  const [open, setOpen] = useState(false);
  const selected = options.find((o) => o.value === value);

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          variant="outline"
          role="combobox"
          aria-expanded={open}
          disabled={disabled}
          className={cn("justify-between font-normal", className)}
        >
          <span
            // 触发器宽度是固定的，选中项再长也只能截断；挂个原生 title，
            // 想确认自己选的到底是哪一台时鼠标悬停就能看全。
            title={selected?.label}
            className={cn("truncate", !selected && "text-muted-foreground")}
          >
            {selected ? selected.label : placeholder}
          </span>
          <ChevronsUpDown className="opacity-50" />
        </Button>
      </PopoverTrigger>
      <PopoverContent
        // 下拉面板可以比触发器宽。Mac 的展示名带着芯片和核心数，动辄五十来个
        // 字（「MacBook Pro 16 英寸 M5 Max 芯片 18 核中央处理器、40 核图形处理器
        // 标准显示屏 深空黑色」），面板要是跟触发器一样宽，被截掉的正好是区分
        // 两台机器的那半句 —— 用户面对两条看起来一模一样的选项只能猜。
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
                  onSelect={() => {
                    onChange(option.value);
                    setOpen(false);
                  }}
                >
                  <Check
                    className={cn(
                      option.value === value ? "opacity-100" : "opacity-0",
                    )}
                  />
                  {/* 宽到 42rem 还放不下就折行，不截断：截断会把区分项藏起来。 */}
                  <span className="whitespace-normal">{option.label}</span>
                </CommandItem>
              ))}
            </CommandGroup>
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  );
}
