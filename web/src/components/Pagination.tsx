interface PaginationProps {
  page: number
  totalPages: number
  onPageChange: (page: number) => void
}

/**
 * Truncated page number bar.
 * Shows: first, last, and neighbors of current page.
 * Example: < 1 ... 4 5 [6] 7 8 ... 20 >
 */
export function Pagination({ page, totalPages, onPageChange }: PaginationProps) {
  if (totalPages <= 1) return null

  const pages = buildPageNumbers(page, totalPages)

  return (
    <nav className="pagination" aria-label="Flow list pagination">
      <button
        className="pagination-btn"
        onClick={() => onPageChange(page - 1)}
        disabled={page === 0}
        aria-label="Previous page"
      >
        &lsaquo; Prev
      </button>

      {pages.map((p, i) =>
        p === null ? (
          <span key={`ellipsis-${i}`} className="pagination-ellipsis" aria-hidden="true">&hellip;</span>
        ) : (
          <button
            key={p}
            className={`pagination-btn pagination-page ${p === page ? 'pagination-active' : ''}`}
            onClick={() => onPageChange(p)}
            aria-label={`Page ${p + 1}`}
            aria-current={p === page ? 'page' : undefined}
          >
            {p + 1}
          </button>
        )
      )}

      <button
        className="pagination-btn"
        onClick={() => onPageChange(page + 1)}
        disabled={page >= totalPages - 1}
        aria-label="Next page"
      >
        Next &rsaquo;
      </button>
    </nav>
  )
}

/** Returns page indices with null for ellipsis gaps. */
function buildPageNumbers(current: number, total: number): (number | null)[] {
  if (total <= 7) {
    return Array.from({ length: total }, (_, i) => i)
  }

  const pages: (number | null)[] = []
  const neighbors = new Set<number>()

  // Always show first and last
  neighbors.add(0)
  neighbors.add(total - 1)

  // Show 2 neighbors around current
  for (let i = current - 2; i <= current + 2; i++) {
    if (i >= 0 && i < total) neighbors.add(i)
  }

  const sorted = [...neighbors].sort((a, b) => a - b)

  for (let i = 0; i < sorted.length; i++) {
    if (i > 0 && sorted[i] - sorted[i - 1] > 1) {
      pages.push(null)
    }
    pages.push(sorted[i])
  }

  return pages
}
