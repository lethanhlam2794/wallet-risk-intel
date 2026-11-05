import * as React from "react"

import {
  Tooltip as TooltipPrimitive,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip"

interface TooltipProps {
  content: React.ReactNode
  side?: React.ComponentProps<typeof TooltipContent>["side"]
  delayDuration?: number
  className?: string
  children: React.ReactNode
}

function Tooltip({
  content,
  side,
  delayDuration = 0,
  className,
  children,
}: TooltipProps) {
  return (
    <TooltipProvider delayDuration={delayDuration}>
      <TooltipPrimitive>
        <TooltipTrigger asChild>{children}</TooltipTrigger>
        <TooltipContent side={side} className={className}>
          {content}
        </TooltipContent>
      </TooltipPrimitive>
    </TooltipProvider>
  )
}

export { Tooltip }
export type { TooltipProps }
