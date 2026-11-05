import * as React from "react"

import {
  DropdownMenu as DropdownMenuPrimitive,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"

interface DropdownMenuItemConfig {
  label: string
  icon?: React.ReactNode
  disabled?: boolean
  destructive?: boolean
  onSelect?: () => void
}

interface DropdownMenuProps {
  trigger: React.ReactNode
  items: DropdownMenuItemConfig[]
  align?: React.ComponentProps<typeof DropdownMenuContent>["align"]
  className?: string
}

function DropdownMenu({ trigger, items, align, className }: DropdownMenuProps) {
  return (
    <DropdownMenuPrimitive>
      <DropdownMenuTrigger asChild>{trigger}</DropdownMenuTrigger>
      <DropdownMenuContent align={align} className={className}>
        {items.map((item, index) => (
          <DropdownMenuItem
            key={index}
            disabled={item.disabled}
            variant={item.destructive ? "destructive" : "default"}
            onSelect={item.onSelect}
          >
            {item.icon}
            {item.label}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenuPrimitive>
  )
}

export { DropdownMenu }
export type { DropdownMenuItemConfig, DropdownMenuProps }
