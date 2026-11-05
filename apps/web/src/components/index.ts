// Public barrel — convenience wrappers own their bare names; colliding
// generated primitives are exported under `*Primitive` aliases.

export { Button, buttonVariants } from "./ui/button"
export { Input } from "./ui/input"
export { Label } from "./ui/label"
export { Separator } from "./ui/separator"
export { Skeleton } from "./ui/skeleton"
export { Spinner } from "./ui/spinner"
export { Badge, badgeVariants } from "./ui/badge"
export {
  Alert,
  AlertTitle,
  AlertDescription,
  AlertAction,
} from "./ui/alert"
export {
  Card,
  CardHeader,
  CardFooter,
  CardTitle,
  CardAction,
  CardDescription,
  CardContent,
} from "./ui/card"
export {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogOverlay,
  DialogPortal,
  DialogTitle,
  DialogTrigger,
} from "./ui/dialog"
export {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectScrollDownButton,
  SelectScrollUpButton,
  SelectSeparator,
  SelectTrigger,
  SelectValue,
} from "./ui/select"
export {
  Tabs,
  TabsList,
  TabsTrigger,
  TabsContent,
  tabsListVariants,
} from "./ui/tabs"
export {
  Pagination,
  PaginationContent,
  PaginationEllipsis,
  PaginationItem,
  PaginationLink,
  PaginationNext,
  PaginationPrevious,
} from "./ui/pagination"
export {
  Table,
  TableHeader,
  TableBody,
  TableFooter,
  TableHead,
  TableRow,
  TableCell,
  TableCaption,
} from "./ui/table"
export {
  Empty,
  EmptyHeader,
  EmptyTitle,
  EmptyDescription,
  EmptyContent,
  EmptyMedia,
} from "./ui/empty"

// Primitive aliases for names owned by the convenience wrappers.
export {
  Tooltip as TooltipPrimitive,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "./ui/tooltip"
export {
  DropdownMenu as DropdownMenuPrimitive,
  DropdownMenuPortal,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuLabel,
  DropdownMenuItem,
  DropdownMenuCheckboxItem,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuShortcut,
  DropdownMenuSub,
  DropdownMenuSubTrigger,
  DropdownMenuSubContent,
} from "./ui/dropdown-menu"

// Convenience API (typed wrappers + composites).
export { Tooltip } from "./tooltip"
export type { TooltipProps } from "./tooltip"
export { DropdownMenu } from "./dropdown-menu"
export type {
  DropdownMenuItemConfig,
  DropdownMenuProps,
} from "./dropdown-menu"
export { EmptyState } from "./empty-state"
export type { EmptyStateProps } from "./empty-state"
export { DataTable } from "./data-table"
export type { DataTableProps } from "./data-table"

// Toast — Sonner mounted once at app root; `toast` callable anywhere.
export { Toaster } from "./ui/sonner"
export { toast } from "sonner"
