/** GitLabMark is a plain tanuki, drawn in the current color. */
export function GitLabMark({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true" className={className}>
      <path
        fill="currentColor"
        d="M8 15.2 0.9 10 2.8 1.4 5 6.2H11L13.2 1.4 15.1 10Z"
      />
    </svg>
  )
}
