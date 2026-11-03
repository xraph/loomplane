/** BitbucketMark is a plain bucket, drawn in the current color. */
export function BitbucketMark({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 24 24" aria-hidden="true" className={className}>
      <path
        fill="currentColor"
        d="M.78 1.21a.77.77 0 0 0-.77.89l3.26 19.81c.09.5.52.87 1.02.87h15.66a.77.77 0 0 0 .77-.65l3.27-20.03a.77.77 0 0 0-.77-.89Zm13.74 14.32H9.52L8.17 8.47h7.56Z"
      />
    </svg>
  )
}
