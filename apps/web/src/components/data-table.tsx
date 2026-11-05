import * as React from "react"
import {
  flexRender,
  getCoreRowModel,
  getPaginationRowModel,
  getSortedRowModel,
  useReactTable,
  type ColumnDef,
  type PaginationState,
  type SortingState,
} from "@tanstack/react-table"
import { ArrowDownIcon, ArrowUpDownIcon, ArrowUpIcon } from "lucide-react"

import { cn } from "@/lib/utils"
import { EmptyState } from "@/components/empty-state"
import {
  Pagination,
  PaginationContent,
  PaginationItem,
  PaginationLink,
  PaginationNext,
  PaginationPrevious,
} from "@/components/ui/pagination"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"

interface DataTableProps<TData, TValue> {
  data: TData[]
  columns: ColumnDef<TData, TValue>[]
  loading?: boolean
  emptyMessage?: string
  onRowClick?: (row: TData) => void
  getRowId?: (row: TData, index: number) => string
  pageSize?: number
  initialSorting?: SortingState
  className?: string
}

const SKELETON_ROW_COUNT = 5

const INTERACTIVE_ELEMENTS =
  'a, button, input, select, textarea, [role="button"], [role="link"], [role="menuitem"], [data-row-click-ignore]'

function isInteractiveEventTarget(target: EventTarget | null): boolean {
  return (
    target instanceof HTMLElement &&
    target.closest(INTERACTIVE_ELEMENTS) !== null
  )
}

function DataTable<TData, TValue>({
  data,
  columns,
  loading = false,
  emptyMessage = "No results.",
  onRowClick,
  getRowId,
  pageSize = 10,
  initialSorting,
  className,
}: DataTableProps<TData, TValue>) {
  const [sorting, setSorting] = React.useState<SortingState>(
    initialSorting ?? [],
  )
  const [pagination, setPagination] = React.useState<PaginationState>({
    pageIndex: 0,
    pageSize,
  })

  const table = useReactTable({
    data,
    columns,
    getRowId,
    state: { sorting, pagination },
    onSortingChange: setSorting,
    onPaginationChange: setPagination,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
    getPaginationRowModel: getPaginationRowModel(),
  })

  const pageCount = table.getPageCount()
  const { pageIndex } = table.getState().pagination
  const canPreviousPage = table.getCanPreviousPage()
  const canNextPage = table.getCanNextPage()
  const rows = table.getRowModel().rows

  return (
    <div className={cn("w-full space-y-2", className)}>
      <Table>
        <TableHeader>
          {table.getHeaderGroups().map((headerGroup) => (
            <TableRow key={headerGroup.id}>
              {headerGroup.headers.map((header) => {
                const sorted = header.column.getIsSorted()
                const sortable = header.column.getCanSort()
                return (
                  <TableHead
                    key={header.id}
                    aria-sort={
                      sortable
                        ? sorted === "asc"
                          ? "ascending"
                          : sorted === "desc"
                            ? "descending"
                            : "none"
                        : undefined
                    }
                  >
                    {header.isPlaceholder ? null : sortable ? (
                      <button
                        type="button"
                        className="inline-flex items-center gap-1 font-medium"
                        onClick={header.column.getToggleSortingHandler()}
                      >
                        {flexRender(
                          header.column.columnDef.header,
                          header.getContext(),
                        )}
                        {sorted === "asc" ? (
                          <ArrowUpIcon data-icon="inline-end" />
                        ) : sorted === "desc" ? (
                          <ArrowDownIcon data-icon="inline-end" />
                        ) : (
                          <ArrowUpDownIcon
                            data-icon="inline-end"
                            className="text-muted-foreground"
                          />
                        )}
                      </button>
                    ) : (
                      flexRender(
                        header.column.columnDef.header,
                        header.getContext(),
                      )
                    )}
                  </TableHead>
                )
              })}
            </TableRow>
          ))}
        </TableHeader>
        <TableBody>
          {loading ? (
            Array.from({ length: SKELETON_ROW_COUNT }).map((_, rowIndex) => (
              <TableRow
                key={rowIndex}
                data-testid="datatable-skeleton-row"
                aria-hidden="true"
              >
                {columns.map((_, cellIndex) => (
                  <TableCell key={cellIndex}>
                    <Skeleton className="h-4 w-full" />
                  </TableCell>
                ))}
              </TableRow>
            ))
          ) : rows.length ? (
            rows.map((row) => (
              <TableRow
                key={row.id}
                data-row-id={row.id}
                tabIndex={onRowClick ? 0 : undefined}
                className={cn(onRowClick && "cursor-pointer")}
                onClick={
                  onRowClick
                    ? (event) => {
                        if (!isInteractiveEventTarget(event.target)) {
                          onRowClick(row.original)
                        }
                      }
                    : undefined
                }
                onKeyDown={
                  onRowClick
                    ? (event) => {
                        if (
                          event.key === "Enter" &&
                          !isInteractiveEventTarget(event.target)
                        ) {
                          onRowClick(row.original)
                        }
                      }
                    : undefined
                }
              >
                {row.getVisibleCells().map((cell) => (
                  <TableCell key={cell.id}>
                    {flexRender(cell.column.columnDef.cell, cell.getContext())}
                  </TableCell>
                ))}
              </TableRow>
            ))
          ) : (
            <TableRow>
              <TableCell colSpan={columns.length} className="p-0">
                <EmptyState title={emptyMessage} />
              </TableCell>
            </TableRow>
          )}
        </TableBody>
      </Table>
      {pageCount > 1 ? (
        <Pagination>
          <PaginationContent>
            <PaginationItem>
              <PaginationPrevious
                href="#"
                aria-disabled={!canPreviousPage}
                tabIndex={canPreviousPage ? 0 : -1}
                onClick={(event) => {
                  event.preventDefault()
                  if (canPreviousPage) {
                    table.previousPage()
                  }
                }}
              />
            </PaginationItem>
            {Array.from({ length: pageCount }).map((_, index) => (
              <PaginationItem key={index}>
                <PaginationLink
                  href="#"
                  isActive={index === pageIndex}
                  onClick={(event) => {
                    event.preventDefault()
                    table.setPageIndex(index)
                  }}
                >
                  {index + 1}
                </PaginationLink>
              </PaginationItem>
            ))}
            <PaginationItem>
              <PaginationNext
                href="#"
                aria-disabled={!canNextPage}
                tabIndex={canNextPage ? 0 : -1}
                onClick={(event) => {
                  event.preventDefault()
                  if (canNextPage) {
                    table.nextPage()
                  }
                }}
              />
            </PaginationItem>
          </PaginationContent>
        </Pagination>
      ) : null}
    </div>
  )
}

export { DataTable }
export type { DataTableProps }
export type { ColumnDef, SortingState } from "@tanstack/react-table"
